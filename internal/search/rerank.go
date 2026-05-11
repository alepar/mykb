package search

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/austinfhunter/voyageai"
	"golang.org/x/sync/errgroup"

	"mykb/internal/pipeline"
)

// rerankBackend is the narrow surface we use from the Voyage SDK. Tests
// substitute fakes; production wires the real client via voyageBackend.
type rerankBackend interface {
	Rerank(query string, documents []string, model string, opts *voyageai.RerankRequestOpts) (*voyageai.RerankResponse, error)
}

// voyageBackend adapts *voyageai.VoyageClient to rerankBackend.
type voyageBackend struct {
	client *voyageai.VoyageClient
}

func (v *voyageBackend) Rerank(query string, documents []string, model string, opts *voyageai.RerankRequestOpts) (*voyageai.RerankResponse, error) {
	return v.client.Rerank(query, documents, model, opts)
}

// Voyage rerank-2 limits (also valid for rerank-2.5/-lite variants per docs).
const (
	// maxBatchTokens is our safe ceiling under Voyage's 600k batch limit.
	// 8% headroom covers tokenizer-mismatch drift between our cl100k_base
	// estimate and Voyage's Qwen2 tokenizer.
	maxBatchTokens = 550_000

	// maxDocsPerCall is Voyage's hard limit on documents per rerank call.
	maxDocsPerCall = 1000

	// maxPairTokens is Voyage's per-pair (query + single doc) cap for
	// rerank-2 when truncation=true. We clamp per-doc contributions to
	// this when computing the batch budget, matching Voyage's server-side
	// truncation.
	maxPairTokens = 16_000
)

// splitIntoBatches greedily packs docs (referenced by index) into sub-batches
// whose total Voyage cost stays ≤ maxTokens and whose size stays ≤ maxDocs.
//
// Voyage's batch cost formula is:
//
//	query_tokens * N + sum(clamped_doc_tokens_i)
//
// where clamped_doc_tokens_i = min(doc_tokens_i, maxPairTokens - query_tokens).
// The clamp reflects Voyage's server-side per-pair truncation.
//
// Order is preserved: each returned slice contains a contiguous run of input
// indices. A document whose clamped cost alone exceeds maxTokens still gets a
// singleton batch (Voyage will truncate it server-side).
func splitIntoBatches(queryTokens int, docTokens []int, maxTokens, maxDocs int) [][]int {
	if len(docTokens) == 0 {
		return nil
	}

	perDocCap := maxPairTokens - queryTokens
	if perDocCap < 0 {
		perDocCap = 0
	}

	clamp := func(d int) int {
		if d > perDocCap {
			return perDocCap
		}
		return d
	}

	var groups [][]int
	var current []int
	currentCost := 0

	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
			currentCost = 0
		}
	}

	for i, d := range docTokens {
		add := queryTokens + clamp(d)
		// If adding this doc would overflow, flush first.
		if len(current) > 0 && (currentCost+add > maxTokens || len(current) >= maxDocs) {
			flush()
		}
		current = append(current, i)
		currentCost += add
	}
	flush()
	return groups
}

// Reranker re-scores documents against a query using the Voyage AI rerank API.
type Reranker struct {
	backend rerankBackend
	model   string
}

// RerankResult holds the original index of a document in the input slice
// and its relevance score assigned by the reranker.
type RerankResult struct {
	Index int
	Score float64
}

// NewReranker creates a Reranker that calls the Voyage AI rerank API.
// If model is empty it defaults to "rerank-2".
func NewReranker(apiKey, model string) *Reranker {
	if model == "" {
		model = "rerank-2"
	}
	client := voyageai.NewClient(&voyageai.VoyageClientOpts{Key: apiKey})
	return &Reranker{
		backend: &voyageBackend{client: client},
		model:   model,
	}
}

// Rerank sends the query and documents to Voyage's rerank endpoint and returns
// the top-K results sorted by relevance score descending. Each RerankResult's
// Index references the caller's original `documents` slice.
//
// When the total batch would exceed Voyage's 600k token limit (or the 1000
// docs-per-call limit), the request is transparently split into sub-batches
// dispatched in parallel; per-batch results are then merged by score. The
// scoring is preserved because Voyage's reranker is a cross-encoder
// (per-pair scoring, comparable across calls).
func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	queryTokens := pipeline.CountTokens(query)
	docTokens := make([]int, len(documents))
	for i, d := range documents {
		docTokens[i] = pipeline.CountTokens(d)
	}

	groups := splitIntoBatches(queryTokens, docTokens, maxBatchTokens, maxDocsPerCall)
	if len(groups) > 1 {
		total := 0
		for _, d := range docTokens {
			total += d
		}
		log.Printf("voyage rerank: split %d candidates into %d sub-batches (query=%d tokens, total docs=%d tokens)",
			len(documents), len(groups), queryTokens, total)
	}

	// Per-batch topK: ask Voyage for all results so we have everything to
	// merge across batches; we trim globally after merge.
	var (
		mu  sync.Mutex
		all []RerankResult
	)

	g, gctx := errgroup.WithContext(ctx)
	for _, group := range groups {
		group := group // capture
		g.Go(func() error {
			subDocs := make([]string, len(group))
			for i, idx := range group {
				subDocs[i] = documents[idx]
			}
			resp, err := r.backend.Rerank(query, subDocs, r.model, &voyageai.RerankRequestOpts{})
			if err != nil {
				return fmt.Errorf("voyage rerank: %w", err)
			}
			// Re-check the context after a possibly-slow call.
			if err := gctx.Err(); err != nil {
				return err
			}
			local := make([]RerankResult, len(resp.Data))
			for i, obj := range resp.Data {
				// obj.Index is local to subDocs; map back to global.
				if obj.Index < 0 || obj.Index >= len(group) {
					return fmt.Errorf("voyage rerank: backend returned out-of-range index %d (sub-batch size %d)", obj.Index, len(group))
				}
				local[i] = RerankResult{
					Index: group[obj.Index],
					Score: float64(obj.RelevanceScore),
				}
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Score > all[j].Score
	})
	if topK > 0 && len(all) > topK {
		all = all[:topK]
	}
	return all, nil
}

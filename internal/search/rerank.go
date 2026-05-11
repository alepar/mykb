package search

import (
	"context"
	"fmt"
	"sort"

	"github.com/austinfhunter/voyageai"
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

// Rerank sends the query and documents to the Voyage AI rerank endpoint and
// returns the top-K results sorted by relevance score in descending order.
// Each RerankResult carries the original index of the document in the input
// slice together with the relevance score.
func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	var tk *int
	if topK > 0 {
		tk = &topK
	}

	resp, err := r.backend.Rerank(query, documents, r.model, &voyageai.RerankRequestOpts{
		TopK: tk,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage rerank: %w", err)
	}

	results := make([]RerankResult, len(resp.Data))
	for i, obj := range resp.Data {
		results[i] = RerankResult{
			Index: obj.Index,
			Score: float64(obj.RelevanceScore),
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

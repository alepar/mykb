package search

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/austinfhunter/voyageai"
)

func TestRerankResultSortAndLimit(t *testing.T) {
	// Verify that the types compile and basic sorting/limiting logic works
	// by exercising the helpers outside of a live API call.
	results := []RerankResult{
		{Index: 0, Score: 0.1},
		{Index: 1, Score: 0.9},
		{Index: 2, Score: 0.5},
		{Index: 3, Score: 0.7},
	}

	// Sort descending by score (same logic as Rerank method).
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if results[0].Index != 1 || results[1].Index != 3 {
		t.Fatalf("unexpected order: %+v", results)
	}

	// Limit to topK=2.
	topK := 2
	if len(results) > topK {
		results = results[:topK]
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Score < results[1].Score {
		t.Fatal("results not sorted descending")
	}
}

func TestNewRerankerDefaults(t *testing.T) {
	r := NewReranker("fake-key", "")
	if r.model != "rerank-2" {
		t.Fatalf("expected default model rerank-2, got %s", r.model)
	}
	if r.backend == nil {
		t.Fatal("backend should not be nil")
	}
}

func TestNewRerankerCustomModel(t *testing.T) {
	r := NewReranker("fake-key", "rerank-2-lite")
	if r.model != "rerank-2-lite" {
		t.Fatalf("expected model rerank-2-lite, got %s", r.model)
	}
}

func TestRerankEmptyDocuments(t *testing.T) {
	r := NewReranker("fake-key", "rerank-2")
	results, err := r.Rerank(context.Background(), "query", nil, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results for empty documents, got %+v", results)
	}
}

func TestRerankIntegration(t *testing.T) {
	apiKey := os.Getenv("TEST_VOYAGE_API_KEY")
	if apiKey == "" {
		t.Skip("TEST_VOYAGE_API_KEY not set, skipping integration test")
	}

	r := NewReranker(apiKey, "rerank-2")

	docs := []string{
		"The capital of France is Paris.",
		"Go is a statically typed programming language.",
		"Paris has many famous landmarks like the Eiffel Tower.",
		"Rust is a systems programming language.",
	}

	results, err := r.Rerank(context.Background(), "What is the capital of France?", docs, 2)
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// The most relevant document should be index 0 or 2.
	topIdx := results[0].Index
	if topIdx != 0 && topIdx != 2 {
		t.Errorf("expected top result to be index 0 or 2, got %d", topIdx)
	}

	// Scores should be descending.
	if results[0].Score < results[1].Score {
		t.Error("results not sorted by score descending")
	}

	t.Logf("top result: index=%d score=%.4f", results[0].Index, results[0].Score)
	t.Logf("second result: index=%d score=%.4f", results[1].Index, results[1].Score)
}

func TestSplitIntoBatches_UnderBudget(t *testing.T) {
	// 1-token query, 10 docs of 100 tokens each.
	// cost = 1*10 + 10*100 = 1010 tokens, well under 550k.
	docTokens := make([]int, 10)
	for i := range docTokens {
		docTokens[i] = 100
	}
	groups := splitIntoBatches(1, docTokens, 550_000, 1000)
	if len(groups) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(groups))
	}
	if len(groups[0]) != 10 {
		t.Fatalf("expected 10 docs in batch, got %d", len(groups[0]))
	}
	for i, idx := range groups[0] {
		if idx != i {
			t.Fatalf("expected index %d at position %d, got %d", i, i, idx)
		}
	}
}

func TestSplitIntoBatches_OverBudget(t *testing.T) {
	// 1-token query, 1000 docs of 1500 tokens each.
	// per-doc cost (clamped at 16000-1=15999, no clamp triggers) = 1500
	// per-batch cost: 1*N + N*1500 = 1501*N
	// budget 550_000 ⇒ N ≤ 366 per batch
	docTokens := make([]int, 1000)
	for i := range docTokens {
		docTokens[i] = 1500
	}
	groups := splitIntoBatches(1, docTokens, 550_000, 1000)
	if len(groups) < 2 {
		t.Fatalf("expected at least 2 batches, got %d", len(groups))
	}
	// All indices accounted for, in order, no duplicates.
	seen := 0
	for _, g := range groups {
		for _, idx := range g {
			if idx != seen {
				t.Fatalf("expected index %d, got %d", seen, idx)
			}
			seen++
		}
	}
	if seen != 1000 {
		t.Fatalf("expected 1000 indices total, got %d", seen)
	}
	// Each batch respects the budget.
	for bi, g := range groups {
		cost := 1 * len(g)
		for _, idx := range g {
			cost += docTokens[idx]
		}
		if cost > 550_000 {
			t.Fatalf("batch %d cost %d exceeds budget", bi, cost)
		}
	}
}

func TestSplitIntoBatches_GiantDocGetsOwnBatch(t *testing.T) {
	// One enormous doc (clamps to 16k-10=15990) among small docs.
	// With query=10, clamped per-doc = min(d, 16000-10=15990)
	// doc[2] clamps to 15990. Use a tiny budget (16k) to force per-doc isolation.
	// per-batch cost formula: 10*N + sum(clamped).
	// doc[2] alone: 10*1 + 15990 = 16000 ≤ 16000 — fits as singleton.
	// Adding any other doc would push over. So expect doc[2] alone.
	docTokens := []int{50, 50, 100_000, 50, 50}
	groups := splitIntoBatches(10, docTokens, 16_000, 1000)
	foundGiantAlone := false
	for _, g := range groups {
		if len(g) == 1 && g[0] == 2 {
			foundGiantAlone = true
		}
	}
	if !foundGiantAlone {
		t.Fatalf("expected doc[2] to land in its own batch; groups=%v", groups)
	}
	// All indices present in order.
	seen := 0
	for _, g := range groups {
		for _, idx := range g {
			if idx != seen {
				t.Fatalf("expected index %d, got %d", seen, idx)
			}
			seen++
		}
	}
}

func TestSplitIntoBatches_MaxDocsPerCall(t *testing.T) {
	// Tiny docs but lots of them — should hit the max-docs cap, not the
	// token budget.
	docTokens := make([]int, 2500)
	for i := range docTokens {
		docTokens[i] = 1
	}
	groups := splitIntoBatches(1, docTokens, 550_000, 1000)
	if len(groups) != 3 {
		t.Fatalf("expected 3 batches (1000+1000+500), got %d", len(groups))
	}
	if len(groups[0]) != 1000 || len(groups[1]) != 1000 || len(groups[2]) != 500 {
		t.Fatalf("unexpected batch sizes: %d, %d, %d", len(groups[0]), len(groups[1]), len(groups[2]))
	}
}

func TestSplitIntoBatches_Empty(t *testing.T) {
	groups := splitIntoBatches(1, nil, 550_000, 1000)
	if len(groups) != 0 {
		t.Fatalf("expected 0 batches for empty input, got %d", len(groups))
	}
}

// fakeBackend records each Rerank call and returns scores keyed by doc content.
// Each doc string should look like "doc-<n>"; the fake assigns score 1/(n+1) so
// lower-numbered docs rank higher (deterministic, easy to assert on).
type fakeBackend struct {
	mu        sync.Mutex
	calls     int
	callDocs  [][]string
	failCallN int32 // if >0, the Nth call (1-indexed) returns an error
	counter   int32
}

func (f *fakeBackend) Rerank(query string, documents []string, model string, opts *voyageai.RerankRequestOpts) (*voyageai.RerankResponse, error) {
	n := atomic.AddInt32(&f.counter, 1)
	f.mu.Lock()
	f.calls++
	cpy := append([]string(nil), documents...)
	f.callDocs = append(f.callDocs, cpy)
	f.mu.Unlock()
	if f.failCallN > 0 && n == f.failCallN {
		return nil, fmt.Errorf("simulated voyage failure on call %d", n)
	}
	data := make([]voyageai.RerankObject, len(documents))
	for i, d := range documents {
		var num int
		_, _ = fmt.Sscanf(d, "doc-%d", &num)
		data[i] = voyageai.RerankObject{
			Index:          i,
			RelevanceScore: float32(1.0 / float64(num+1)),
		}
	}
	return &voyageai.RerankResponse{Data: data}, nil
}

func newTestReranker(b rerankBackend) *Reranker {
	return &Reranker{backend: b, model: "rerank-2"}
}

func TestRerank_SingleCallUnderBudget(t *testing.T) {
	docs := []string{"doc-0", "doc-1", "doc-2"}
	fb := &fakeBackend{}
	r := newTestReranker(fb)

	results, err := r.Rerank(context.Background(), "q", docs, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.calls != 1 {
		t.Fatalf("expected 1 backend call, got %d", fb.calls)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (topK=2), got %d", len(results))
	}
	if results[0].Index != 0 || results[1].Index != 1 {
		t.Fatalf("expected top results [0, 1], got [%d, %d]", results[0].Index, results[1].Index)
	}
}

func TestRerank_SplitsWhenOverBudget(t *testing.T) {
	// Build docs big enough to force a split under the production budget.
	// Each doc is a long string; with 800 of them the total cost exceeds
	// the maxBatchTokens (550k) ceiling.
	// "word " encodes to ~1.2 tokens/word; 700 repeats ≈ 846 tokens/doc,
	// 800 docs × 846 ≈ 677k tokens > 550k budget → forces ≥2 batches.
	const n = 800
	docs := make([]string, n)
	body := strings.Repeat("word ", 700) // ~700 words → ~846 tokens/doc
	for i := range docs {
		docs[i] = fmt.Sprintf("doc-%d %s", i, body)
	}
	fb := &fakeBackend{}
	r := newTestReranker(fb)

	results, err := r.Rerank(context.Background(), "q", docs, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.calls < 2 {
		t.Fatalf("expected ≥2 backend calls due to budget split, got %d", fb.calls)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results (topK=10), got %d", len(results))
	}
	if results[0].Index != 0 {
		t.Fatalf("expected top result Index=0, got %d", results[0].Index)
	}
	// Indices must be globally unique and within range.
	seen := make(map[int]bool)
	for _, rr := range results {
		if rr.Index < 0 || rr.Index >= n {
			t.Fatalf("result Index %d out of range [0,%d)", rr.Index, n)
		}
		if seen[rr.Index] {
			t.Fatalf("duplicate global index %d in results", rr.Index)
		}
		seen[rr.Index] = true
	}
}

func TestRerank_IndexMappingAcrossBatches(t *testing.T) {
	const n = 600
	docs := make([]string, n)
	body := strings.Repeat("payload ", 800)
	for i := range docs {
		docs[i] = fmt.Sprintf("doc-%d %s", i, body)
	}
	fb := &fakeBackend{}
	r := newTestReranker(fb)

	results, err := r.Rerank(context.Background(), "q", docs, n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.calls < 2 {
		t.Fatalf("expected ≥2 calls to exercise mapping, got %d", fb.calls)
	}
	if len(results) != n {
		t.Fatalf("expected %d results, got %d", n, len(results))
	}
	seen := make(map[int]bool)
	for _, rr := range results {
		if rr.Index < 0 || rr.Index >= n {
			t.Fatalf("Index %d outside global range [0,%d)", rr.Index, n)
		}
		if seen[rr.Index] {
			t.Fatalf("duplicate Index %d", rr.Index)
		}
		seen[rr.Index] = true
	}
	if len(seen) != n {
		t.Fatalf("expected every input index represented, got %d unique", len(seen))
	}
}

func TestRerank_BackendErrorPropagates(t *testing.T) {
	const n = 600
	docs := make([]string, n)
	body := strings.Repeat("payload ", 800)
	for i := range docs {
		docs[i] = fmt.Sprintf("doc-%d %s", i, body)
	}
	fb := &fakeBackend{failCallN: 1}
	r := newTestReranker(fb)

	_, err := r.Rerank(context.Background(), "q", docs, 10)
	if err == nil {
		t.Fatal("expected error from failed backend call, got nil")
	}
}

package search

import (
	"context"
	"os"
	"testing"
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
	if r.client == nil {
		t.Fatal("client should not be nil")
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

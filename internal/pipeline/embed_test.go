package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/austinfhunter/voyageai"
)

// newTestEmbedder creates an Embedder pointing at the given test server URL.
func newTestEmbedder(baseURL string, batchSize int) *Embedder {
	client := voyageai.NewClient(&voyageai.VoyageClientOpts{
		Key:     "test-key",
		BaseURL: baseURL,
	})
	return &Embedder{
		client:    client,
		model:     "voyage-3-large",
		batchSize: batchSize,
	}
}

// fakeEmbedServer returns an httptest.Server that counts calls and returns
// dummy embeddings with the correct count.
func fakeEmbedServer(t *testing.T, callCount *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)

		var req voyageai.EmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		data := make([]voyageai.EmbeddingObject, len(req.Input))
		for i := range req.Input {
			data[i] = voyageai.EmbeddingObject{
				Object:    "embedding",
				Embedding: make([]float32, 1024),
				Index:     i,
			}
		}

		resp := voyageai.EmbeddingResponse{
			Object: "list",
			Data:   data,
			Model:  req.Model,
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func TestEmbedDocuments_Batching(t *testing.T) {
	var calls atomic.Int32
	srv := fakeEmbedServer(t, &calls)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 128)

	// 300 texts with batch size 128 -> ceil(300/128) = 3 API calls
	texts := make([]string, 300)
	for i := range texts {
		texts[i] = "text"
	}

	vecs, err := emb.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}

	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 API calls, got %d", got)
	}

	if len(vecs) != 300 {
		t.Errorf("expected 300 vectors, got %d", len(vecs))
	}

	for i, v := range vecs {
		if len(v) != 1024 {
			t.Errorf("vector %d: expected dim 1024, got %d", i, len(v))
		}
	}
}

func TestEmbedDocuments_Empty(t *testing.T) {
	emb := newTestEmbedder("http://unused", 128)
	vecs, err := emb.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedDocuments(nil): %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil, got %v", vecs)
	}
}

func TestEmbedDocuments_SingleBatch(t *testing.T) {
	var calls atomic.Int32
	srv := fakeEmbedServer(t, &calls)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 128)

	texts := make([]string, 50)
	for i := range texts {
		texts[i] = "text"
	}

	vecs, err := emb.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 API call, got %d", got)
	}

	if len(vecs) != 50 {
		t.Errorf("expected 50 vectors, got %d", len(vecs))
	}
}

func TestEmbedDocuments_CancelledContext(t *testing.T) {
	var calls atomic.Int32
	srv := fakeEmbedServer(t, &calls)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 128)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	texts := []string{"hello"}
	_, err := emb.EmbedDocuments(ctx, texts)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestEmbedQuery(t *testing.T) {
	var calls atomic.Int32
	srv := fakeEmbedServer(t, &calls)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 128)

	vec, err := emb.EmbedQuery(context.Background(), "search query")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 API call, got %d", got)
	}

	if len(vec) != 1024 {
		t.Errorf("expected dim 1024, got %d", len(vec))
	}
}

func TestEmbedQuery_CancelledContext(t *testing.T) {
	emb := newTestEmbedder("http://unused", 128)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := emb.EmbedQuery(ctx, "test")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestNewEmbedder_DefaultBatchSize(t *testing.T) {
	emb := NewEmbedder("key", "model", 0)
	if emb.batchSize != 128 {
		t.Errorf("expected default batch size 128, got %d", emb.batchSize)
	}
}

func TestEmbedDocuments_InputTypeDocument(t *testing.T) {
	var capturedInputType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req voyageai.EmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.InputType != nil {
			capturedInputType = *req.InputType
		}

		resp := voyageai.EmbeddingResponse{
			Object: "list",
			Data: []voyageai.EmbeddingObject{{
				Object:    "embedding",
				Embedding: make([]float32, 1024),
				Index:     0,
			}},
			Model: req.Model,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 128)
	_, err := emb.EmbedDocuments(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if capturedInputType != "document" {
		t.Errorf("expected input_type=document, got %q", capturedInputType)
	}
}

func TestEmbedQuery_InputTypeQuery(t *testing.T) {
	var capturedInputType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req voyageai.EmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.InputType != nil {
			capturedInputType = *req.InputType
		}

		resp := voyageai.EmbeddingResponse{
			Object: "list",
			Data: []voyageai.EmbeddingObject{{
				Object:    "embedding",
				Embedding: make([]float32, 1024),
				Index:     0,
			}},
			Model: req.Model,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 128)
	_, err := emb.EmbedQuery(context.Background(), "search")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if capturedInputType != "query" {
		t.Errorf("expected input_type=query, got %q", capturedInputType)
	}
}

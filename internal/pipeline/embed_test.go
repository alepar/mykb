package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeCtxEmbedServer(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/contextualizedembeddings" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req ctxEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Build response matching the nested structure.
		var groups []ctxEmbedGroup
		totalTokens := 0
		for gi, group := range req.Inputs {
			var items []ctxEmbedItem
			for ci := range group {
				items = append(items, ctxEmbedItem{
					Embedding: make([]float32, dim),
					Index:     ci,
				})
				totalTokens += 10 // fake token count
			}
			groups = append(groups, ctxEmbedGroup{
				Data:  items,
				Index: gi,
			})
		}

		resp := ctxEmbedResponse{Data: groups}
		resp.Usage.TotalTokens = totalTokens

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func newTestEmbedder(baseURL string, dim int) *Embedder {
	return &Embedder{
		apiKey:     "test-key",
		model:      "voyage-context-3",
		dimension:  dim,
		httpClient: http.DefaultClient,
		baseURL:    baseURL,
	}
}

func TestEmbedChunks(t *testing.T) {
	srv := fakeCtxEmbedServer(t, 2048)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 2048)
	chunks := []string{"chunk one", "chunk two", "chunk three"}

	vecs, err := emb.EmbedChunks(context.Background(), chunks)
	if err != nil {
		t.Fatalf("EmbedChunks: %v", err)
	}

	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 2048 {
			t.Errorf("vector %d: expected dim 2048, got %d", i, len(v))
		}
	}
}

func TestEmbedChunks_Empty(t *testing.T) {
	emb := newTestEmbedder("http://unused", 2048)
	vecs, err := emb.EmbedChunks(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedChunks(nil): %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil, got %v", vecs)
	}
}

func TestEmbedQuery(t *testing.T) {
	srv := fakeCtxEmbedServer(t, 2048)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 2048)
	vec, err := emb.EmbedQuery(context.Background(), "search query")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}

	if len(vec) != 2048 {
		t.Errorf("expected dim 2048, got %d", len(vec))
	}
}

func TestEmbedChunks_CancelledContext(t *testing.T) {
	srv := fakeCtxEmbedServer(t, 2048)
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 2048)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := emb.EmbedChunks(ctx, []string{"hello"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestEmbedQuery_CancelledContext(t *testing.T) {
	emb := newTestEmbedder("http://unused", 2048)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := emb.EmbedQuery(ctx, "test")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestEmbedChunks_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 2048)
	_, err := emb.EmbedChunks(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error from API error")
	}
}

func TestEmbedChunks_RequestFormat(t *testing.T) {
	var captured ctxEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctxEmbedResponse{
			Data: []ctxEmbedGroup{{
				Data: []ctxEmbedItem{
					{Embedding: make([]float32, 2048), Index: 0},
					{Embedding: make([]float32, 2048), Index: 1},
				},
				Index: 0,
			}},
		})
	}))
	defer srv.Close()

	emb := newTestEmbedder(srv.URL, 2048)
	emb.EmbedChunks(context.Background(), []string{"a", "b"})

	if captured.InputType != "document" {
		t.Errorf("expected input_type=document, got %q", captured.InputType)
	}
	if captured.OutputDtype != "int8" {
		t.Errorf("expected output_dtype=int8, got %q", captured.OutputDtype)
	}
	if captured.OutputDimension != 2048 {
		t.Errorf("expected output_dimension=2048, got %d", captured.OutputDimension)
	}
	if len(captured.Inputs) != 1 || len(captured.Inputs[0]) != 2 {
		t.Errorf("expected inputs=[[a,b]], got %v", captured.Inputs)
	}
}

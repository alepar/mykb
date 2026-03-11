package storage

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
)

func qdrantTestStore(t *testing.T) *QdrantStore {
	t.Helper()
	host := os.Getenv("TEST_QDRANT_HOST")
	if host == "" {
		t.Skip("TEST_QDRANT_HOST not set, skipping Qdrant integration test")
	}
	// Use a unique collection name per test to avoid collisions.
	collection := fmt.Sprintf("test_%s", t.Name())
	store, err := NewQdrantStore(host, collection)
	if err != nil {
		t.Fatalf("NewQdrantStore: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup: delete the test collection.
		_ = store.client.DeleteCollection(context.Background(), collection)
		_ = store.Close()
	})
	return store
}

func TestEnsureCollection(t *testing.T) {
	store := qdrantTestStore(t)
	ctx := context.Background()

	// First call should create the collection.
	if err := store.EnsureCollection(ctx, 1024); err != nil {
		t.Fatalf("EnsureCollection (create): %v", err)
	}

	// Verify it exists.
	exists, err := store.client.CollectionExists(ctx, store.collectionName)
	if err != nil {
		t.Fatalf("CollectionExists: %v", err)
	}
	if !exists {
		t.Fatal("collection should exist after EnsureCollection")
	}

	// Second call should be idempotent (no error).
	if err := store.EnsureCollection(ctx, 1024); err != nil {
		t.Fatalf("EnsureCollection (idempotent): %v", err)
	}
}

func TestUpsertAndSearch(t *testing.T) {
	store := qdrantTestStore(t)
	ctx := context.Background()

	const dim = 4 // small dimension for testing
	if err := store.EnsureCollection(ctx, dim); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	id1 := uuid.New().String()
	id2 := uuid.New().String()
	docID := "doc-abc"

	ids := []string{id1, id2}
	vectors := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
	}
	payloads := []map[string]any{
		{"document_id": docID, "chunk_index": 0},
		{"document_id": docID, "chunk_index": 1},
	}

	if err := store.UpsertVectors(ctx, ids, vectors, payloads); err != nil {
		t.Fatalf("UpsertVectors: %v", err)
	}

	// Search with the first vector; expect it to be the top result.
	results, err := store.Search(ctx, []float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != id1 {
		t.Errorf("expected top result ID %s, got %s", id1, results[0].ID)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("expected top result to have higher score: %f <= %f", results[0].Score, results[1].Score)
	}

	// Verify payload is returned.
	if results[0].Payload["document_id"] != docID {
		t.Errorf("expected payload document_id=%q, got %v", docID, results[0].Payload["document_id"])
	}
}

func TestDeleteByDocumentID(t *testing.T) {
	store := qdrantTestStore(t)
	ctx := context.Background()

	const dim = 4
	if err := store.EnsureCollection(ctx, dim); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	docA := "doc-a"
	docB := "doc-b"

	ids := []string{
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
	}
	vectors := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
	}
	payloads := []map[string]any{
		{"document_id": docA, "chunk_index": 0},
		{"document_id": docA, "chunk_index": 1},
		{"document_id": docB, "chunk_index": 0},
	}

	if err := store.UpsertVectors(ctx, ids, vectors, payloads); err != nil {
		t.Fatalf("UpsertVectors: %v", err)
	}

	// Verify all 3 points exist.
	results, err := store.Search(ctx, []float32{1, 1, 1, 0}, 10)
	if err != nil {
		t.Fatalf("Search before delete: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results before delete, got %d", len(results))
	}

	// Delete docA's points.
	if err := store.DeleteByDocumentID(ctx, docA); err != nil {
		t.Fatalf("DeleteByDocumentID: %v", err)
	}

	// Verify only 1 point remains (docB).
	results, err = store.Search(ctx, []float32{1, 1, 1, 0}, 10)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after delete, got %d", len(results))
	}
	if results[0].Payload["document_id"] != docB {
		t.Errorf("expected remaining point to be docB, got %v", results[0].Payload["document_id"])
	}
}

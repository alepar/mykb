package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"mykb/internal/storage"
)

func meiliTestStore(t *testing.T) *storage.MeilisearchStore {
	t.Helper()
	host := os.Getenv("TEST_MEILISEARCH_HOST")
	if host == "" {
		t.Skip("TEST_MEILISEARCH_HOST not set; skipping Meilisearch integration test")
	}
	apiKey := os.Getenv("TEST_MEILISEARCH_API_KEY")
	indexName := fmt.Sprintf("mykb_test_%d", time.Now().UnixNano())
	store, err := storage.NewMeilisearchStore(host, apiKey, indexName)
	if err != nil {
		t.Fatalf("NewMeilisearchStore: %v", err)
	}
	return store
}

func TestMeilisearch_EnsureIndex_Idempotent(t *testing.T) {
	store := meiliTestStore(t)
	ctx := context.Background()

	// First call creates the index.
	if err := store.EnsureIndex(ctx); err != nil {
		t.Fatalf("first EnsureIndex: %v", err)
	}

	// Second call should be idempotent.
	if err := store.EnsureIndex(ctx); err != nil {
		t.Fatalf("second EnsureIndex: %v", err)
	}
}

func TestMeilisearch_IndexChunks_Search(t *testing.T) {
	store := meiliTestStore(t)
	ctx := context.Background()

	if err := store.EnsureIndex(ctx); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}

	chunks := []storage.MeiliChunk{
		{
			ChunkID:    "doc1-0",
			DocumentID: "doc1",
			ChunkIndex: 0,
			Content:    "Meilisearch is a powerful full-text search engine",
		},
		{
			ChunkID:    "doc1-1",
			DocumentID: "doc1",
			ChunkIndex: 1,
			Content:    "It supports typo tolerance and ranking",
		},
		{
			ChunkID:    "doc2-0",
			DocumentID: "doc2",
			ChunkIndex: 0,
			Content:    "PostgreSQL is a relational database system",
		},
	}

	if err := store.IndexChunks(ctx, chunks); err != nil {
		t.Fatalf("IndexChunks: %v", err)
	}

	hits, err := store.Search(ctx, "search engine", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(hits) == 0 {
		t.Fatal("expected at least one search result")
	}

	// The top hit should be the one mentioning "search engine".
	top := hits[0]
	if top.ChunkID != "doc1-0" {
		t.Errorf("expected top hit chunk_id=doc1-0, got %s", top.ChunkID)
	}
	if top.DocumentID != "doc1" {
		t.Errorf("expected top hit document_id=doc1, got %s", top.DocumentID)
	}
	if top.ChunkIndex != 0 {
		t.Errorf("expected top hit chunk_index=0, got %d", top.ChunkIndex)
	}
}

func TestMeilisearch_Search_RankingScores(t *testing.T) {
	store := meiliTestStore(t)
	ctx := context.Background()

	if err := store.EnsureIndex(ctx); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}

	chunks := []storage.MeiliChunk{
		{
			ChunkID:    "rs-0",
			DocumentID: "rs",
			ChunkIndex: 0,
			Content:    "full-text search with ranking scores enabled",
		},
	}

	if err := store.IndexChunks(ctx, chunks); err != nil {
		t.Fatalf("IndexChunks: %v", err)
	}

	hits, err := store.Search(ctx, "ranking scores", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(hits) == 0 {
		t.Fatal("expected at least one result")
	}

	if hits[0].RankingScore == 0 {
		t.Error("expected non-zero ranking score")
	}
}

func TestMeilisearch_DeleteByDocumentID(t *testing.T) {
	store := meiliTestStore(t)
	ctx := context.Background()

	if err := store.EnsureIndex(ctx); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}

	chunks := []storage.MeiliChunk{
		{
			ChunkID:    "del-0",
			DocumentID: "del-doc",
			ChunkIndex: 0,
			Content:    "this document should be deleted",
		},
		{
			ChunkID:    "del-1",
			DocumentID: "del-doc",
			ChunkIndex: 1,
			Content:    "second chunk of the deletable document",
		},
		{
			ChunkID:    "keep-0",
			DocumentID: "keep-doc",
			ChunkIndex: 0,
			Content:    "this document should be kept and not deleted",
		},
	}

	if err := store.IndexChunks(ctx, chunks); err != nil {
		t.Fatalf("IndexChunks: %v", err)
	}

	if err := store.DeleteByDocumentID(ctx, "del-doc"); err != nil {
		t.Fatalf("DeleteByDocumentID: %v", err)
	}

	// Search for all content to verify deletion.
	hits, err := store.Search(ctx, "document", 10)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}

	for _, h := range hits {
		if h.DocumentID == "del-doc" {
			t.Errorf("found deleted document_id=del-doc in results: %+v", h)
		}
	}

	// Verify the kept document is still present.
	found := false
	for _, h := range hits {
		if h.DocumentID == "keep-doc" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected keep-doc to still be present after deletion")
	}
}

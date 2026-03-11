package pipeline

import (
	"context"
	"fmt"

	"mykb/internal/storage"
)

// Indexer writes chunks to both Qdrant (vector search) and Meilisearch (full-text search).
type Indexer struct {
	qdrant      *storage.QdrantStore
	meilisearch *storage.MeilisearchStore
}

// NewIndexer creates an Indexer that writes to both Qdrant and Meilisearch.
func NewIndexer(qdrant *storage.QdrantStore, meili *storage.MeilisearchStore) *Indexer {
	return &Indexer{
		qdrant:      qdrant,
		meilisearch: meili,
	}
}

// IndexableChunk holds all data needed to index a single chunk into both stores.
type IndexableChunk struct {
	ID                 string    // chunk UUID
	DocumentID         string    // document UUID
	ChunkIndex         int
	Vector             []float32 // 1024-dim embedding
	ContextualizedText string    // chunk_text + "\n\n" + context (for FTS)
}

// IndexChunks upserts the given chunks into both Qdrant and Meilisearch.
// Both operations are attempted; if either fails, the error is returned.
func (idx *Indexer) IndexChunks(ctx context.Context, chunks []IndexableChunk) error {
	if len(chunks) == 0 {
		return nil
	}

	// Build Qdrant upsert data.
	ids := make([]string, len(chunks))
	vectors := make([][]float32, len(chunks))
	payloads := make([]map[string]any, len(chunks))
	for i, c := range chunks {
		ids[i] = c.ID
		vectors[i] = c.Vector
		payloads[i] = map[string]any{
			"document_id": c.DocumentID,
			"chunk_index": c.ChunkIndex,
		}
	}

	// Build Meilisearch index data.
	meiliChunks := make([]storage.MeiliChunk, len(chunks))
	for i, c := range chunks {
		meiliChunks[i] = storage.MeiliChunk{
			ChunkID:    c.ID,
			DocumentID: c.DocumentID,
			ChunkIndex: c.ChunkIndex,
			Content:    c.ContextualizedText,
		}
	}

	// Upsert to Qdrant.
	if err := idx.qdrant.UpsertVectors(ctx, ids, vectors, payloads); err != nil {
		return fmt.Errorf("index qdrant: %w", err)
	}

	// Index to Meilisearch.
	if err := idx.meilisearch.IndexChunks(ctx, meiliChunks); err != nil {
		return fmt.Errorf("index meilisearch: %w", err)
	}

	return nil
}

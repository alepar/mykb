package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"mykb/internal/storage"
	"mykb/internal/wiki"
)

// ComputeContentHash returns the hex-encoded sha256 of the input.
// Used as the content_hash for wiki documents.
func ComputeContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// stripFrontmatterForChunking removes leading YAML frontmatter so it doesn't
// pollute embeddings. The original body (with frontmatter) is what gets
// stored on the document; only the embedded chunks see the stripped form.
func stripFrontmatterForChunking(body string) string {
	_, stripped := wiki.SplitFrontmatter(body)
	return strings.TrimLeft(stripped, "\n")
}

// WikiIngestResult summarizes the outcome of a wiki ingest call.
type WikiIngestResult struct {
	DocumentID string
	Chunks     int
	WasNoop    bool // true if content_hash matched and we skipped
}

// WikiIngestor runs the synchronous wiki ingest pipeline:
// strip frontmatter -> chunk -> embed -> index -> store.
type WikiIngestor struct {
	pg       *storage.PostgresStore
	embedder *Embedder
	indexer  *Indexer
}

// NewWikiIngestor wires the pieces. The caller owns lifecycle of the
// underlying stores and the embedder.
func NewWikiIngestor(pg *storage.PostgresStore, embedder *Embedder, indexer *Indexer) *WikiIngestor {
	return &WikiIngestor{pg: pg, embedder: embedder, indexer: indexer}
}

// Ingest runs the pipeline for a single wiki document. Idempotent: if a
// document with the given URL already exists with a matching content_hash,
// returns a no-op result without re-embedding.
//
// The url MUST be a wiki:// URL. The body is the full markdown, including
// frontmatter — the function strips it before chunking. The caller has
// already computed `contentHash` from the same body.
func (w *WikiIngestor) Ingest(ctx context.Context, url, title, body, contentHash string) (WikiIngestResult, error) {
	if !wiki.IsWikiURL(url) {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest: not a wiki URL: %q", url)
	}

	// Idempotency: if the existing doc has the same hash, skip everything.
	if existing, err := w.pg.GetDocumentByURL(ctx, url); err == nil && existing.ContentHash == contentHash && contentHash != "" {
		chunks := 0
		if existing.ChunkCount != nil {
			chunks = *existing.ChunkCount
		}
		return WikiIngestResult{DocumentID: existing.ID, Chunks: chunks, WasNoop: true}, nil
	}

	// Upsert document row (creates or updates by URL). Title and
	// content_hash are updated; chunks are replaced below.
	doc, err := w.pg.UpsertWikiDocument(ctx, url, title, contentHash)
	if err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest upsert: %w", err)
	}

	// Replace any pre-existing chunks for this document across all stores.
	// (Postgres FK cascades chunks; we still must clear Qdrant + Meilisearch.)
	if err := w.indexer.qdrant.DeleteByDocumentID(ctx, doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear qdrant: %w", err)
	}
	if err := w.indexer.meilisearch.DeleteByDocumentID(ctx, doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear meilisearch: %w", err)
	}
	if err := w.pg.DeleteChunksByDocument(ctx, doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear chunks: %w", err)
	}

	// Chunk (frontmatter stripped).
	chunkedText := ChunkMarkdown(stripFrontmatterForChunking(body), ChunkOptions{}.withDefaults())
	if len(chunkedText) == 0 {
		return WikiIngestResult{DocumentID: doc.ID, Chunks: 0}, nil
	}

	// Embed.
	vectors, err := w.embedder.EmbedChunks(ctx, chunkedText)
	if err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest embed: %w", err)
	}

	// Persist chunks (Postgres). InsertChunks(ctx, documentID, count) returns
	// []Chunk with IDs assigned, ordered by chunk_index 0..count-1.
	pgChunks, err := w.pg.InsertChunks(ctx, doc.ID, len(chunkedText))
	if err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest insert chunks: %w", err)
	}

	// Build index payloads using the IDs returned by InsertChunks.
	indexable := make([]IndexableChunk, len(chunkedText))
	for i, txt := range chunkedText {
		indexable[i] = IndexableChunk{
			ID:         pgChunks[i].ID,
			DocumentID: doc.ID,
			ChunkIndex: i,
			Vector:     vectors[i],
			Text:       txt,
		}
	}

	// Index into Qdrant and Meilisearch.
	if err := w.indexer.IndexChunks(ctx, indexable); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest index: %w", err)
	}

	// Update chunk_count on the document.
	if err := w.pg.SetDocumentChunkCount(ctx, doc.ID, len(chunkedText)); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest set chunk count: %w", err)
	}

	return WikiIngestResult{DocumentID: doc.ID, Chunks: len(chunkedText)}, nil
}

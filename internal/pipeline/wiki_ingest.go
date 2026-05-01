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
	fs       *storage.FilesystemStore
}

// NewWikiIngestor wires the pieces. The caller owns lifecycle of the
// underlying stores and the embedder.
func NewWikiIngestor(pg *storage.PostgresStore, embedder *Embedder, indexer *Indexer, fs *storage.FilesystemStore) *WikiIngestor {
	return &WikiIngestor{pg: pg, embedder: embedder, indexer: indexer, fs: fs}
}

// Ingest runs the pipeline for a single wiki document. Idempotent: if a
// document with the given URL already exists with a matching content_hash,
// returns a no-op result without re-embedding. Pass force=true to bypass
// the idempotency check and re-run the full pipeline unconditionally
// (used to repair documents whose downstream state — chunk text cache,
// vectors, FTS index — has drifted from the postgres row).
//
// The url MUST be a wiki:// URL. The body is the full markdown, including
// frontmatter — the function strips it before chunking. The caller has
// already computed `contentHash` from the same body.
func (w *WikiIngestor) Ingest(ctx context.Context, url, title, body, contentHash string, force bool) (WikiIngestResult, error) {
	if !wiki.IsWikiURL(url) {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest: not a wiki URL: %q", url)
	}

	// Idempotency: if the existing doc has the same hash, skip everything.
	// Bypassed when force=true.
	if !force {
		if existing, err := w.pg.GetDocumentByURL(ctx, url); err == nil && existing.ContentHash == contentHash && contentHash != "" {
			chunks := 0
			if existing.ChunkCount != nil {
				chunks = *existing.ChunkCount
			}
			return WikiIngestResult{DocumentID: existing.ID, Chunks: chunks, WasNoop: true}, nil
		}
	}

	// Upsert document row (creates or updates by URL). Title is updated;
	// content_hash is deliberately left empty here and only committed at the
	// very end of the pipeline. This keeps the idempotency check honest: if
	// any pipeline step fails, content_hash stays empty (or retains its old
	// value), so the next sync will re-run the full pipeline instead of
	// silently skipping a broken document.
	doc, err := w.pg.UpsertWikiDocument(ctx, url, title, "")
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
	// Clear any old chunk files on disk so leftover chunk_index entries from
	// a previous larger version don't linger after re-ingest.
	if err := w.fs.DeleteDocumentFiles(doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear chunk files: %w", err)
	}

	// Chunk (frontmatter stripped).
	chunkedText := ChunkMarkdown(stripFrontmatterForChunking(body), ChunkOptions{}.withDefaults())
	if len(chunkedText) == 0 {
		// Even with no chunks, record chunk_count = 0 so the document row is
		// consistent, and commit the content_hash so the next sync noops.
		if err := w.pg.SetDocumentChunkCount(ctx, doc.ID, 0); err != nil {
			return WikiIngestResult{}, fmt.Errorf("wiki ingest set chunk count (empty): %w", err)
		}
		if err := w.pg.SetContentHash(ctx, doc.ID, contentHash); err != nil {
			return WikiIngestResult{}, fmt.Errorf("wiki ingest set hash (empty): %w", err)
		}
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

	// Persist chunk text to the filesystem cache. The search path's reranker
	// reads chunk text by (document_id, chunk_index) from this location, so
	// wiki documents must populate it just like raw-source documents do.
	for i, txt := range chunkedText {
		if err := w.fs.WriteChunkText(doc.ID, i, []byte(txt)); err != nil {
			return WikiIngestResult{}, fmt.Errorf("wiki ingest write chunk file %d: %w", i, err)
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

	// Commit the content_hash only now, after the full pipeline has succeeded.
	// Any failure above leaves content_hash empty/stale so the next sync
	// retries rather than silently treating the document as up-to-date.
	if err := w.pg.SetContentHash(ctx, doc.ID, contentHash); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest set hash: %w", err)
	}

	return WikiIngestResult{DocumentID: doc.ID, Chunks: len(chunkedText)}, nil
}

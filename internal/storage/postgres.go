package storage

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Document represents a row in the documents table.
type Document struct {
	ID          string
	URL         string
	Status      string
	Error       *string
	Title       *string
	ChunkCount  *int
	RetryCount  int
	NextRetryAt *time.Time
	CrawledAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Chunk represents a row in the chunks table.
type Chunk struct {
	ID         string
	DocumentID string
	ChunkIndex int
	Status     string
	CreatedAt  time.Time
}

// PostgresStore wraps a pgxpool.Pool for document and chunk persistence.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects to PostgreSQL using the given DSN and returns a
// PostgresStore backed by a connection pool.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// RunMigrations reads the embedded migration SQL files and executes them if the
// documents table does not yet exist.
func (s *PostgresStore) RunMigrations(ctx context.Context) error {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'documents')`,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check tables: %w", err)
	}
	if exists {
		return nil
	}

	sql, err := migrationFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	return nil
}

// Close closes the underlying connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// InsertDocument inserts a new document with the given URL and returns it.
func (s *PostgresStore) InsertDocument(ctx context.Context, url string) (Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx,
		`INSERT INTO documents (url) VALUES ($1)
		 RETURNING id, url, status, error, title, chunk_count, retry_count,
		           next_retry_at, crawled_at, created_at, updated_at`,
		url,
	).Scan(&d.ID, &d.URL, &d.Status, &d.Error, &d.Title, &d.ChunkCount,
		&d.RetryCount, &d.NextRetryAt, &d.CrawledAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return Document{}, fmt.Errorf("insert document: %w", err)
	}
	return d, nil
}

// UpdateDocumentStatus sets the status and updated_at for a document.
func (s *PostgresStore) UpdateDocumentStatus(ctx context.Context, id, status string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("update document status: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// SetDocumentError sets the error message, increments retry_count, and
// computes next_retry_at using exponential backoff (2^retry_count * 30s).
func (s *PostgresStore) SetDocumentError(ctx context.Context, id, errMsg string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents
		 SET error = $2,
		     retry_count = retry_count + 1,
		     next_retry_at = now() + make_interval(secs => power(2, retry_count) * 30),
		     updated_at = now()
		 WHERE id = $1`, id, errMsg)
	if err != nil {
		return fmt.Errorf("set document error: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// ClearDocumentError sets the error field to NULL.
func (s *PostgresStore) ClearDocumentError(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET error = NULL, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("clear document error: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// SetDocumentTitle sets the title for a document.
func (s *PostgresStore) SetDocumentTitle(ctx context.Context, id, title string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET title = $2, updated_at = now() WHERE id = $1`, id, title)
	if err != nil {
		return fmt.Errorf("set document title: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// SetDocumentChunkCount sets the chunk_count for a document.
func (s *PostgresStore) SetDocumentChunkCount(ctx context.Context, id string, count int) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET chunk_count = $2, updated_at = now() WHERE id = $1`, id, count)
	if err != nil {
		return fmt.Errorf("set document chunk count: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// SetDocumentCrawledAt sets crawled_at to now() for a document.
func (s *PostgresStore) SetDocumentCrawledAt(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET crawled_at = now(), updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("set document crawled_at: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// GetDocument retrieves a single document by ID.
func (s *PostgresStore) GetDocument(ctx context.Context, id string) (Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx,
		`SELECT id, url, status, error, title, chunk_count, retry_count,
		        next_retry_at, crawled_at, created_at, updated_at
		 FROM documents WHERE id = $1`, id,
	).Scan(&d.ID, &d.URL, &d.Status, &d.Error, &d.Title, &d.ChunkCount,
		&d.RetryCount, &d.NextRetryAt, &d.CrawledAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return d, nil
}

// GetDocumentByURL retrieves a document by its URL.
func (s *PostgresStore) GetDocumentByURL(ctx context.Context, url string) (Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx,
		`SELECT id, url, status, error, title, chunk_count, retry_count,
		        next_retry_at, crawled_at, created_at, updated_at
		 FROM documents WHERE url = $1`, url,
	).Scan(&d.ID, &d.URL, &d.Status, &d.Error, &d.Title, &d.ChunkCount,
		&d.RetryCount, &d.NextRetryAt, &d.CrawledAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return Document{}, fmt.Errorf("get document by url: %w", err)
	}
	return d, nil
}

// GetDocumentsByIDs retrieves multiple documents by their IDs.
func (s *PostgresStore) GetDocumentsByIDs(ctx context.Context, ids []string) ([]Document, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, url, status, error, title, chunk_count, retry_count,
		        next_retry_at, crawled_at, created_at, updated_at
		 FROM documents WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("get documents by ids: %w", err)
	}
	defer rows.Close()
	return scanDocuments(rows)
}

// ListDocuments returns a page of documents ordered by created_at descending,
// along with the total count.
func (s *PostgresStore) ListDocuments(ctx context.Context, limit, offset int) ([]Document, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM documents`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count documents: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, url, status, error, title, chunk_count, retry_count,
		        next_retry_at, crawled_at, created_at, updated_at
		 FROM documents ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	docs, err := scanDocuments(rows)
	if err != nil {
		return nil, 0, err
	}
	return docs, total, nil
}

// DeleteDocument deletes a document by ID. Chunks are cascade-deleted via FK.
func (s *PostgresStore) DeleteDocument(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// GetPendingDocuments returns documents that are eligible for processing:
// not DONE and either no error or retry-eligible.
func (s *PostgresStore) GetPendingDocuments(ctx context.Context, maxRetries int) ([]Document, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, url, status, error, title, chunk_count, retry_count,
		        next_retry_at, crawled_at, created_at, updated_at
		 FROM documents
		 WHERE status != 'DONE'
		   AND (error IS NULL OR (retry_count < $1 AND next_retry_at <= now()))
		 ORDER BY created_at`, maxRetries)
	if err != nil {
		return nil, fmt.Errorf("get pending documents: %w", err)
	}
	defer rows.Close()
	return scanDocuments(rows)
}

// InsertChunks batch-inserts count chunks for the given document with
// chunk_index values 0..count-1.
func (s *PostgresStore) InsertChunks(ctx context.Context, documentID string, count int) ([]Chunk, error) {
	if count <= 0 {
		return nil, nil
	}

	// Build batch insert using CopyFrom for efficiency is overkill here;
	// use a single multi-row INSERT instead.
	query := `INSERT INTO chunks (document_id, chunk_index) VALUES `
	args := make([]any, 0, count*2)
	for i := 0; i < count; i++ {
		if i > 0 {
			query += ", "
		}
		query += fmt.Sprintf("($%d, $%d)", i*2+1, i*2+2)
		args = append(args, documentID, i)
	}
	query += ` RETURNING id, document_id, chunk_index, status, created_at`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("insert chunks: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.ChunkIndex, &c.Status, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return chunks, nil
}

// GetChunksByDocument returns all chunks for a document ordered by chunk_index.
func (s *PostgresStore) GetChunksByDocument(ctx context.Context, documentID string) ([]Chunk, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, chunk_index, status, created_at
		 FROM chunks WHERE document_id = $1 ORDER BY chunk_index`, documentID)
	if err != nil {
		return nil, fmt.Errorf("get chunks by document: %w", err)
	}
	defer rows.Close()
	return scanChunks(rows)
}

// UpdateChunkStatus sets the status for a chunk.
func (s *PostgresStore) UpdateChunkStatus(ctx context.Context, id, status string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE chunks SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("update chunk status: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("chunk %s not found", id)
	}
	return nil
}

// GetChunksByDocumentAndStatus returns chunks for a document filtered by status.
func (s *PostgresStore) GetChunksByDocumentAndStatus(ctx context.Context, documentID, status string) ([]Chunk, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, chunk_index, status, created_at
		 FROM chunks WHERE document_id = $1 AND status = $2 ORDER BY chunk_index`,
		documentID, status)
	if err != nil {
		return nil, fmt.Errorf("get chunks by document and status: %w", err)
	}
	defer rows.Close()
	return scanChunks(rows)
}

// DeleteChunksByDocument deletes all chunks belonging to a document.
func (s *PostgresStore) DeleteChunksByDocument(ctx context.Context, documentID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM chunks WHERE document_id = $1`, documentID)
	if err != nil {
		return fmt.Errorf("delete chunks by document: %w", err)
	}
	return nil
}

// scanDocuments collects Document rows from the given pgx.Rows.
func scanDocuments(rows pgx.Rows) ([]Document, error) {
	var docs []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.URL, &d.Status, &d.Error, &d.Title, &d.ChunkCount,
			&d.RetryCount, &d.NextRetryAt, &d.CrawledAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return docs, nil
}

// scanChunks collects Chunk rows from the given pgx.Rows.
func scanChunks(rows pgx.Rows) ([]Chunk, error) {
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.ChunkIndex, &c.Status, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return chunks, nil
}

package storage

import (
	"context"
	"embed"
	"fmt"
	"log"
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
	Step        string     // Pipeline stage: CRAWLING, CHUNKING, EMBEDDING, INDEXING, DONE
	State       string     // Lifecycle: QUEUED, PROCESSING, COMPLETED, FAILED, ABANDONED
	FailedStep  *string    // Which step caused the error
	IsRetriable bool       // Whether the error is worth retrying
	Error       *string
	Title       *string
	ChunkCount  *int
	RetryCount  int
	NextRetryAt *time.Time
	LockedAt    *time.Time // When a worker claimed this document
	LockedBy    *string    // Worker identifier (hostname-PID)
	CrawledAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ContentHash string // sha256 of body for wiki docs; empty for raw sources
}

const documentColumns = `id, url, step, state, failed_step, is_retriable,
	error, title, chunk_count, retry_count, next_retry_at,
	locked_at, locked_by, crawled_at, created_at, updated_at, COALESCE(content_hash, '')`

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.URL, &d.Step, &d.State, &d.FailedStep,
		&d.IsRetriable, &d.Error, &d.Title, &d.ChunkCount,
		&d.RetryCount, &d.NextRetryAt, &d.LockedAt, &d.LockedBy,
		&d.CrawledAt, &d.CreatedAt, &d.UpdatedAt, &d.ContentHash)
	return d, err
}

func (d Document) DisplayStatus() string {
	switch d.State {
	case "QUEUED":
		return "PENDING"
	case "COMPLETED":
		return "DONE"
	case "FAILED":
		if !d.IsRetriable {
			return "ERROR"
		}
		return d.Step
	default: // PROCESSING, ABANDONED
		return d.Step
	}
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

// RunMigrations applies embedded SQL migrations in sorted order, tracking
// applied migrations in a schema_migrations table.
func (s *PostgresStore) RunMigrations(ctx context.Context) error {
	// Create tracking table if it doesn't exist.
	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// If documents table already exists but 001_init.sql isn't recorded,
	// seed it. This handles databases created before schema_migrations.
	var docExists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'documents')`,
	).Scan(&docExists); err != nil {
		return fmt.Errorf("check documents table: %w", err)
	}
	if docExists {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ('001_init.sql') ON CONFLICT DO NOTHING`,
		); err != nil {
			return fmt.Errorf("seed 001_init.sql: %w", err)
		}
	}

	// Read all migration files (embed.FS returns them sorted by name).
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		// Skip if already applied.
		var applied bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = $1)`,
			name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		// Read and apply.
		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}

		log.Printf("storage: applied migration %s", name)
	}

	return nil
}

// Close closes the underlying connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// InsertDocument inserts a new document with the given URL and returns it.
func (s *PostgresStore) InsertDocument(ctx context.Context, url string) (Document, error) {
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`INSERT INTO documents (url) VALUES ($1) RETURNING `+documentColumns, url))
	if err != nil {
		return Document{}, fmt.Errorf("insert document: %w", err)
	}
	return d, nil
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
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE id = $1`, id))
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return d, nil
}

// GetDocumentByURL retrieves a document by its URL.
func (s *PostgresStore) GetDocumentByURL(ctx context.Context, url string) (Document, error) {
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE url = $1`, url))
	if err != nil {
		return Document{}, fmt.Errorf("get document by url: %w", err)
	}
	return d, nil
}

// GetDocumentsByIDs retrieves multiple documents by their IDs.
func (s *PostgresStore) GetDocumentsByIDs(ctx context.Context, ids []string) ([]Document, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE id = ANY($1)`, ids)
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
		`SELECT `+documentColumns+`
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

// StatusCounts returns the number of documents in each status and the total chunk count.
func (s *PostgresStore) StatusCounts(ctx context.Context) (map[string]int, int, error) {
	counts := make(map[string]int)
	rows, err := s.pool.Query(ctx,
		`SELECT
		   CASE
		     WHEN state = 'QUEUED' THEN 'PENDING'
		     WHEN state = 'COMPLETED' THEN 'DONE'
		     WHEN state = 'FAILED' AND is_retriable = false THEN 'ERROR'
		     WHEN state = 'FAILED' THEN step
		     WHEN state = 'ABANDONED' THEN step
		     ELSE step
		   END AS display_status,
		   COUNT(*)
		 FROM documents GROUP BY display_status`)
	if err != nil {
		return nil, 0, fmt.Errorf("status counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, 0, fmt.Errorf("scan status count: %w", err)
		}
		counts[status] = count
	}
	var chunkCount int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&chunkCount); err != nil {
		return nil, 0, fmt.Errorf("count chunks: %w", err)
	}
	return counts, chunkCount, nil
}

// ClaimDocument atomically transitions a document from QUEUED/FAILED/ABANDONED
// to PROCESSING and assigns a worker lock. Returns (false, zero, nil) if the
// document is not in a claimable state.
func (s *PostgresStore) ClaimDocument(ctx context.Context, id, workerID string) (bool, Document, error) {
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`UPDATE documents
		 SET state = 'PROCESSING', locked_at = now(), locked_by = $2,
		     error = NULL, failed_step = NULL, updated_at = now()
		 WHERE id = $1 AND state IN ('QUEUED', 'FAILED', 'ABANDONED')
		 RETURNING `+documentColumns,
		id, workerID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, Document{}, nil
		}
		return false, Document{}, fmt.Errorf("claim document: %w", err)
	}
	return true, d, nil
}

// AdvanceStep moves a document to the next pipeline step without changing state.
func (s *PostgresStore) AdvanceStep(ctx context.Context, id, newStep string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET step = $2, updated_at = now() WHERE id = $1`,
		id, newStep)
	if err != nil {
		return fmt.Errorf("advance step: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// CompleteDocument marks a document as successfully processed.
func (s *PostgresStore) CompleteDocument(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents
		 SET step = 'DONE', state = 'COMPLETED',
		     locked_at = NULL, locked_by = NULL,
		     error = NULL, failed_step = NULL, updated_at = now()
		 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("complete document: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// FailDocument records a failure, increments retry_count, and computes
// next_retry_at using exponential backoff. Marks as non-retriable if the
// error is permanent or max retries are exhausted.
func (s *PostgresStore) FailDocument(ctx context.Context, id, failedStep, errMsg string, retriable bool, maxRetries int) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents
		 SET state = 'FAILED', failed_step = $2, error = $3,
		     retry_count = retry_count + 1,
		     next_retry_at = now() + make_interval(secs => power(2, retry_count) * 30),
		     is_retriable = CASE WHEN NOT $4 THEN false WHEN retry_count + 1 >= $5 THEN false ELSE true END,
		     locked_at = NULL, locked_by = NULL, updated_at = now()
		 WHERE id = $1`,
		id, failedStep, errMsg, retriable, maxRetries)
	if err != nil {
		return fmt.Errorf("fail document: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found", id)
	}
	return nil
}

// HeartbeatDocument refreshes locked_at for a PROCESSING document, signaling
// that the worker is still actively working on it.
func (s *PostgresStore) HeartbeatDocument(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents SET locked_at = now(), updated_at = now()
		 WHERE id = $1 AND state = 'PROCESSING'`, id)
	if err != nil {
		return fmt.Errorf("heartbeat document: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("document %s not found or not processing", id)
	}
	return nil
}

// AbandonStaleDocuments marks PROCESSING documents as ABANDONED if they have
// been locked longer than the given timeout.
func (s *PostgresStore) AbandonStaleDocuments(ctx context.Context, timeout time.Duration) (int, error) {
	ct, err := s.pool.Exec(ctx,
		`UPDATE documents
		 SET state = 'ABANDONED', locked_at = NULL, locked_by = NULL, updated_at = now()
		 WHERE state = 'PROCESSING' AND locked_at < $1`,
		time.Now().Add(-timeout))
	if err != nil {
		return 0, fmt.Errorf("abandon stale documents: %w", err)
	}
	return int(ct.RowsAffected()), nil
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
// QUEUED, ABANDONED, or retriable FAILED with elapsed backoff.
func (s *PostgresStore) GetPendingDocuments(ctx context.Context, maxRetries int) ([]Document, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+documentColumns+`
		 FROM documents
		 WHERE state IN ('QUEUED', 'ABANDONED')
		    OR (state = 'FAILED' AND is_retriable = true
		        AND retry_count < $1 AND next_retry_at <= now())
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
		if err := rows.Scan(&d.ID, &d.URL, &d.Step, &d.State, &d.FailedStep,
			&d.IsRetriable, &d.Error, &d.Title, &d.ChunkCount,
			&d.RetryCount, &d.NextRetryAt, &d.LockedAt, &d.LockedBy,
			&d.CrawledAt, &d.CreatedAt, &d.UpdatedAt, &d.ContentHash); err != nil {
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

// UpsertWikiDocument inserts or updates a wiki document by URL. Used by the
// wiki ingest path. The unique constraint on documents.url ensures one row
// per URL; on conflict, title and content_hash are updated.
func (s *PostgresStore) UpsertWikiDocument(ctx context.Context, url, title, contentHash string) (Document, error) {
	const q = `
		INSERT INTO documents (url, title, content_hash, step, state)
		VALUES ($1, $2, $3, 'DONE', 'COMPLETED')
		ON CONFLICT (url) DO UPDATE SET
			title = EXCLUDED.title,
			content_hash = EXCLUDED.content_hash,
			updated_at = now()
		RETURNING ` + documentColumns
	d, err := scanDocument(s.pool.QueryRow(ctx, q, url, title, contentHash))
	if err != nil {
		return Document{}, fmt.Errorf("upsert wiki document: %w", err)
	}
	return d, nil
}

// ListWikiDocuments returns wiki documents for a given wiki name, identified
// by their "wiki://<wikiName>/" URL prefix. Used by `mykb wiki sync` to
// compute the three-way diff.
func (s *PostgresStore) ListWikiDocuments(ctx context.Context, wikiName string) ([]Document, error) {
	prefix := "wiki://" + wikiName + "/"
	rows, err := s.pool.Query(ctx,
		`SELECT id, url, COALESCE(title, ''), COALESCE(content_hash, '')
		 FROM documents
		 WHERE url LIKE $1
		 ORDER BY url`,
		prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("list wiki documents: %w", err)
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		var title string
		if err := rows.Scan(&d.ID, &d.URL, &title, &d.ContentHash); err != nil {
			return nil, fmt.Errorf("scan wiki document: %w", err)
		}
		d.Title = &title
		out = append(out, d)
	}
	return out, rows.Err()
}

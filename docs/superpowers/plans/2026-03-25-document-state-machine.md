# Document State Machine Redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the document `status` column into `step` (pipeline progress) and `state` (processing lifecycle) with explicit locking, failure tracking, and retriability.

**Architecture:** Replace the single `status` column with `step` + `state` columns, add locking (`locked_at`/`locked_by`), failure tracking (`failed_step`/`is_retriable`), and abandon detection. Backward-compatible API — proto `Document.status` field maps from step+state to the same display values clients expect.

**Tech Stack:** Go, PostgreSQL (pgx v5), ConnectRPC, testcontainers-go

**Spec:** `docs/superpowers/specs/2026-03-25-document-state-machine-design.md`

---

### Task 1: Migration Runner Upgrade

**Files:**
- Modify: `internal/storage/postgres.go` (RunMigrations function)
- Test: `internal/storage/postgres_test.go`

- [ ] **Step 1: Write the migration runner test**

Add to `internal/storage/postgres_test.go`:

```go
func TestRunMigrations_Idempotent(t *testing.T) {
	store := newTestStore(t) // calls RunMigrations internally
	ctx := context.Background()

	// schema_migrations table should exist with recorded entries.
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count == 0 {
		t.Fatal("expected migrations to be recorded")
	}

	// Running again should be idempotent.
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("RunMigrations (idempotent): %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/ -run TestRunMigrations_Idempotent -v`
Expected: FAIL — `schema_migrations` table doesn't exist.

- [ ] **Step 3: Rewrite RunMigrations**

Replace the `RunMigrations` function in `internal/storage/postgres.go`. Add `"log"` to imports.

```go
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
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		// Record.
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		log.Printf("storage: applied migration %s", name)
	}

	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/ -run TestRunMigrations_Idempotent -v`
Expected: PASS

- [ ] **Step 5: Run all storage tests to verify no regression**

Run: `go test ./internal/storage/ -v`
Expected: All existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/storage/postgres.go internal/storage/postgres_test.go
git commit -m "feat: upgrade migration runner to schema_migrations tracking"
```

---

### Task 2: Storage Layer — Document State Machine

This task rewrites the storage layer: migration SQL, Document struct, all query methods, new state machine methods, and tests. After this task, `go test ./internal/storage/` passes but `go build ./...` will fail (worker/server still reference removed methods — fixed in tasks 3–4).

**Files:**
- Create: `internal/storage/migrations/002_document_states.sql`
- Modify: `internal/storage/postgres.go`
- Modify: `internal/storage/postgres_test.go`

#### Sub-group A: Migration + Struct

- [ ] **Step 1: Create migration SQL**

Create `internal/storage/migrations/002_document_states.sql`:

```sql
ALTER TABLE documents ADD COLUMN step TEXT;
ALTER TABLE documents ADD COLUMN state TEXT;
ALTER TABLE documents ADD COLUMN failed_step TEXT;
ALTER TABLE documents ADD COLUMN is_retriable BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE documents ADD COLUMN locked_at TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN locked_by TEXT;

-- Migrate existing data.
UPDATE documents SET step = 'DONE', state = 'COMPLETED' WHERE status = 'DONE';
UPDATE documents SET step = status, state = 'FAILED', failed_step = status, is_retriable = false WHERE status = 'ERROR';
UPDATE documents SET step = CASE WHEN status = 'PENDING' THEN 'CRAWLING' ELSE status END, state = 'QUEUED' WHERE status NOT IN ('DONE', 'ERROR');

ALTER TABLE documents ALTER COLUMN step SET NOT NULL;
ALTER TABLE documents ALTER COLUMN state SET NOT NULL;

ALTER TABLE documents DROP COLUMN status;

-- Replace old index with new ones.
DROP INDEX IF EXISTS idx_documents_status;
CREATE INDEX idx_documents_state ON documents(state);
CREATE INDEX idx_documents_retry ON documents(next_retry_at) WHERE state = 'FAILED' AND is_retriable = true;
CREATE INDEX idx_documents_abandoned ON documents(locked_at) WHERE state = 'PROCESSING';
```

- [ ] **Step 2: Update Document struct**

Replace the `Document` struct in `internal/storage/postgres.go`:

```go
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
}
```

- [ ] **Step 3: Add documentColumns constant, scanDocument helper, and DisplayStatus method**

Add after the Document struct:

```go
// documentColumns is the SELECT column list for documents, kept in sync with
// scanDocument and scanDocuments.
const documentColumns = `id, url, step, state, failed_step, is_retriable,
	error, title, chunk_count, retry_count, next_retry_at,
	locked_at, locked_by, crawled_at, created_at, updated_at`

// scanDocument scans a single document row.
func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.URL, &d.Step, &d.State, &d.FailedStep,
		&d.IsRetriable, &d.Error, &d.Title, &d.ChunkCount,
		&d.RetryCount, &d.NextRetryAt, &d.LockedAt, &d.LockedBy,
		&d.CrawledAt, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// DisplayStatus maps step+state to the display status string used by the
// proto API and HTTP endpoints. Preserves backward compatibility with
// clients that expect PENDING, DONE, ERROR, CRAWLING, EMBEDDING, etc.
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
```

- [ ] **Step 4: Update scanDocuments**

Replace the `scanDocuments` function:

```go
func scanDocuments(rows pgx.Rows) ([]Document, error) {
	var docs []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.URL, &d.Step, &d.State, &d.FailedStep,
			&d.IsRetriable, &d.Error, &d.Title, &d.ChunkCount,
			&d.RetryCount, &d.NextRetryAt, &d.LockedAt, &d.LockedBy,
			&d.CrawledAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return docs, nil
}
```

#### Sub-group B: Update Existing Query Methods

- [ ] **Step 5: Update InsertDocument**

```go
func (s *PostgresStore) InsertDocument(ctx context.Context, url string) (Document, error) {
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`INSERT INTO documents (url) VALUES ($1) RETURNING `+documentColumns, url))
	if err != nil {
		return Document{}, fmt.Errorf("insert document: %w", err)
	}
	return d, nil
}
```

- [ ] **Step 6: Update GetDocument**

```go
func (s *PostgresStore) GetDocument(ctx context.Context, id string) (Document, error) {
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE id = $1`, id))
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return d, nil
}
```

- [ ] **Step 7: Update GetDocumentByURL**

```go
func (s *PostgresStore) GetDocumentByURL(ctx context.Context, url string) (Document, error) {
	d, err := scanDocument(s.pool.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE url = $1`, url))
	if err != nil {
		return Document{}, fmt.Errorf("get document by url: %w", err)
	}
	return d, nil
}
```

- [ ] **Step 8: Update GetDocumentsByIDs**

```go
func (s *PostgresStore) GetDocumentsByIDs(ctx context.Context, ids []string) ([]Document, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("get documents by ids: %w", err)
	}
	defer rows.Close()
	return scanDocuments(rows)
}
```

- [ ] **Step 9: Update ListDocuments**

```go
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
```

#### Sub-group C: New State Machine Methods

- [ ] **Step 10: Add ClaimDocument**

```go
// ClaimDocument atomically claims a document for processing. Returns
// (true, doc) on success, (false, zero) if the document is not claimable
// (already PROCESSING or COMPLETED). Clears stale error/failed_step on claim.
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
```

- [ ] **Step 11: Add AdvanceStep**

```go
// AdvanceStep moves a document to the next pipeline stage. State stays
// PROCESSING, lock stays held.
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
```

- [ ] **Step 12: Add CompleteDocument**

```go
// CompleteDocument marks a document as successfully processed: step=DONE,
// state=COMPLETED, lock released, error cleared.
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
```

- [ ] **Step 13: Add FailDocument**

```go
// FailDocument records a processing failure: sets state=FAILED, records the
// failed step and error, increments retry_count with exponential backoff,
// and releases the lock. If retriable is false or max retries exhausted,
// is_retriable is set to false.
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
```

- [ ] **Step 14: Add AbandonStaleDocuments**

```go
// AbandonStaleDocuments marks documents as ABANDONED if they've been
// PROCESSING longer than the given timeout. Returns the number of
// documents abandoned.
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
```

#### Sub-group D: Update Querying Methods

- [ ] **Step 15: Update GetPendingDocuments**

```go
// GetPendingDocuments returns documents eligible for processing: QUEUED,
// ABANDONED, or FAILED with is_retriable=true and retry time elapsed.
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
```

- [ ] **Step 16: Update StatusCounts**

```go
// StatusCounts returns document counts grouped by display status and the
// total chunk count.
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
```

#### Sub-group E: Remove Old Methods

- [ ] **Step 17: Delete UpdateDocumentStatus, SetDocumentError, ClearDocumentError**

Remove these three methods from `internal/storage/postgres.go`:
- `UpdateDocumentStatus` (the whole function)
- `SetDocumentError` (the whole function)
- `ClearDocumentError` (the whole function)

#### Sub-group F: Rewrite Tests

- [ ] **Step 18: Write DisplayStatus test**

This is a pure unit test — no database needed. Add to `postgres_test.go`:

```go
func TestDisplayStatus(t *testing.T) {
	tests := []struct {
		step, state string
		retriable   bool
		want        string
	}{
		{"CRAWLING", "QUEUED", true, "PENDING"},
		{"CRAWLING", "PROCESSING", true, "CRAWLING"},
		{"EMBEDDING", "PROCESSING", true, "EMBEDDING"},
		{"DONE", "COMPLETED", true, "DONE"},
		{"CRAWLING", "FAILED", true, "CRAWLING"},
		{"CRAWLING", "FAILED", false, "ERROR"},
		{"EMBEDDING", "FAILED", false, "ERROR"},
		{"CRAWLING", "ABANDONED", true, "CRAWLING"},
	}
	for _, tt := range tests {
		d := Document{Step: tt.step, State: tt.state, IsRetriable: tt.retriable}
		if got := d.DisplayStatus(); got != tt.want {
			t.Errorf("DisplayStatus(%q, %q, retriable=%v) = %q, want %q",
				tt.step, tt.state, tt.retriable, got, tt.want)
		}
	}
}
```

- [ ] **Step 19: Rewrite TestInsertAndGetDocument**

```go
func TestInsertAndGetDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, err := store.InsertDocument(ctx, "https://example.com/page1")
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if doc.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if doc.URL != "https://example.com/page1" {
		t.Fatalf("URL = %q, want %q", doc.URL, "https://example.com/page1")
	}
	if doc.Step != "CRAWLING" {
		t.Fatalf("Step = %q, want CRAWLING", doc.Step)
	}
	if doc.State != "QUEUED" {
		t.Fatalf("State = %q, want QUEUED", doc.State)
	}
	if !doc.IsRetriable {
		t.Fatal("expected IsRetriable = true")
	}

	got, err := store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.ID != doc.ID || got.URL != doc.URL {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
}
```

- [ ] **Step 20: Remove old tests that reference removed methods**

Delete these test functions from `postgres_test.go`:
- `TestUpdateDocumentStatus`
- `TestSetDocumentError`
- `TestClearDocumentError`

- [ ] **Step 21: Write ClaimDocument tests**

```go
func TestClaimDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/claim")

	// Claim should succeed for QUEUED doc.
	claimed, got, err := store.ClaimDocument(ctx, doc.ID, "worker-1")
	if err != nil {
		t.Fatalf("ClaimDocument: %v", err)
	}
	if !claimed {
		t.Fatal("expected claim to succeed")
	}
	if got.State != "PROCESSING" {
		t.Fatalf("State = %q, want PROCESSING", got.State)
	}
	if got.LockedBy == nil || *got.LockedBy != "worker-1" {
		t.Fatalf("LockedBy = %v, want worker-1", got.LockedBy)
	}
	if got.LockedAt == nil {
		t.Fatal("LockedAt should be set")
	}

	// Second claim should fail (already PROCESSING).
	claimed2, _, err := store.ClaimDocument(ctx, doc.ID, "worker-2")
	if err != nil {
		t.Fatalf("second ClaimDocument: %v", err)
	}
	if claimed2 {
		t.Fatal("expected second claim to fail")
	}
}

func TestClaimDocument_ClearsError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/claimerr")
	// Claim + fail to set error state.
	store.ClaimDocument(ctx, doc.ID, "w1")
	store.FailDocument(ctx, doc.ID, "CRAWLING", "boom", true, 3)

	// Re-claim should clear error and failed_step.
	claimed, got, _ := store.ClaimDocument(ctx, doc.ID, "w2")
	if !claimed {
		t.Fatal("expected re-claim to succeed for FAILED doc")
	}
	if got.Error != nil {
		t.Fatalf("Error should be nil after re-claim, got %v", got.Error)
	}
	if got.FailedStep != nil {
		t.Fatalf("FailedStep should be nil after re-claim, got %v", got.FailedStep)
	}
}
```

- [ ] **Step 22: Write AdvanceStep test**

```go
func TestAdvanceStep(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/advance")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.AdvanceStep(ctx, doc.ID, "CHUNKING"); err != nil {
		t.Fatalf("AdvanceStep: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Step != "CHUNKING" {
		t.Fatalf("Step = %q, want CHUNKING", got.Step)
	}
	if got.State != "PROCESSING" {
		t.Fatalf("State = %q, want PROCESSING (unchanged)", got.State)
	}
}
```

- [ ] **Step 23: Write CompleteDocument test**

```go
func TestCompleteDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/complete")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.CompleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("CompleteDocument: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Step != "DONE" {
		t.Fatalf("Step = %q, want DONE", got.Step)
	}
	if got.State != "COMPLETED" {
		t.Fatalf("State = %q, want COMPLETED", got.State)
	}
	if got.LockedAt != nil || got.LockedBy != nil {
		t.Fatal("lock should be released")
	}
}
```

- [ ] **Step 24: Write FailDocument tests**

```go
func TestFailDocument_Retriable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/failretry")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.FailDocument(ctx, doc.ID, "CRAWLING", "timeout", true, 3); err != nil {
		t.Fatalf("FailDocument: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.State != "FAILED" {
		t.Fatalf("State = %q, want FAILED", got.State)
	}
	if got.FailedStep == nil || *got.FailedStep != "CRAWLING" {
		t.Fatalf("FailedStep = %v, want CRAWLING", got.FailedStep)
	}
	if !got.IsRetriable {
		t.Fatal("expected IsRetriable = true")
	}
	if got.RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.NextRetryAt == nil {
		t.Fatal("NextRetryAt should be set")
	}
	if got.LockedBy != nil || got.LockedAt != nil {
		t.Fatal("lock should be released on failure")
	}
}

func TestFailDocument_Permanent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/failperm")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.FailDocument(ctx, doc.ID, "CRAWLING", "invalid URL", false, 3); err != nil {
		t.Fatalf("FailDocument: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.IsRetriable {
		t.Fatal("expected IsRetriable = false for permanent error")
	}
}

func TestFailDocument_MaxRetries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/failmax")
	for i := 0; i < 3; i++ {
		store.ClaimDocument(ctx, doc.ID, "w1")
		store.FailDocument(ctx, doc.ID, "CRAWLING", "error", true, 3)
	}

	got, _ := store.GetDocument(ctx, doc.ID)
	if got.IsRetriable {
		t.Fatal("expected IsRetriable = false after max retries exhausted")
	}
	if got.RetryCount != 3 {
		t.Fatalf("RetryCount = %d, want 3", got.RetryCount)
	}
}
```

- [ ] **Step 25: Write AbandonStaleDocuments test**

```go
func TestAbandonStaleDocuments(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/abandon")
	store.ClaimDocument(ctx, doc.ID, "w1")

	// Set locked_at to 10 minutes ago.
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET locked_at = now() - interval '10 minutes' WHERE id = $1`, doc.ID)

	count, err := store.AbandonStaleDocuments(ctx, 5*time.Minute)
	if err != nil {
		t.Fatalf("AbandonStaleDocuments: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	got, _ := store.GetDocument(ctx, doc.ID)
	if got.State != "ABANDONED" {
		t.Fatalf("State = %q, want ABANDONED", got.State)
	}
	if got.LockedAt != nil || got.LockedBy != nil {
		t.Fatal("lock should be cleared on abandon")
	}
}
```

- [ ] **Step 26: Rewrite TestGetPendingDocuments**

```go
func TestGetPendingDocuments(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// QUEUED doc — should be included.
	queued, _ := store.InsertDocument(ctx, "https://example.com/queued")

	// COMPLETED doc — should be excluded.
	done, _ := store.InsertDocument(ctx, "https://example.com/done")
	store.ClaimDocument(ctx, done.ID, "w1")
	store.CompleteDocument(ctx, done.ID)

	// ABANDONED doc — should be included.
	abandoned, _ := store.InsertDocument(ctx, "https://example.com/abandoned")
	store.ClaimDocument(ctx, abandoned.ID, "w1")
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET state = 'ABANDONED', locked_at = NULL, locked_by = NULL WHERE id = $1`,
		abandoned.ID)

	// FAILED retriable doc with next_retry_at in past — should be included.
	retriable, _ := store.InsertDocument(ctx, "https://example.com/retriable")
	store.ClaimDocument(ctx, retriable.ID, "w1")
	store.FailDocument(ctx, retriable.ID, "CRAWLING", "error", true, 3)
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET next_retry_at = now() - interval '1 hour' WHERE id = $1`, retriable.ID)

	// FAILED non-retriable doc — should be excluded.
	perm, _ := store.InsertDocument(ctx, "https://example.com/perm")
	store.ClaimDocument(ctx, perm.ID, "w1")
	store.FailDocument(ctx, perm.ID, "CRAWLING", "invalid", false, 3)
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET next_retry_at = now() - interval '1 hour' WHERE id = $1`, perm.ID)

	docs, err := store.GetPendingDocuments(ctx, 3)
	if err != nil {
		t.Fatalf("GetPendingDocuments: %v", err)
	}

	ids := make(map[string]bool)
	for _, d := range docs {
		ids[d.ID] = true
	}

	if !ids[queued.ID] {
		t.Error("QUEUED doc should be included")
	}
	if ids[done.ID] {
		t.Error("COMPLETED doc should be excluded")
	}
	if !ids[abandoned.ID] {
		t.Error("ABANDONED doc should be included")
	}
	if !ids[retriable.ID] {
		t.Error("retriable FAILED doc should be included")
	}
	if ids[perm.ID] {
		t.Error("non-retriable FAILED doc should be excluded")
	}
}
```

- [ ] **Step 27: Verify TestDeleteDocumentCascade needs no changes**

This test only checks cascade delete behavior (insert doc + chunks, delete doc, verify chunks gone). It does not reference `Status`, so no changes are needed.

- [ ] **Step 28: Run all storage tests**

Run: `go test ./internal/storage/ -v`
Expected: All tests pass. (Note: `go build ./...` will fail because worker/server still reference old methods — fixed in tasks 3–4.)

- [ ] **Step 29: Commit**

```bash
git add internal/storage/migrations/002_document_states.sql internal/storage/postgres.go internal/storage/postgres_test.go
git commit -m "feat: implement document state machine in storage layer

Split status into step (pipeline) + state (lifecycle) with locking,
failure tracking, and retriability. Add ClaimDocument, AdvanceStep,
CompleteDocument, FailDocument, AbandonStaleDocuments methods."
```

---

### Task 3: Worker Refactor

**Files:**
- Modify: `internal/worker/worker.go`

- [ ] **Step 1: Add workerID to Worker struct and NewWorker**

Add `workerID string` field to `Worker` struct. Update `NewWorker` to accept and store it:

```go
type Worker struct {
	pg       *storage.PostgresStore
	fs       *storage.FilesystemStore
	crawler  *pipeline.Crawler
	embedder *pipeline.Embedder
	indexer  *pipeline.Indexer
	cfg      *config.Config
	notify   chan workItem
	workerID string
}

func NewWorker(
	pg *storage.PostgresStore,
	fs *storage.FilesystemStore,
	crawler *pipeline.Crawler,
	embedder *pipeline.Embedder,
	indexer *pipeline.Indexer,
	cfg *config.Config,
	workerID string,
) *Worker {
	return &Worker{
		pg:       pg,
		fs:       fs,
		crawler:  crawler,
		embedder: embedder,
		indexer:  indexer,
		cfg:      cfg,
		notify:   make(chan workItem, 8192),
		workerID: workerID,
	}
}
```

- [ ] **Step 2: Add processDocumentStages, failDocument, isPermanentError**

Add `"strings"` to imports. Add these functions:

```go
// processDocumentStages runs a claimed document through pipeline stages,
// resuming from its current step. The document must already be in
// PROCESSING state (claimed).
func (w *Worker) processDocumentStages(ctx context.Context, doc storage.Document, progress chan<- ProgressUpdate) error {
	vectors := make(map[string][]float32)

	switch doc.Step {
	case "CRAWLING":
		if err := w.doCrawl(ctx, &doc, progress); err != nil {
			return w.failDocument(ctx, doc.ID, "CRAWLING", err)
		}
		if err := w.pg.AdvanceStep(ctx, doc.ID, "CHUNKING"); err != nil {
			return fmt.Errorf("advance to CHUNKING: %w", err)
		}
		fallthrough
	case "CHUNKING":
		if err := w.doChunk(ctx, &doc, progress); err != nil {
			return w.failDocument(ctx, doc.ID, "CHUNKING", err)
		}
		if err := w.pg.AdvanceStep(ctx, doc.ID, "EMBEDDING"); err != nil {
			return fmt.Errorf("advance to EMBEDDING: %w", err)
		}
		fallthrough
	case "EMBEDDING":
		if err := w.doEmbed(ctx, &doc, progress, vectors); err != nil {
			return w.failDocument(ctx, doc.ID, "EMBEDDING", err)
		}
		if err := w.pg.AdvanceStep(ctx, doc.ID, "INDEXING"); err != nil {
			return fmt.Errorf("advance to INDEXING: %w", err)
		}
		fallthrough
	case "INDEXING":
		if err := w.doIndex(ctx, &doc, progress, vectors); err != nil {
			return w.failDocument(ctx, doc.ID, "INDEXING", err)
		}
		if err := w.pg.CompleteDocument(ctx, doc.ID); err != nil {
			return fmt.Errorf("complete document: %w", err)
		}
	case "DONE":
		return nil
	}
	return nil
}

// failDocument records a failure on the document and returns the original error.
func (w *Worker) failDocument(ctx context.Context, docID, step string, err error) error {
	retriable := !isPermanentError(err)
	if setErr := w.pg.FailDocument(ctx, docID, step, err.Error(), retriable, w.cfg.MaxRetries); setErr != nil {
		log.Printf("worker: failed to record failure on document %s: %v", docID, setErr)
	}
	return err
}

// isPermanentError returns true for errors that should not be retried.
func isPermanentError(err error) bool {
	msg := err.Error()
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "invalid URL") {
		return true
	}
	if strings.Contains(msg, "too many tokens") {
		return true
	}
	return false
}
```

- [ ] **Step 3: Rewrite ProcessDocument**

```go
func (w *Worker) ProcessDocument(ctx context.Context, docID string, progress chan<- ProgressUpdate) error {
	claimed, doc, err := w.pg.ClaimDocument(ctx, docID, w.workerID)
	if err != nil {
		return fmt.Errorf("claim document: %w", err)
	}
	if !claimed {
		return nil // another worker has it or it's already done
	}
	return w.processDocumentStages(ctx, doc, progress)
}
```

- [ ] **Step 4: Remove UpdateDocumentStatus call from doCrawl**

Remove this line from the top of `doCrawl`:
```go
if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "CRAWLING"); err != nil {
    return fmt.Errorf("set status CRAWLING: %w", err)
}
```

- [ ] **Step 5: Remove UpdateDocumentStatus call from saveCrawlResult**

Remove this line from the top of `saveCrawlResult`:
```go
if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "CRAWLING"); err != nil {
    return fmt.Errorf("set status CRAWLING: %w", err)
}
```

- [ ] **Step 6: Remove UpdateDocumentStatus call from doChunk**

Remove this line from the top of `doChunk`:
```go
if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "CHUNKING"); err != nil {
    return fmt.Errorf("set status CHUNKING: %w", err)
}
```

- [ ] **Step 7: Remove UpdateDocumentStatus call from doEmbed**

Remove this line from the top of `doEmbed`:
```go
if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "EMBEDDING"); err != nil {
    return fmt.Errorf("set status EMBEDDING: %w", err)
}
```

- [ ] **Step 8: Remove UpdateDocumentStatus calls from doIndex**

Remove the `UpdateDocumentStatus(ctx, doc.ID, "INDEXING")` call from the top of `doIndex`.

Remove both `UpdateDocumentStatus(ctx, doc.ID, "DONE")` calls from `doIndex` (in the empty-chunks branch and at the end). The `DONE` completion is now handled by `CompleteDocument` in `processDocumentStages`.

- [ ] **Step 9: Delete the handleError method**

Remove the entire `handleError` function — it's replaced by `failDocument`.

- [ ] **Step 10: Rewrite processBatch**

Replace the entire `processBatch` function. Key changes:
1. Use `ClaimDocument` instead of `GetDocument` + `ClearDocumentError`
2. Separate docs by step: CRAWLING goes to batch crawl, others go to processDocumentStages
3. Use `failDocument` instead of `handleError`
4. Add `AdvanceStep` calls between stages
5. Use `processDocumentStages` for embed+index fan-out

```go
func (w *Worker) processBatch(ctx context.Context, batch []workItem) {
	type batchDoc struct {
		item workItem
		doc  storage.Document
	}

	var freshDocs []batchDoc  // step=CRAWLING, need batch crawl
	var resumeDocs []batchDoc // step past CRAWLING, resume from current step

	// Phase 1: Claim all documents.
	for _, item := range batch {
		claimed, doc, err := w.pg.ClaimDocument(ctx, item.documentID, w.workerID)
		if err != nil {
			log.Printf("worker: failed to claim document %s: %v", item.documentID, err)
			if item.progress != nil {
				sendProgress(item.progress, ProgressUpdate{
					DocumentID: item.documentID, Status: "ERROR", Message: err.Error(),
				})
				close(item.progress)
			}
			continue
		}
		if !claimed {
			if item.progress != nil {
				close(item.progress)
			}
			continue
		}

		if doc.Step == "CRAWLING" {
			freshDocs = append(freshDocs, batchDoc{item: item, doc: doc})
		} else {
			resumeDocs = append(resumeDocs, batchDoc{item: item, doc: doc})
		}
	}

	if len(freshDocs) == 0 && len(resumeDocs) == 0 {
		return
	}

	var wg sync.WaitGroup

	// Phase 2: Resume docs — process from their current step.
	for _, bd := range resumeDocs {
		wg.Add(1)
		go func(bd batchDoc) {
			defer wg.Done()
			defer func() {
				if bd.item.progress != nil {
					close(bd.item.progress)
				}
			}()

			if err := w.processDocumentStages(ctx, bd.doc, bd.item.progress); err != nil {
				if bd.item.progress != nil {
					sendProgress(bd.item.progress, ProgressUpdate{
						DocumentID: bd.doc.ID, Status: "ERROR", Message: err.Error(),
					})
				}
				log.Printf("worker: resume failed for %s: %v", bd.doc.URL, err)
			}
		}(bd)
	}

	// Phase 3: Batch crawl fresh docs.
	if len(freshDocs) > 0 {
		// Separate Reddit URLs from regular URLs.
		var regularDocs []batchDoc
		var redditDocs []batchDoc
		for _, bd := range freshDocs {
			if pipeline.IsRedditURL(bd.doc.URL) {
				redditDocs = append(redditDocs, bd)
			} else {
				regularDocs = append(regularDocs, bd)
			}
		}

		// Separate prefetch HTML docs from regular docs.
		var prefetchDocs []batchDoc
		var regularDocsFiltered []batchDoc
		for _, bd := range regularDocs {
			if w.fs.HasPrefetchHTML(bd.doc.ID) {
				prefetchDocs = append(prefetchDocs, bd)
			} else {
				regularDocsFiltered = append(regularDocsFiltered, bd)
			}
		}
		regularDocs = regularDocsFiltered

		// Crawl all concurrently.
		crawlResults := make(map[string]pipeline.CrawlResult)
		crawlErrors := make(map[string]error)
		var crawlWg sync.WaitGroup

		if len(regularDocs) > 0 {
			crawlWg.Add(1)
			go func() {
				defer crawlWg.Done()
				urls := make([]string, len(regularDocs))
				for i, bd := range regularDocs {
					urls[i] = bd.doc.URL
				}
				log.Printf("worker: batch crawling %d URLs", len(urls))
				results, errs := w.crawler.CrawlBatch(ctx, urls)
				for url, result := range results {
					crawlResults[url] = result
				}
				for url, err := range errs {
					crawlErrors[url] = err
				}
			}()
		}

		for _, bd := range redditDocs {
			crawlWg.Add(1)
			go func(bd batchDoc) {
				defer crawlWg.Done()
				result, err := w.crawler.Crawl(ctx, bd.doc.URL)
				if err != nil {
					crawlErrors[bd.doc.URL] = err
				} else {
					crawlResults[bd.doc.URL] = result
				}
			}(bd)
		}

		for _, bd := range prefetchDocs {
			crawlWg.Add(1)
			go func(bd batchDoc) {
				defer crawlWg.Done()
				html, err := w.fs.ReadPrefetchHTML(bd.doc.ID)
				if err != nil {
					crawlErrors[bd.doc.URL] = err
					return
				}
				result, err := w.crawler.CrawlWithHTML(ctx, bd.doc.URL, string(html))
				if err != nil {
					crawlErrors[bd.doc.URL] = err
				} else {
					crawlResults[bd.doc.URL] = result
					w.fs.DeletePrefetchHTML(bd.doc.ID)
				}
			}(bd)
		}

		crawlWg.Wait()

		// Phase 4: Process crawl results — save, chunk, then fan out embed+index.
		for i := range freshDocs {
			bd := &freshDocs[i]
			url := bd.doc.URL

			if crawlErr, failed := crawlErrors[url]; failed {
				w.failDocument(ctx, bd.doc.ID, "CRAWLING", fmt.Errorf("crawl: %w", crawlErr))
				if bd.item.progress != nil {
					sendProgress(bd.item.progress, ProgressUpdate{
						DocumentID: bd.doc.ID, Status: "ERROR", Message: crawlErr.Error(),
					})
					close(bd.item.progress)
				}
				log.Printf("worker: crawl failed for %s: %v", url, crawlErr)
				continue
			}

			result := crawlResults[url]
			if err := w.saveCrawlResult(ctx, &bd.doc, result, bd.item.progress); err != nil {
				w.failDocument(ctx, bd.doc.ID, "CRAWLING", err)
				if bd.item.progress != nil {
					sendProgress(bd.item.progress, ProgressUpdate{
						DocumentID: bd.doc.ID, Status: "ERROR", Message: err.Error(),
					})
					close(bd.item.progress)
				}
				log.Printf("worker: save crawl result failed for %s: %v", url, err)
				continue
			}

			if err := w.pg.AdvanceStep(ctx, bd.doc.ID, "CHUNKING"); err != nil {
				log.Printf("worker: advance to CHUNKING failed for %s: %v", bd.doc.ID, err)
				if bd.item.progress != nil {
					close(bd.item.progress)
				}
				continue
			}

			if err := w.doChunk(ctx, &bd.doc, bd.item.progress); err != nil {
				w.failDocument(ctx, bd.doc.ID, "CHUNKING", err)
				if bd.item.progress != nil {
					sendProgress(bd.item.progress, ProgressUpdate{
						DocumentID: bd.doc.ID, Status: "ERROR", Message: err.Error(),
					})
					close(bd.item.progress)
				}
				log.Printf("worker: chunk failed for %s: %v", url, err)
				continue
			}

			if err := w.pg.AdvanceStep(ctx, bd.doc.ID, "EMBEDDING"); err != nil {
				log.Printf("worker: advance to EMBEDDING failed for %s: %v", bd.doc.ID, err)
				if bd.item.progress != nil {
					close(bd.item.progress)
				}
				continue
			}
			bd.doc.Step = "EMBEDDING"

			// Fan out embed + index.
			wg.Add(1)
			go func(bd batchDoc) {
				defer wg.Done()
				defer func() {
					if bd.item.progress != nil {
						close(bd.item.progress)
					}
				}()

				if err := w.processDocumentStages(ctx, bd.doc, bd.item.progress); err != nil {
					if bd.item.progress != nil {
						sendProgress(bd.item.progress, ProgressUpdate{
							DocumentID: bd.doc.ID, Status: "ERROR", Message: err.Error(),
						})
					}
					log.Printf("worker: embed/index failed for %s: %v", bd.doc.URL, err)
				}
			}(*bd)
		}
	}

	wg.Wait()
}
```

- [ ] **Step 11: Update retryScanner to call AbandonStaleDocuments**

Add `AbandonStaleDocuments` call at the beginning of each tick, before scanning for pending docs. Insert this block after the old-entry cleanup loop and before the `GetPendingDocuments` call:

```go
// Abandon stale documents before scanning for retries.
if count, err := w.pg.AbandonStaleDocuments(ctx, 5*time.Minute); err != nil {
	log.Printf("worker: abandon stale failed: %v", err)
} else if count > 0 {
	log.Printf("worker: abandoned %d stale documents", count)
}
```

- [ ] **Step 12: Verify build and tests**

Run: `go build ./...`
Expected: Compiles successfully.

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 13: Commit**

```bash
git add internal/worker/worker.go
git commit -m "feat: use document state machine in worker

Replace status-based pipeline with step+state model. Worker claims
documents atomically, advances steps explicitly, records failures
with retriability, and detects abandoned documents."
```

---

### Task 4: Server + Main

**Files:**
- Modify: `internal/server/server.go` (documentToProto)
- Modify: `internal/server/http.go` (ingestStatusResponse, handleIngestStatus)
- Modify: `cmd/mykb-api/main.go` (workerID generation)

- [ ] **Step 1: Update documentToProto in server.go**

Replace `Status: doc.Status` with `Status: doc.DisplayStatus()`:

```go
func documentToProto(doc storage.Document) *mykbv1.Document {
	d := &mykbv1.Document{
		Id:        doc.ID,
		Url:       doc.URL,
		Status:    doc.DisplayStatus(),
		CreatedAt: doc.CreatedAt.Unix(),
		UpdatedAt: doc.UpdatedAt.Unix(),
	}
	if doc.Error != nil {
		d.Error = *doc.Error
	}
	if doc.Title != nil {
		d.Title = *doc.Title
	}
	if doc.ChunkCount != nil {
		d.ChunkCount = int32(*doc.ChunkCount)
	}
	if doc.CrawledAt != nil {
		d.CrawledAt = doc.CrawledAt.Unix()
	}
	return d
}
```

- [ ] **Step 2: Update ingestStatusResponse and handleIngestStatus in http.go**

Update the response struct:

```go
type ingestStatusResponse struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Step   string  `json:"step"`
	State  string  `json:"state"`
	Error  *string `json:"error"`
}
```

Update the handler to use DisplayStatus and include step/state:

```go
_ = json.NewEncoder(rw).Encode(ingestStatusResponse{
	ID:     doc.ID,
	Status: doc.DisplayStatus(),
	Step:   doc.Step,
	State:  doc.State,
	Error:  doc.Error,
})
```

- [ ] **Step 3: Generate workerID in main.go**

In `cmd/mykb-api/main.go`, before the `NewWorker` call, add worker ID generation. Add `"os"` to imports if not already present:

```go
hostname, _ := os.Hostname()
workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())
log.Printf("worker ID: %s", workerID)

w := worker.NewWorker(pg, fs, crawler, embedder, indexer, cfg, workerID)
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: Compiles successfully.

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 5: Verify linting**

Run: `golangci-lint run ./...`
Expected: No new warnings.

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/server/http.go cmd/mykb-api/main.go
git commit -m "feat: wire document state machine to server and API

Map step+state to display status for backward-compatible proto and
HTTP responses. Generate worker ID from hostname-PID."
```

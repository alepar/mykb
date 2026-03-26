# Document State Machine Redesign

## Problem

The current `status` column conflates pipeline progress with processing lifecycle. A document in status `CRAWLING` could mean it's actively being crawled, crashed mid-crawl, or failed and waiting for retry. This ambiguity caused multiple bugs during bulk ingestion:

- Documents stuck at EMBEDDING/CRAWLING with max retries but not marked as ERROR
- Retry scanner re-queuing documents already being processed
- No way to distinguish permanent vs retriable errors
- No way to detect abandoned documents (worker crash mid-processing)

## Solution

Split `status` into two columns: `step` (pipeline progress) and `state` (processing lifecycle). Add explicit locking, failure tracking, and retriability.

## New Columns

| Column | Type | Default | Purpose |
|--------|------|---------|---------|
| `step` | TEXT NOT NULL | 'CRAWLING' | Pipeline stage: CRAWLING, CHUNKING, EMBEDDING, INDEXING, DONE |
| `state` | TEXT NOT NULL | 'QUEUED' | Lifecycle: QUEUED, PROCESSING, COMPLETED, FAILED, ABANDONED |
| `failed_step` | TEXT | NULL | Which step caused the error |
| `is_retriable` | BOOL NOT NULL | true | Whether the error is worth retrying |
| `locked_at` | TIMESTAMPTZ | NULL | When a worker claimed this document |
| `locked_by` | TEXT | NULL | Worker identifier (hostname-PID, per-process) |

Removed: `status` (replaced by `step` + `state`). The old `PENDING` status is eliminated — new documents start at `step='CRAWLING', state='QUEUED'`. The worker's `ProcessDocument` switch removes the `case "PENDING"` branch.

Kept unchanged: `id`, `url`, `error`, `title`, `chunk_count`, `retry_count`, `next_retry_at`, `crawled_at`, `created_at`, `updated_at`

## State Transition Protocol

### Document creation

```sql
INSERT INTO documents (url) VALUES ($1)
-- Defaults: step='CRAWLING', state='QUEUED', retry_count=0
```

### Worker claims a document (atomic)

```sql
UPDATE documents
SET state = 'PROCESSING', locked_at = now(), locked_by = $worker_id,
    error = NULL, failed_step = NULL, updated_at = now()
WHERE id = $1 AND state IN ('QUEUED', 'FAILED', 'ABANDONED')
RETURNING *
```

If 0 rows affected, another worker already claimed it — skip. Clears stale `error` and `failed_step` on claim so API consumers don't see stale error messages during reprocessing.

### Abandoned document re-processing

When a document is abandoned and then re-claimed, its `step` is NOT reset — the worker resumes from wherever it was (same as today's `ProcessDocument` which resumes from current status). This is safe because:
- Crawl: re-fetches and overwrites markdown on disk
- Chunk: deletes existing chunks before re-chunking
- Embed: re-embeds and stores new vectors
- Index: detects missing vectors and re-embeds if needed

### Worker advances a step

```sql
UPDATE documents SET step = $new_step, updated_at = now() WHERE id = $1
```

State stays PROCESSING, lock stays held.

### Worker completes successfully

```sql
UPDATE documents
SET step = 'DONE', state = 'COMPLETED',
    locked_at = NULL, locked_by = NULL, error = NULL,
    failed_step = NULL, updated_at = now()
WHERE id = $1
```

### Worker encounters a retriable error

```sql
UPDATE documents
SET state = 'FAILED', failed_step = $step, error = $msg,
    retry_count = retry_count + 1,
    next_retry_at = now() + make_interval(secs => power(2, retry_count) * 30),
    is_retriable = CASE WHEN retry_count + 1 >= $max_retries THEN false ELSE true END,
    locked_at = NULL, locked_by = NULL, updated_at = now()
WHERE id = $1
```

Releases the lock. Sets `is_retriable = false` when max retries exhausted (combined in one query, no separate "max retries exhausted" step).

### Worker encounters a permanent error

Same query but `is_retriable = false` unconditionally. Examples: invalid URL, malformed HTML with no recoverable content.

**Determining retriability:** By default, all errors are retriable. Permanent errors are detected by checking the error type:
- Crawl4AI "invalid URL" / DNS resolution failure → permanent
- Voyage API "too many tokens" after truncation → permanent
- Everything else (timeouts, 429s, 500s, connection errors) → retriable

### Retry scanner query

```sql
SELECT * FROM documents
WHERE state = 'FAILED' AND is_retriable = true
  AND retry_count < $max_retries AND next_retry_at <= now()
ORDER BY next_retry_at
```

No dedup map needed — documents in PROCESSING state are not returned.

### Abandoned document detection

```sql
UPDATE documents
SET state = 'ABANDONED', locked_at = NULL, locked_by = NULL, updated_at = now()
WHERE state = 'PROCESSING' AND locked_at < now() - interval '5 minutes'
```

The retry scanner runs this query on every tick (30s) before scanning for FAILED docs. ABANDONED docs become eligible for reprocessing via the startup resume / retry scanner queries.

### Startup resume

```sql
SELECT * FROM documents
WHERE state IN ('QUEUED', 'ABANDONED')
   OR (state = 'FAILED' AND is_retriable = true
       AND retry_count < $max_retries AND next_retry_at <= now())
ORDER BY created_at
```

## Valid State Combinations

| step | state | Meaning |
|------|-------|---------|
| CRAWLING | QUEUED | Freshly inserted, waiting to be picked up |
| CRAWLING | PROCESSING | Worker is actively crawling |
| CRAWLING | FAILED | Crawl failed, will retry (if is_retriable) |
| CRAWLING | ABANDONED | Worker crashed mid-crawl |
| CHUNKING | PROCESSING | Worker is chunking |
| CHUNKING | FAILED | Chunking failed |
| EMBEDDING | PROCESSING | Worker is embedding |
| EMBEDDING | FAILED | Embedding failed (e.g., token limit) |
| INDEXING | PROCESSING | Worker is indexing |
| INDEXING | FAILED | Indexing failed |
| DONE | COMPLETED | Successfully ingested |

Any `step` + `state=FAILED` with `is_retriable=false` means permanently failed.

## Proto and API Backward Compatibility

The proto `Document` message keeps its `status` field (field tag 4). The `documentToProto` function maps step+state to a display string for backward compatibility:

| state | is_retriable | Mapped status |
|-------|-------------|---------------|
| QUEUED | — | PENDING |
| PROCESSING | — | {step} (e.g., CRAWLING, EMBEDDING) |
| COMPLETED | — | DONE |
| FAILED | true | {step} (e.g., CRAWLING — same as before, shows where it's stuck) |
| FAILED | false | ERROR |
| ABANDONED | — | {step} |

This preserves the values that the CLI client, Firefox extension (`pollStatus` badge), and web UI expect. No proto changes needed.

The `ProgressUpdate.Status` field in worker.go continues to send step names (CRAWLING, CHUNKING, etc.) and DONE/ERROR — same values as today.

## StatusCounts

The `StatusCounts` method changes from `GROUP BY status` to group by the mapped display status (same mapping as proto):

```sql
SELECT
  CASE
    WHEN state = 'QUEUED' THEN 'PENDING'
    WHEN state = 'COMPLETED' THEN 'DONE'
    WHEN state = 'FAILED' AND is_retriable = false THEN 'ERROR'
    WHEN state = 'FAILED' THEN step
    WHEN state = 'ABANDONED' THEN step
    ELSE step
  END AS display_status,
  COUNT(*)
FROM documents GROUP BY display_status
```

This preserves the frontend's existing status breakdown (DONE, PENDING, ERROR, CRAWLING, EMBEDDING, etc.).

## Migration

### Migration runner update

The current `RunMigrations` is hardcoded to `001_init.sql` and skips if the table exists. Update it to:
1. Create a `schema_migrations` table to track applied migrations
2. Scan `migrations/*.sql` files in sorted order
3. Apply any migration not yet recorded in `schema_migrations`

This is a prerequisite for `002_document_states.sql` to execute.

### Migration SQL (`002_document_states.sql`)

```sql
ALTER TABLE documents ADD COLUMN step TEXT;
ALTER TABLE documents ADD COLUMN state TEXT;
ALTER TABLE documents ADD COLUMN failed_step TEXT;
ALTER TABLE documents ADD COLUMN is_retriable BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE documents ADD COLUMN locked_at TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN locked_by TEXT;

-- Migrate existing data (PENDING maps to step=CRAWLING, state=QUEUED)
UPDATE documents SET step = 'DONE', state = 'COMPLETED' WHERE status = 'DONE';
UPDATE documents SET step = status, state = 'FAILED', failed_step = status, is_retriable = false WHERE status = 'ERROR';
UPDATE documents SET step = CASE WHEN status = 'PENDING' THEN 'CRAWLING' ELSE status END, state = 'QUEUED' WHERE status NOT IN ('DONE', 'ERROR');

ALTER TABLE documents ALTER COLUMN step SET NOT NULL;
ALTER TABLE documents ALTER COLUMN state SET NOT NULL;

ALTER TABLE documents DROP COLUMN status;

-- New indexes
DROP INDEX IF EXISTS idx_documents_status;
CREATE INDEX idx_documents_state ON documents(state);
CREATE INDEX idx_documents_retry ON documents(next_retry_at) WHERE state = 'FAILED' AND is_retriable = true;
CREATE INDEX idx_documents_abandoned ON documents(locked_at) WHERE state = 'PROCESSING';
```

## Code Changes

| File | Change |
|------|--------|
| `migrations/002_document_states.sql` | New migration file |
| `internal/storage/postgres.go` | Update migration runner to support ordered multi-file migrations with `schema_migrations` tracking. Update `Document` struct (Step/State replace Status). Replace `UpdateDocumentStatus`, `SetDocumentError`, `ClearDocumentError` with `ClaimDocument`, `AdvanceStep`, `CompleteDocument`, `FailDocument`, `AbandonStaleDocuments`. Update `GetPendingDocuments`, `StatusCounts`, `documentToProto` mapping. |
| `internal/storage/postgres_test.go` | Update all tests for new struct fields and methods |
| `internal/worker/worker.go` | Use `ClaimDocument` before processing. Use `CompleteDocument` on success. Use `FailDocument` on error (with `isRetriable` flag). Call `AbandonStaleDocuments` in retry scanner. Remove `handleError`, `ClearDocumentError` calls. Remove `case "PENDING"` from ProcessDocument switch. |
| `internal/server/server.go` | Update `documentToProto` to map step+state → display status for proto `Document.status` field |
| `internal/server/http.go` | Update `ingestStatusResponse` to include step/state. Update `StatusCounts` usage. |
| `cmd/mykb-api/main.go` | Generate worker ID (`hostname-PID`) and pass to worker. Worker uses it for `locked_by`. |
| `frontend/src/api.ts` | Update Document type — `status` stays (mapped by server), add `step`/`state` if needed for richer display |
| `frontend/src/pages/StatusPage.tsx` | Show step + state in table (or keep using mapped status) |

## What Does Not Change

- Pipeline stages (crawl, chunk, embed, index)
- Batch coordinator flow
- Rate limiter
- K8s manifests
- Proto `Document.status` field tag (mapped from step+state, backward compatible)
- CLI client behavior
- Firefox extension polling behavior

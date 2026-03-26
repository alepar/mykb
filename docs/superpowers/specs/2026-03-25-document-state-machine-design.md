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
| `locked_by` | TEXT | NULL | Worker identifier |

Removed: `status` (replaced by `step` + `state`)

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
SET state = 'PROCESSING', locked_at = now(), locked_by = $worker_id, updated_at = now()
WHERE id = $1 AND state IN ('QUEUED', 'FAILED', 'ABANDONED')
RETURNING *
```

If 0 rows affected, another worker already claimed it — skip.

### Worker advances a step

```sql
UPDATE documents SET step = $new_step, updated_at = now() WHERE id = $1
```

State stays PROCESSING, lock stays held.

### Worker completes successfully

```sql
UPDATE documents
SET step = 'DONE', state = 'COMPLETED',
    locked_at = NULL, locked_by = NULL, error = NULL, updated_at = now()
WHERE id = $1
```

### Worker encounters a retriable error

```sql
UPDATE documents
SET state = 'FAILED', failed_step = $step, error = $msg,
    retry_count = retry_count + 1,
    next_retry_at = now() + make_interval(secs => power(2, retry_count) * 30),
    is_retriable = true, locked_at = NULL, locked_by = NULL, updated_at = now()
WHERE id = $1
```

Releases the lock so the retry scanner can pick it up later.

### Worker encounters a permanent error

Same as retriable but `is_retriable = false`. Examples: invalid URL, content exceeds all size limits after truncation, malformed HTML.

### Max retries exhausted

When `retry_count + 1 >= max_retries`, set `is_retriable = false`.

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

The retry scanner runs this query on every tick (30s) before scanning for FAILED docs. ABANDONED docs become eligible for reprocessing.

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
| DONE | COMPLETED | Successfully ingested |

Any `step` + `state=FAILED` with `is_retriable=false` means permanently failed.

## Migration

```sql
-- 002_document_states.sql

ALTER TABLE documents ADD COLUMN step TEXT;
ALTER TABLE documents ADD COLUMN state TEXT;
ALTER TABLE documents ADD COLUMN failed_step TEXT;
ALTER TABLE documents ADD COLUMN is_retriable BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE documents ADD COLUMN locked_at TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN locked_by TEXT;

-- Migrate existing data
UPDATE documents SET step = status, state = 'COMPLETED' WHERE status = 'DONE';
UPDATE documents SET step = status, state = 'FAILED', failed_step = status, is_retriable = false WHERE status = 'ERROR';
UPDATE documents SET step = status, state = 'QUEUED' WHERE status NOT IN ('DONE', 'ERROR');

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
| `internal/storage/postgres.go` | Update Document struct, all queries using `status` → `step`/`state`. Add `ClaimDocument`, `CompleteDocument`, `FailDocument`, `AbandonStaleDocuments`. Remove `SetDocumentError`. Update `GetPendingDocuments`. |
| `internal/worker/worker.go` | Use `ClaimDocument` before processing. Use `CompleteDocument` on success. Use `FailDocument` on error (with `isRetriable` flag). Call `AbandonStaleDocuments` in retry scanner. Remove `handleError`. |
| `internal/server/server.go` | Update `doc.Status` references to `doc.Step`/`doc.State` |
| `internal/server/http.go` | Update `ingestStatusResponse` to include step/state |
| `cmd/mykb-api/main.go` | Generate worker ID (e.g., hostname + PID) for `locked_by` |
| `frontend/src/api.ts` | Update Document type (step/state instead of status) |
| `frontend/src/pages/StatusPage.tsx` | Show step + state in table |

## What Does Not Change

- Pipeline stages (crawl, chunk, embed, index)
- Batch coordinator flow
- Rate limiter
- K8s manifests
- Proto/ConnectRPC (uses its own Document message with `status` field — can map step+state to a display string)

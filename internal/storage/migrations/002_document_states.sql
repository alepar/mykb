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
ALTER TABLE documents ALTER COLUMN step SET DEFAULT 'CRAWLING';
ALTER TABLE documents ALTER COLUMN state SET NOT NULL;
ALTER TABLE documents ALTER COLUMN state SET DEFAULT 'QUEUED';

ALTER TABLE documents DROP COLUMN status;

-- Replace old index with new ones.
DROP INDEX IF EXISTS idx_documents_status;
CREATE INDEX idx_documents_state ON documents(state);
CREATE INDEX idx_documents_retry ON documents(next_retry_at) WHERE state = 'FAILED' AND is_retriable = true;
CREATE INDEX idx_documents_abandoned ON documents(locked_at) WHERE state = 'PROCESSING';

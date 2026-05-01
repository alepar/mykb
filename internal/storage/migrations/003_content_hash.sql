-- Adds content_hash column for wiki document idempotent re-ingest.
-- NULL for raw-source documents (which use crawled_at for staleness checks).
ALTER TABLE documents ADD COLUMN content_hash TEXT;

CREATE INDEX idx_documents_content_hash ON documents(content_hash) WHERE content_hash IS NOT NULL;

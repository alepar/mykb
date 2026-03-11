# RAG Knowledge Base - Design Document

## Overview

A RAG-based knowledge base that ingests user-provided URLs, converts them to markdown, chunks them at semantic boundaries, generates contextual embeddings, and provides hybrid search (vector + full-text) with reranking.

Runs as a docker-compose stack: Go gRPC service + Postgres + Qdrant + Meilisearch + Crawl4AI.

## Architecture

Single Go service orchestrates the entire pipeline. Postgres serves as the durable job queue with status tracking and retry logic. All text content lives on the filesystem; Postgres stores only metadata and status.

### Docker Compose Services

| Service | Image | Purpose |
|---------|-------|---------|
| mykb | Custom Go build | gRPC API server + background ingestion worker |
| postgres | postgres:17 | Document/chunk metadata, job queue |
| qdrant | qdrant/qdrant:v1.17.0 | Vector similarity search |
| meilisearch | getmeili/meilisearch:v1.37.0 | Full-text search |
| crawl4ai | unclecode/crawl4ai:latest | URL crawling, HTML to markdown |

Only the Go service exposes a port (9090 gRPC). All other services communicate via docker network.

## Data Model

### Postgres Schema

```sql
CREATE TABLE documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    url             TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'PENDING',
    error           TEXT,
    title           TEXT,
    chunk_count     INT,
    retry_count     INT NOT NULL DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    crawled_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE chunks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index     INT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(document_id, chunk_index)
);
```

Document statuses: `PENDING -> CRAWLING -> CHUNKING -> CONTEXTUALIZING -> EMBEDDING -> INDEXING -> DONE`

Failure is indicated by `error IS NOT NULL` on the current status. This tells you both that it failed and which stage failed.

### Filesystem Layout

```
data/documents/{uuid[0:2]}/{uuid[2:4]}/
  {uuid}.md          # full crawled markdown
  {uuid}.000t.md     # chunk 0 text
  {uuid}.000c.md     # chunk 0 context
  {uuid}.001t.md     # chunk 1 text
  {uuid}.001c.md     # chunk 1 context
  ...
```

Two levels of directory sharding (~65K possible leaf directories).

### Qdrant

- Collection with 1024-dim vectors (voyage-3-large), cosine distance, int8 scalar quantization
- Point payload: `{document_id, chunk_index}`

### Meilisearch

- Searchable field: contextualized text (chunk text + context, written at index time)
- Filterable: `chunk_id`, `document_id`, `chunk_index`

## gRPC API

```protobuf
service KBService {
  rpc IngestURL(IngestURLRequest) returns (stream IngestProgress);
  rpc Query(QueryRequest) returns (QueryResponse);
  rpc ListDocuments(ListDocumentsRequest) returns (ListDocumentsResponse);
  rpc GetDocuments(GetDocumentsRequest) returns (GetDocumentsResponse);
  rpc DeleteDocument(DeleteDocumentRequest) returns (DeleteDocumentResponse);
}
```

### IngestURL

Server-streaming RPC. Inserts document as PENDING, then streams progress updates as the pipeline runs. Returns `document_id`, current `status`, `message`, `chunks_total`, `chunks_processed`.

### Query

Hybrid search with configurable depths:

- `query`: search text
- `top_k`: final results (default 10)
- `vector_depth`: Qdrant candidates (default 100)
- `fts_depth`: Meilisearch candidates (default 100)
- `rerank_depth`: candidates passed to Voyage AI reranker (default 50)

Returns `QueryResult` with `chunk_id`, `document_id`, `chunk_index`, `score`.

### GetDocuments

Batched retrieval by IDs. `include_content` flag to optionally return full markdown from filesystem.

### DeleteDocument

Deletes document and cascades: removes chunks from Postgres, vectors from Qdrant, documents from Meilisearch, files from filesystem.

## Ingestion Pipeline

### Stage-by-stage

1. **PENDING**: Document row inserted in Postgres
2. **CRAWLING**: Call Crawl4AI REST API, write markdown to filesystem, extract title
3. **CHUNKING**: Markdown-aware splitting (split by `##` headers, subdivide large sections by paragraphs, target ~800 tokens, max ~1500). Write chunk files, insert chunk rows
4. **CONTEXTUALIZING**: For each chunk with `status=PENDING`, call Claude Haiku with document context (prompt-cached) + chunk content. Write context file, update chunk status. Sequential processing within a document for prompt cache efficiency
5. **EMBEDDING**: Batch embed via Voyage AI voyage-3-large (up to 128 chunks per call). Update chunk status
6. **INDEXING**: Upsert vectors to Qdrant, index contextualized text to Meilisearch. Update chunk and document status to DONE

### Contextual Embeddings

Following the Anthropic cookbook approach:

- Document context prompt: full markdown with `cache_control: ephemeral`
- Chunk context prompt: asks Claude to situate the chunk within the document
- Generated context is prepended to chunk text before embedding: `chunk_text + "\n\n" + context`
- Sequential chunk processing per document maximizes prompt cache hits

### Resumability

On startup or after failure, the worker queries for incomplete documents:

```sql
SELECT * FROM documents
WHERE status NOT IN ('DONE')
  AND (error IS NULL OR (retry_count < 5 AND next_retry_at <= now()))
ORDER BY created_at;
```

Each stage resumes based on what's already stored:

| Status | Resume action |
|---|---|
| CRAWLING | Re-crawl (idempotent) |
| CHUNKING | Delete existing chunks, re-split from stored markdown |
| CONTEXTUALIZING | Process only chunks where context file doesn't exist |
| EMBEDDING | Process only chunks with status < EMBEDDED |
| INDEXING | Process only chunks with status < DONE |

### Retry Logic

On failure: increment `retry_count`, set `next_retry_at = now() + 2^retry_count * 30s`. Max 5 retries (30s, 1m, 2m, 4m, 8m). After 5 retries, document stays failed until manually re-triggered.

## Query Pipeline

1. **Parallel fan-out**: Embed query via Voyage AI + search Meilisearch (concurrent)
2. **Qdrant search**: Top `vector_depth` nearest neighbors using query embedding
3. **RRF fusion**: Merge Qdrant + Meilisearch results with Reciprocal Rank Fusion (k=60)
4. **Rerank**: Top `rerank_depth` candidates through Voyage AI rerank-2
5. **Return**: Top `top_k` results with scores

## Project Structure

```
mykb/
├── cmd/mykb/main.go                    # entrypoint
├── proto/mykb/v1/kb.proto              # gRPC definition
├── internal/
│   ├── server/server.go                # gRPC handlers
│   ├── worker/worker.go                # background ingestion pipeline
│   ├── pipeline/
│   │   ├── crawl.go                    # Crawl4AI client
│   │   ├── chunk.go                    # markdown-aware chunking
│   │   ├── contextualize.go            # Claude API contextual embeddings
│   │   ├── embed.go                    # Voyage AI embedding client
│   │   └── index.go                    # Qdrant + Meilisearch indexing
│   ├── search/
│   │   ├── search.go                   # hybrid query orchestration
│   │   ├── rrf.go                      # reciprocal rank fusion
│   │   └── rerank.go                   # Voyage AI reranker client
│   ├── storage/
│   │   ├── postgres.go                 # document/chunk DB operations
│   │   ├── filesystem.go               # read/write markdown files
│   │   ├── qdrant.go                   # vector operations
│   │   └── meilisearch.go              # FTS operations
│   └── config/config.go                # env-based configuration
├── migrations/001_init.sql
├── docker-compose.yml
├── Dockerfile
├── go.mod
├── Justfile
└── .env.example
```

## External APIs

| API | Model | Purpose | Pricing |
|-----|-------|---------|---------|
| Anthropic Claude | claude-haiku-4-5-20251001 | Contextual embeddings (chunk situating) | With prompt caching |
| Voyage AI | voyage-3-large (1024 dims) | Chunk + query embedding | $0.18/1M tokens |
| Voyage AI | rerank-2 | Reranking query results | Per request |

## Key Design Decisions

1. **Postgres as job queue** — No separate message queue infrastructure. Document status + retry columns provide durable, resumable job tracking
2. **Text on filesystem, not in DB** — Keeps Postgres lean. Avoids bloating WAL/backups with large text. Simple to inspect/debug
3. **Sequential chunk processing** — Maximizes Claude prompt cache hits (all chunks from same document share cached context)
4. **Chunk-level status tracking** — Enables fine-grained resume without redoing expensive API calls
5. **Hybrid search with RRF** — Combines semantic understanding (vectors) with keyword precision (FTS), proven in hackernews prototype
6. **Failure = status + error IS NOT NULL** — No separate FAILED status. Current status tells you which stage failed

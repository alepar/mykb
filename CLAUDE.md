# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
just build              # go build ./...
just test               # go test ./...
just lint               # golangci-lint run ./...
just fmt                # gofmt -w .
just cli                # go build -o mykb ./cmd/mykb/
just proto              # regenerate gRPC code from proto/mykb/v1/kb.proto
just up / just down     # docker compose up -d / down
```

Run a single test:
```bash
go test ./internal/pipeline/ -run TestChunkMarkdown_HeadingSplits -v
```

Proto regeneration requires `protoc-gen-go` and `protoc-gen-go-grpc` in PATH (`~/go/bin`).

## Architecture

MyKB is a RAG knowledge base: ingest web pages, chunk them, embed with Voyage AI, store in multiple backends, and search with hybrid vector+FTS retrieval.

**Two binaries:**
- `cmd/mykb/` — CLI client (`mykb ingest <url>`, `mykb query <query>`). Deploys to client machines.
- `cmd/mykb-api/` — gRPC server. Runs in Docker alongside backends.

**Ingestion pipeline** (`internal/pipeline/`): URL → Crawl4AI (markdown) → recursive chunker → Voyage AI embeddings → dual-index (Qdrant + Meilisearch). Orchestrated by `internal/worker/` which manages document lifecycle and retry.

**Search flow** (`internal/search/`): Query → parallel fan-out to Qdrant (vector) and Meilisearch (FTS) → RRF fusion (`rrf.go`) → Voyage rerank (`rerank.go`) → RSE segment extraction (`rse.go`, default) or individual chunks (`--no-merge`).

**Storage** (`internal/storage/`): Four backends — PostgreSQL (metadata, retry state), Qdrant (vectors, int8 quantized), Meilisearch (full-text), filesystem (raw markdown, 2-level directory sharding).

**gRPC service** (`internal/server/`): Implements `proto/mykb/v1/kb.proto`. Generated code lives in `gen/mykb/v1/`.

## Key Design Decisions

- **Recursive chunker** splits at markdown heading hierarchy → paragraphs → sentences. Code fences and tables are protected (never split mid-block).
- **Contextualized embeddings** (Voyage `voyage-context-3`): chunks are sent together so the model sees sibling context, replacing the need for prepended headers.
- **RSE (Relevant Segment Extraction)**: merges adjacent high-scoring chunks into coherent passages using exponential decay scoring. Default mode; `no_merge` bypasses it.
- **CLI config**: `~/.mykb.conf` (TOML), with CLI flag overrides. Auto-detects `NO_COLOR` and non-TTY for agent/tool usage.

## Docker Compose Services

| Service | Host Port | Internal Port | Purpose |
|---------|-----------|---------------|---------|
| mykb | 9090 | 9090 | gRPC API server |
| postgres | 5433 | 5432 | Document/chunk metadata |
| qdrant | 6335 (gRPC), 6336 (HTTP) | 6334 (gRPC), 6333 (HTTP) | Vector search |
| meilisearch | 7701 | 7700 | Full-text search |
| crawl4ai | 11235 | 11235 | Web scraping |

**Database/index names:**

| Backend | Name | Primary Key | Notes |
|---------|------|-------------|-------|
| PostgreSQL | `documents` table | `id` (uuid) | Unique constraint on `url` |
| PostgreSQL | `chunks` table | `id` (uuid) | FK to `documents.id` (cascades) |
| Qdrant | `mykb` collection | chunk uuid | 2048-dim vectors (voyage-context-3), cosine, int8 quantization |
| Meilisearch | `mykb` index | `chunk_id` | FTS on `content`, filterable by `document_id`, `chunk_id`, `chunk_index` |

Requires `.env` file with `VOYAGE_API_KEY` and `MEILISEARCH_KEY`.

## Data Preservation

**IMPORTANT: Protect local database contents.** Data is stored in `~/.local/share/mykb/` (machine-local, not synced between machines). The PostgreSQL `documents` table records which URLs have been ingested — this metadata is very hard to recreate (requires manually rediscovering all original URLs). Never run `docker compose down -v` or otherwise destroy the Postgres volume. Avoid `DELETE FROM documents` unless explicitly asked.

Chunks metadata and search index data (Qdrant, Meilisearch) are less critical — they can be recreated by re-ingesting, which costs embedding API calls but is tolerable at small volumes.

## Clearing Test Data

```bash
curl -X DELETE 'http://localhost:6336/collections/mykb'
curl -X DELETE -H 'Authorization: Bearer mykb-dev-key' 'http://localhost:7701/indexes/mykb'
docker compose exec -T postgres psql -U mykb -d mykb -c "DELETE FROM chunks; DELETE FROM documents;"
docker compose restart mykb  # recreates collection/index on startup
```

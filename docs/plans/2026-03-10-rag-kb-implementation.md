# RAG Knowledge Base Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a RAG-based knowledge base with URL ingestion, contextual embeddings, and hybrid search, running as a docker-compose stack.

**Architecture:** Monolithic Go gRPC service orchestrating crawl→chunk→contextualize→embed→index pipeline. Postgres as job queue + metadata store. Qdrant for vectors, Meilisearch for FTS, filesystem for all text content.

**Tech Stack:** Go, gRPC/protobuf, Postgres, Qdrant, Meilisearch, Crawl4AI, Claude API (Haiku), Voyage AI (voyage-3-large + rerank-2)

**Reference codebases:**
- `~/AleCode/hackernews` — RRF fusion, Qdrant gRPC client, Meilisearch SDK, docker-compose patterns
- `~/AleCode/meilisearch-movies` — Voyage AI Go client (`austinfhunter/voyageai`), gRPC server setup, Meilisearch index config, proto definitions

---

### Task 1: Project scaffolding — go.mod, docker-compose, Dockerfile, Justfile, .env

**Files:**
- Create: `go.mod`
- Create: `docker-compose.yml`
- Create: `Dockerfile`
- Create: `Justfile`
- Create: `.env.example`
- Create: `.gitignore`

**Step 1: Initialize Go module**

```bash
cd /home/alepar/AleCode/mykb
go mod init mykb
```

**Step 2: Create docker-compose.yml**

```yaml
services:
  mykb:
    build: .
    ports:
      - "9090:9090"
    depends_on:
      - postgres
      - qdrant
      - meilisearch
      - crawl4ai
    env_file: .env
    environment:
      POSTGRES_DSN: postgres://mykb:mykb@postgres:5432/mykb?sslmode=disable
      QDRANT_GRPC_HOST: qdrant:6334
      MEILISEARCH_HOST: http://meilisearch:7700
      CRAWL4AI_URL: http://crawl4ai:11235
    volumes:
      - ./data/documents:/data/documents:Z

  postgres:
    image: postgres:17
    environment:
      POSTGRES_USER: mykb
      POSTGRES_PASSWORD: mykb
      POSTGRES_DB: mykb
    volumes:
      - ./data/postgres:/var/lib/postgresql/data:Z

  qdrant:
    image: qdrant/qdrant:v1.17.0
    volumes:
      - ./data/qdrant:/qdrant/storage:Z

  meilisearch:
    image: getmeili/meilisearch:v1.37.0
    environment:
      MEILI_MASTER_KEY: ${MEILISEARCH_KEY}
    volumes:
      - ./data/meili:/meili_data:Z

  crawl4ai:
    image: unclecode/crawl4ai:latest
```

**Step 3: Create Dockerfile**

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /mykb ./cmd/mykb

FROM alpine:3.21
COPY --from=builder /mykb /mykb
COPY migrations /migrations
EXPOSE 9090
CMD ["/mykb"]
```

**Step 4: Create Justfile**

```just
default:
    @just --list

up:
    docker compose up -d

down:
    docker compose down

build:
    go build ./...

lint:
    golangci-lint run ./...

fmt:
    gofmt -w .

test:
    go test ./...

proto:
    protoc --go_out=. --go-grpc_out=. proto/mykb/v1/kb.proto
```

**Step 5: Create .env.example**

```
ANTHROPIC_API_KEY=
VOYAGE_API_KEY=
MEILISEARCH_KEY=
```

**Step 6: Create .gitignore**

```
data/
.env
/mykb
```

**Step 7: Commit**

```bash
git add go.mod docker-compose.yml Dockerfile Justfile .env.example .gitignore
git commit -m "feat: project scaffolding"
```

---

### Task 2: Proto definition + code generation

**Files:**
- Create: `proto/mykb/v1/kb.proto`

**Step 1: Create proto file**

```protobuf
syntax = "proto3";
package mykb.v1;
option go_package = "mykb/gen/mykb/v1;mykbv1";

service KBService {
  rpc IngestURL(IngestURLRequest) returns (stream IngestProgress);
  rpc Query(QueryRequest) returns (QueryResponse);
  rpc ListDocuments(ListDocumentsRequest) returns (ListDocumentsResponse);
  rpc GetDocuments(GetDocumentsRequest) returns (GetDocumentsResponse);
  rpc DeleteDocument(DeleteDocumentRequest) returns (DeleteDocumentResponse);
}

message IngestURLRequest {
  string url = 1;
}

message IngestProgress {
  string document_id = 1;
  string status = 2;
  string message = 3;
  int32 chunks_total = 4;
  int32 chunks_processed = 5;
}

message QueryRequest {
  string query = 1;
  int32 top_k = 2;
  int32 vector_depth = 3;
  int32 fts_depth = 4;
  int32 rerank_depth = 5;
}

message QueryResponse {
  repeated QueryResult results = 1;
}

message QueryResult {
  string chunk_id = 1;
  string document_id = 2;
  int32 chunk_index = 3;
  float score = 4;
}

message Document {
  string id = 1;
  string url = 2;
  string title = 3;
  string status = 4;
  string error = 5;
  int32 chunk_count = 6;
  int64 created_at = 7;
  int64 crawled_at = 8;
  string content = 9;
}

message ListDocumentsRequest {
  int32 limit = 1;
  int32 offset = 2;
}

message ListDocumentsResponse {
  repeated Document documents = 1;
  int32 total = 2;
}

message GetDocumentsRequest {
  repeated string ids = 1;
  bool include_content = 2;
}

message GetDocumentsResponse {
  repeated Document documents = 1;
}

message DeleteDocumentRequest {
  string id = 1;
}

message DeleteDocumentResponse {}
```

**Step 2: Generate Go code**

```bash
mkdir -p gen/mykb/v1
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       proto/mykb/v1/kb.proto
```

Note: generated code lands in `gen/mykb/v1/` per the `go_package` option. If `protoc-gen-go` or `protoc-gen-go-grpc` aren't installed:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

**Step 3: Add protobuf/grpc deps**

```bash
go get google.golang.org/grpc
go get google.golang.org/protobuf
```

**Step 4: Commit**

```bash
git add proto/ gen/ go.mod go.sum
git commit -m "feat: add gRPC proto definition and generated code"
```

---

### Task 3: Config package

**Files:**
- Create: `internal/config/config.go`

**Step 1: Write config**

```go
package config

import "os"

type Config struct {
	PostgresDSN    string
	QdrantGRPCHost string
	MeilisearchHost string
	MeilisearchKey  string
	Crawl4AIURL     string
	AnthropicAPIKey string
	VoyageAPIKey    string
	GRPCPort        string
	DataDir         string

	// Model settings
	ClaudeModel          string
	VoyageEmbedModel     string
	VoyageEmbedDimension int
	VoyageRerankModel    string

	// Pipeline settings
	ChunkTargetTokens int
	ChunkMaxTokens    int
	EmbedBatchSize    int
	MaxRetries        int

	// Search defaults
	DefaultTopK        int
	DefaultVectorDepth int
	DefaultFTSDepth    int
	DefaultRerankDepth int
	RRFConstant        int
}

func Load() *Config {
	return &Config{
		PostgresDSN:     envOr("POSTGRES_DSN", "postgres://mykb:mykb@localhost:5432/mykb?sslmode=disable"),
		QdrantGRPCHost:  envOr("QDRANT_GRPC_HOST", "localhost:6334"),
		MeilisearchHost: envOr("MEILISEARCH_HOST", "http://localhost:7700"),
		MeilisearchKey:  os.Getenv("MEILISEARCH_KEY"),
		Crawl4AIURL:     envOr("CRAWL4AI_URL", "http://localhost:11235"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		VoyageAPIKey:    os.Getenv("VOYAGE_API_KEY"),
		GRPCPort:        envOr("GRPC_PORT", "9090"),
		DataDir:         envOr("DATA_DIR", "/data/documents"),

		ClaudeModel:          "claude-haiku-4-5-20251001",
		VoyageEmbedModel:     "voyage-3-large",
		VoyageEmbedDimension: 1024,
		VoyageRerankModel:    "rerank-2",

		ChunkTargetTokens: 800,
		ChunkMaxTokens:    1500,
		EmbedBatchSize:    128,
		MaxRetries:        5,

		DefaultTopK:        10,
		DefaultVectorDepth: 100,
		DefaultFTSDepth:    100,
		DefaultRerankDepth: 50,
		RRFConstant:        60,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

**Step 2: Commit**

```bash
git add internal/config/
git commit -m "feat: add config package with env-based configuration"
```

---

### Task 4: Postgres schema + migrations + storage client

**Files:**
- Create: `migrations/001_init.sql`
- Create: `internal/storage/postgres.go`
- Test: `internal/storage/postgres_test.go`

**Step 1: Create migration**

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

CREATE INDEX idx_documents_status ON documents(status);
CREATE INDEX idx_documents_next_retry ON documents(next_retry_at) WHERE error IS NOT NULL;

CREATE TABLE chunks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index     INT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(document_id, chunk_index)
);

CREATE INDEX idx_chunks_document_id ON chunks(document_id);
```

**Step 2: Write postgres storage client**

Use `pgx` as the Postgres driver (standard for Go):

```bash
go get github.com/jackc/pgx/v5
```

`internal/storage/postgres.go` — Implement:
- `NewPostgresStore(ctx, dsn) (*PostgresStore, error)` — connects, runs migrations
- `RunMigrations(ctx) error` — reads and executes `migrations/*.sql`
- `InsertDocument(ctx, url) (Document, error)` — returns created doc with UUID
- `UpdateDocumentStatus(ctx, id, status) error` — sets status + updated_at
- `SetDocumentError(ctx, id, errMsg) error` — sets error, increments retry_count, computes next_retry_at
- `ClearDocumentError(ctx, id) error` — clears error for retry
- `SetDocumentTitle(ctx, id, title) error`
- `SetDocumentChunkCount(ctx, id, count) error`
- `SetDocumentCrawledAt(ctx, id) error`
- `GetDocument(ctx, id) (Document, error)`
- `ListDocuments(ctx, limit, offset) ([]Document, total int, error)`
- `DeleteDocument(ctx, id) error` — cascading delete (chunks FK)
- `GetPendingDocuments(ctx) ([]Document, error)` — the resume query: `WHERE status != 'DONE' AND (error IS NULL OR (retry_count < max AND next_retry_at <= now()))`
- `InsertChunks(ctx, documentID, count) ([]Chunk, error)` — batch insert N chunks
- `GetChunksByDocument(ctx, documentID) ([]Chunk, error)`
- `UpdateChunkStatus(ctx, id, status) error`
- `GetChunksByDocumentAndStatus(ctx, documentID, status) ([]Chunk, error)`
- `DeleteChunksByDocument(ctx, documentID) error`

**Document struct:**

```go
type Document struct {
	ID         string
	URL        string
	Status     string
	Error      *string
	Title      *string
	ChunkCount *int
	RetryCount int
	NextRetryAt *time.Time
	CrawledAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
```

**Chunk struct:**

```go
type Chunk struct {
	ID          string
	DocumentID  string
	ChunkIndex  int
	Status      string
	CreatedAt   time.Time
}
```

**Step 3: Write tests**

`internal/storage/postgres_test.go` — integration tests that require a running Postgres (skip with build tag or env check). Test:
- InsertDocument + GetDocument round-trip
- Duplicate URL returns error
- UpdateDocumentStatus changes status
- SetDocumentError increments retry_count and sets next_retry_at
- GetPendingDocuments returns docs with correct filtering
- InsertChunks + GetChunksByDocument
- DeleteDocument cascades to chunks

Use `testcontainers-go` or check for `TEST_POSTGRES_DSN` env var.

```bash
go get github.com/testcontainers/testcontainers-go
go get github.com/testcontainers/testcontainers-go/modules/postgres
```

**Step 4: Run tests**

```bash
go test ./internal/storage/ -v -run TestPostgres
```

**Step 5: Commit**

```bash
git add migrations/ internal/storage/ go.mod go.sum
git commit -m "feat: postgres schema, migrations, and storage client"
```

---

### Task 5: Filesystem storage

**Files:**
- Create: `internal/storage/filesystem.go`
- Test: `internal/storage/filesystem_test.go`

**Step 1: Write failing tests**

Test:
- `WriteDocument(id, content)` creates `{id[0:2]}/{id[2:4]}/{id}.md`
- `ReadDocument(id)` reads it back
- `WriteChunkText(id, index, content)` creates `{id[0:2]}/{id[2:4]}/{id}.000t.md`
- `WriteChunkContext(id, index, content)` creates `{id[0:2]}/{id[2:4]}/{id}.000c.md`
- `ReadChunkText(id, index)` reads chunk text
- `ReadChunkContext(id, index)` reads chunk context
- `DeleteDocumentFiles(id)` removes all files for a document
- Index formatting: 0→`000`, 12→`012`, 999→`999`

**Step 2: Run tests to verify they fail**

```bash
go test ./internal/storage/ -v -run TestFilesystem
```

**Step 3: Implement**

`internal/storage/filesystem.go`:

```go
type FilesystemStore struct {
	baseDir string
}

func NewFilesystemStore(baseDir string) *FilesystemStore

// docDir returns "{baseDir}/{id[0:2]}/{id[2:4]}"
func (fs *FilesystemStore) docDir(id string) string

func (fs *FilesystemStore) WriteDocument(id string, content []byte) error
func (fs *FilesystemStore) ReadDocument(id string) ([]byte, error)
func (fs *FilesystemStore) WriteChunkText(id string, index int, content []byte) error
func (fs *FilesystemStore) WriteChunkContext(id string, index int, content []byte) error
func (fs *FilesystemStore) ReadChunkText(id string, index int) ([]byte, error)
func (fs *FilesystemStore) ReadChunkContext(id string, index int) ([]byte, error)
func (fs *FilesystemStore) DeleteDocumentFiles(id string) error
```

File naming: `{id}.{fmt.Sprintf("%03d", index)}t.md` and `...c.md`

**Step 4: Run tests**

```bash
go test ./internal/storage/ -v -run TestFilesystem
```

**Step 5: Commit**

```bash
git add internal/storage/filesystem.go internal/storage/filesystem_test.go
git commit -m "feat: filesystem storage for documents and chunks"
```

---

### Task 6: Qdrant storage client

**Files:**
- Create: `internal/storage/qdrant.go`
- Test: `internal/storage/qdrant_test.go`

**Step 1: Add dependency**

```bash
go get github.com/qdrant/go-client
```

**Step 2: Implement**

Reference: `~/AleCode/hackernews` Qdrant patterns (gRPC client, `pb.NewClient`, `pb.NewVectorsConfig`, int8 scalar quantization).

```go
type QdrantStore struct {
	client         *pb.Client
	collectionName string
}

func NewQdrantStore(host string, collectionName string) (*QdrantStore, error)
func (q *QdrantStore) EnsureCollection(ctx context.Context, dimension uint64) error
// Creates collection if not exists: cosine distance, int8 scalar quantization

func (q *QdrantStore) UpsertVectors(ctx context.Context, ids []string, vectors [][]float32, payloads []map[string]any) error
// payload contains document_id, chunk_index
// UUID string → use pb.NewIDUuid(id)

func (q *QdrantStore) Search(ctx context.Context, vector []float32, limit uint64) ([]SearchResult, error)
// Returns SearchResult{ID string, Score float32, Payload map[string]any}

func (q *QdrantStore) DeleteByDocumentID(ctx context.Context, documentID string) error
// Filter delete: payload match on document_id
```

Qdrant point IDs: use UUID directly via `pb.NewIDUuid(id)` (Qdrant supports UUID point IDs).

**Step 3: Write integration tests** (require running Qdrant, skip if unavailable)

Test: EnsureCollection → UpsertVectors → Search returns correct results → DeleteByDocumentID removes them.

**Step 4: Commit**

```bash
git add internal/storage/qdrant.go internal/storage/qdrant_test.go go.mod go.sum
git commit -m "feat: qdrant storage client"
```

---

### Task 7: Meilisearch storage client

**Files:**
- Create: `internal/storage/meilisearch.go`
- Test: `internal/storage/meilisearch_test.go`

**Step 1: Add dependency**

```bash
go get github.com/meilisearch/meilisearch-go
```

**Step 2: Implement**

Reference: `~/AleCode/hackernews` and `~/AleCode/meilisearch-movies` Meilisearch patterns.

```go
type MeilisearchStore struct {
	client    meilisearch.ServiceManager
	indexName string
}

func NewMeilisearchStore(host, apiKey, indexName string) (*MeilisearchStore, error)

func (m *MeilisearchStore) EnsureIndex(ctx context.Context) error
// Creates index if not exists
// Settings: searchable=["content"], filterable=["chunk_id","document_id","chunk_index"]
// Primary key: "chunk_id"

func (m *MeilisearchStore) IndexChunks(ctx context.Context, chunks []MeiliChunk) error
// MeiliChunk{ChunkID, DocumentID, ChunkIndex, Content string}
// Content = contextualized text (chunk_text + "\n\n" + context)
// Waits for task completion

func (m *MeilisearchStore) Search(ctx context.Context, query string, limit int64) ([]MeiliHit, error)
// MeiliHit{ChunkID, DocumentID, ChunkIndex, RankingScore float64}

func (m *MeilisearchStore) DeleteByDocumentID(ctx context.Context, documentID string) error
// Filter delete on document_id
```

**Step 3: Integration tests**

**Step 4: Commit**

```bash
git add internal/storage/meilisearch.go internal/storage/meilisearch_test.go go.mod go.sum
git commit -m "feat: meilisearch storage client"
```

---

### Task 8: Pipeline — Crawl4AI client

**Files:**
- Create: `internal/pipeline/crawl.go`
- Test: `internal/pipeline/crawl_test.go`

**Step 1: Implement**

Crawl4AI exposes a REST API. Reference: https://docs.crawl4ai.com/api/parameters/

```go
type Crawler struct {
	baseURL    string
	httpClient *http.Client
}

func NewCrawler(baseURL string) *Crawler

func (c *Crawler) Crawl(ctx context.Context, url string) (CrawlResult, error)
// POST to {baseURL}/crawl with JSON body
// Request: {"urls": [url], "word_count_threshold": 10}
// Response contains markdown content
// Returns CrawlResult{Markdown string, Title string}
```

Check Crawl4AI docs for the exact request/response format — it may vary by version. The container image `unclecode/crawl4ai:latest` exposes port 11235.

**Step 2: Write test** (mock HTTP server for unit test)

**Step 3: Commit**

```bash
git add internal/pipeline/crawl.go internal/pipeline/crawl_test.go
git commit -m "feat: crawl4ai client"
```

---

### Task 9: Pipeline — Markdown-aware chunking

**Files:**
- Create: `internal/pipeline/chunk.go`
- Test: `internal/pipeline/chunk_test.go`

**Step 1: Write failing tests**

Test cases:
- Short document (< target tokens) → single chunk
- Document with `##` headers → splits at headers
- Large section (> max tokens) → subdivided by paragraphs
- Code blocks are kept intact (not split mid-block)
- Empty document → no chunks
- Document with only `#` top-level header → treated as single section
- Chunk sizes stay within target/max token bounds

Token estimation: `len(text) / 4` (rough approximation, good enough).

**Step 2: Run tests to verify they fail**

```bash
go test ./internal/pipeline/ -v -run TestChunk
```

**Step 3: Implement**

```go
type ChunkOptions struct {
	TargetTokens int // default 800
	MaxTokens    int // default 1500
}

func ChunkMarkdown(content string, opts ChunkOptions) []string
```

Algorithm:
1. Split by `\n## ` (keeping the header with its section)
2. For each section: if estimated tokens <= MaxTokens, keep as one chunk
3. If section exceeds MaxTokens, split by double newlines (paragraphs)
4. Greedily accumulate paragraphs until approaching TargetTokens
5. Never split inside fenced code blocks (``` ... ```)

**Step 4: Run tests**

```bash
go test ./internal/pipeline/ -v -run TestChunk
```

**Step 5: Commit**

```bash
git add internal/pipeline/chunk.go internal/pipeline/chunk_test.go
git commit -m "feat: markdown-aware chunking"
```

---

### Task 10: Pipeline — Contextualize (Claude API)

**Files:**
- Create: `internal/pipeline/contextualize.go`
- Test: `internal/pipeline/contextualize_test.go`

**Step 1: Add Anthropic Go SDK**

```bash
go get github.com/anthropics/anthropic-sdk-go
```

**Step 2: Implement**

```go
type Contextualizer struct {
	client *anthropic.Client
	model  string
}

func NewContextualizer(apiKey, model string) *Contextualizer

func (c *Contextualizer) Contextualize(ctx context.Context, document string, chunk string) (string, error)
```

Following the contextual embeddings cookbook:
- Message with two content blocks
- First block: full document wrapped in `<document>...</document>` tags, with `cache_control: {"type": "ephemeral"}`
- Second block: chunk wrapped in `<chunk>...</chunk>` tags with the prompt "Please give a short succinct context to situate this chunk within the overall document for the purposes of improving search retrieval of the chunk. Answer only with the succinct context and nothing else."
- `temperature: 0.0`, `max_tokens: 256`

**Step 3: Write test** (mock API or use `ANTHROPIC_API_KEY` env for integration test)

**Step 4: Commit**

```bash
git add internal/pipeline/contextualize.go internal/pipeline/contextualize_test.go go.mod go.sum
git commit -m "feat: contextual embeddings via Claude API"
```

---

### Task 11: Pipeline — Embed (Voyage AI)

**Files:**
- Create: `internal/pipeline/embed.go`
- Test: `internal/pipeline/embed_test.go`

**Step 1: Add Voyage AI dependency**

Reference: `~/AleCode/meilisearch-movies/backend` uses `github.com/austinfhunter/voyageai`.

```bash
go get github.com/austinfhunter/voyageai
```

**Step 2: Implement**

```go
type Embedder struct {
	client *voyageai.Client
	model  string
}

func NewEmbedder(apiKey, model string) *Embedder

func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
// Batch embed with input_type="document"
// Process in batches of up to 128

func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error)
// Single embed with input_type="query"
```

Reference `~/AleCode/meilisearch-movies/backend/internal/voyage/embed.go` for client initialization patterns (NewDocumentEmbedder, NewQueryEmbedder).

**Step 3: Write test** (mock or integration)

**Step 4: Commit**

```bash
git add internal/pipeline/embed.go internal/pipeline/embed_test.go go.mod go.sum
git commit -m "feat: voyage AI embedding client"
```

---

### Task 12: Pipeline — Index (Qdrant + Meilisearch)

**Files:**
- Create: `internal/pipeline/index.go`
- Test: `internal/pipeline/index_test.go`

**Step 1: Implement**

```go
type Indexer struct {
	qdrant      *storage.QdrantStore
	meilisearch *storage.MeilisearchStore
}

func NewIndexer(qdrant *storage.QdrantStore, meili *storage.MeilisearchStore) *Indexer

func (idx *Indexer) IndexChunks(ctx context.Context, chunks []IndexableChunk) error
// IndexableChunk{ID, DocumentID string, ChunkIndex int, Vector []float32, ContextualizedText string}
// 1. Upsert vectors to Qdrant (with document_id, chunk_index payload)
// 2. Index to Meilisearch (chunk_id, document_id, chunk_index, contextualized content)
```

**Step 2: Test (integration)**

**Step 3: Commit**

```bash
git add internal/pipeline/index.go internal/pipeline/index_test.go
git commit -m "feat: qdrant + meilisearch indexing"
```

---

### Task 13: Search — RRF fusion

**Files:**
- Create: `internal/search/rrf.go`
- Test: `internal/search/rrf_test.go`

**Step 1: Write failing tests**

Adapt directly from `~/AleCode/hackernews/internal/fusion/rrf.go` but use string IDs instead of int64.

Test cases:
- Single list → scores are 1/(k+rank+1)
- Two overlapping lists → scores are summed
- Two disjoint lists → all items present
- topK limits output
- Empty lists → empty result

**Step 2: Run tests to verify they fail**

**Step 3: Implement**

```go
package search

type ScoredID struct {
	ID    string
	Score float64
}

func RRF(k int, topK int, lists ...[]ScoredID) []ScoredID
```

Same algorithm as hackernews but with string IDs.

**Step 4: Run tests**

**Step 5: Commit**

```bash
git add internal/search/
git commit -m "feat: reciprocal rank fusion"
```

---

### Task 14: Search — Reranker (Voyage AI)

**Files:**
- Create: `internal/search/rerank.go`
- Test: `internal/search/rerank_test.go`

**Step 1: Implement**

```go
type Reranker struct {
	client *voyageai.Client
	model  string
}

func NewReranker(apiKey, model string) *Reranker

func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]RerankResult, error)
// RerankResult{Index int, Score float64}
// Calls Voyage AI rerank API
// Returns sorted by score descending, limited to topK
```

Reference: `~/AleCode/meilisearch-movies/backend/internal/search/rerank.go` for Voyage rerank patterns.

**Step 2: Test (mock or integration)**

**Step 3: Commit**

```bash
git add internal/search/rerank.go internal/search/rerank_test.go
git commit -m "feat: voyage AI reranker"
```

---

### Task 15: Search — Hybrid search orchestration

**Files:**
- Create: `internal/search/search.go`
- Test: `internal/search/search_test.go`

**Step 1: Implement**

```go
type HybridSearcher struct {
	embedder    *pipeline.Embedder
	qdrant      *storage.QdrantStore
	meilisearch *storage.MeilisearchStore
	reranker    *Reranker
	fs          *storage.FilesystemStore
	cfg         *config.Config
}

func NewHybridSearcher(...) *HybridSearcher

type SearchParams struct {
	Query       string
	TopK        int
	VectorDepth int
	FTSDepth    int
	RerankDepth int
}

type SearchResult struct {
	ChunkID     string
	DocumentID  string
	ChunkIndex  int
	Score       float64
}

func (h *HybridSearcher) Search(ctx context.Context, params SearchParams) ([]SearchResult, error)
```

Algorithm:
1. **Parallel**: `go embedder.EmbedQuery(query)` + `go meilisearch.Search(query, ftsDepth)`
2. `qdrant.Search(queryVector, vectorDepth)`
3. Convert both to `[]ScoredID`, call `RRF(k, rerankDepth, qdrantList, meiliList)`
4. For top RRF candidates, read chunk text from filesystem for reranker input
5. `reranker.Rerank(query, chunkTexts, topK)`
6. Map rerank results back to SearchResult with chunk metadata

**Step 2: Test** (unit test with mocked dependencies)

**Step 3: Commit**

```bash
git add internal/search/search.go internal/search/search_test.go
git commit -m "feat: hybrid search orchestration"
```

---

### Task 16: Worker — Background ingestion pipeline

**Files:**
- Create: `internal/worker/worker.go`
- Test: `internal/worker/worker_test.go`

**Step 1: Implement**

```go
type Worker struct {
	pg          *storage.PostgresStore
	fs          *storage.FilesystemStore
	crawler     *pipeline.Crawler
	chunker     ChunkOptions // from config
	contextualizer *pipeline.Contextualizer
	embedder    *pipeline.Embedder
	indexer     *pipeline.Indexer
	cfg         *config.Config
	notify      chan string // receives document IDs to process
}

func NewWorker(...) *Worker

func (w *Worker) Start(ctx context.Context)
// 1. On startup: query GetPendingDocuments, process each
// 2. Listen on notify channel for new document IDs
// 3. Process one document at a time (sequential for prompt cache)

func (w *Worker) ProcessDocument(ctx context.Context, doc storage.Document, progress chan<- IngestProgress) error
// Runs through pipeline stages, updating status at each transition
// Sends progress updates to channel (for streaming to gRPC client)
// On error: calls SetDocumentError, returns

func (w *Worker) Notify(documentID string)
// Sends document ID to notify channel (non-blocking)
```

Pipeline per document:
1. `UpdateDocumentStatus(CRAWLING)` → `crawler.Crawl(url)` → `fs.WriteDocument` → `SetDocumentTitle` → `SetDocumentCrawledAt`
2. `UpdateDocumentStatus(CHUNKING)` → `fs.ReadDocument` → `ChunkMarkdown` → write chunk files → `InsertChunks` → `SetDocumentChunkCount`
3. `UpdateDocumentStatus(CONTEXTUALIZING)` → for each pending chunk: `contextualizer.Contextualize(doc, chunk)` → `fs.WriteChunkContext` → `UpdateChunkStatus(CONTEXTUALIZED)`
4. `UpdateDocumentStatus(EMBEDDING)` → collect contextualized chunks → `embedder.EmbedDocuments(batch)` → `UpdateChunkStatus(EMBEDDED)`
5. `UpdateDocumentStatus(INDEXING)` → `indexer.IndexChunks` → `UpdateChunkStatus(DONE)` → `UpdateDocumentStatus(DONE)`

Each stage checks current status for resumability (skip already-completed work).

**Step 2: Test** (unit test with mocked pipeline stages)

**Step 3: Commit**

```bash
git add internal/worker/
git commit -m "feat: background ingestion worker"
```

---

### Task 17: gRPC server — All handlers

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Step 1: Implement**

```go
type Server struct {
	mykbv1.UnimplementedKBServiceServer
	pg       *storage.PostgresStore
	fs       *storage.FilesystemStore
	searcher *search.HybridSearcher
	worker   *worker.Worker
	cfg      *config.Config
}

func NewServer(...) *Server
```

**IngestURL handler:**
- Insert document via `pg.InsertDocument(url)`
- If duplicate URL, return error
- Create progress channel, call `worker.Notify(docID)`
- Stream progress updates from channel to gRPC stream
- Worker sends updates via the channel as it processes

**Query handler:**
- Apply defaults for zero-value params from config
- Call `searcher.Search(params)`
- Map results to proto QueryResult

**ListDocuments handler:**
- Apply default limit (50) if zero
- Call `pg.ListDocuments`
- Map to proto

**GetDocuments handler:**
- Call `pg.GetDocument` for each ID
- If `include_content`, call `fs.ReadDocument` for each
- Map to proto

**DeleteDocument handler:**
- Get document from Postgres (need chunk IDs)
- Delete from Qdrant (by document_id filter)
- Delete from Meilisearch (by document_id filter)
- Delete from filesystem
- Delete from Postgres (cascades to chunks)

**Step 2: Test** (unit tests with mocks for storage/search)

**Step 3: Commit**

```bash
git add internal/server/
git commit -m "feat: gRPC server handlers"
```

---

### Task 18: Main entrypoint

**Files:**
- Create: `cmd/mykb/main.go`

**Step 1: Implement**

```go
package main

func main() {
	cfg := config.Load()

	// Connect storage backends
	pg := storage.NewPostgresStore(ctx, cfg.PostgresDSN)
	pg.RunMigrations(ctx)
	fs := storage.NewFilesystemStore(cfg.DataDir)
	qdrant := storage.NewQdrantStore(cfg.QdrantGRPCHost, "mykb")
	qdrant.EnsureCollection(ctx, uint64(cfg.VoyageEmbedDimension))
	meili := storage.NewMeilisearchStore(cfg.MeilisearchHost, cfg.MeilisearchKey, "mykb")
	meili.EnsureIndex(ctx)

	// Create pipeline components
	crawler := pipeline.NewCrawler(cfg.Crawl4AIURL)
	contextualizer := pipeline.NewContextualizer(cfg.AnthropicAPIKey, cfg.ClaudeModel)
	embedder := pipeline.NewEmbedder(cfg.VoyageAPIKey, cfg.VoyageEmbedModel)
	indexer := pipeline.NewIndexer(qdrant, meili)

	// Create search
	reranker := search.NewReranker(cfg.VoyageAPIKey, cfg.VoyageRerankModel)
	searcher := search.NewHybridSearcher(embedder, qdrant, meili, reranker, fs, cfg)

	// Start worker
	w := worker.NewWorker(pg, fs, crawler, contextualizer, embedder, indexer, cfg)
	go w.Start(ctx)

	// Start gRPC server
	srv := server.NewServer(pg, fs, searcher, w, cfg)
	lis, _ := net.Listen("tcp", ":"+cfg.GRPCPort)
	grpcServer := grpc.NewServer()
	mykbv1.RegisterKBServiceServer(grpcServer, srv)
	reflection.Register(grpcServer)

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		grpcServer.GracefulStop()
		cancel()
	}()

	grpcServer.Serve(lis)
}
```

**Step 2: Verify it compiles**

```bash
go build ./cmd/mykb/
```

**Step 3: Commit**

```bash
git add cmd/mykb/
git commit -m "feat: main entrypoint"
```

---

### Task 19: Integration test — Full pipeline

**Files:**
- Create: `tests/integration_test.go`

**Step 1: Write end-to-end test**

Requires `docker compose up`. Test:
1. Call `IngestURL` with a known URL (e.g., a simple static page)
2. Verify streaming progress completes with `DONE`
3. Call `ListDocuments` — verify document appears
4. Call `Query` with text from the page — verify results returned
5. Call `GetDocuments` with `include_content=true` — verify markdown
6. Call `DeleteDocument` — verify cleanup

Use `grpcurl` or Go gRPC client for testing.

**Step 2: Add to Justfile**

```just
integration-test:
    go test ./tests/ -v -tags=integration -timeout 120s
```

**Step 3: Commit**

```bash
git add tests/ Justfile
git commit -m "feat: integration tests"
```

---

## Task Dependency Order

```
Task 1 (scaffolding)
  → Task 2 (proto)
  → Task 3 (config)
  → Task 4 (postgres) ──────────────────────┐
  → Task 5 (filesystem) ────────────────────┤
  → Task 6 (qdrant) ────────────────────────┤
  → Task 7 (meilisearch) ───────────────────┤
  → Task 8 (crawl4ai) ─────────────────────┤
  → Task 9 (chunking) ─────────────────────┤
  → Task 10 (contextualize) ───────────────┤
  → Task 11 (embed) ───────────────────────┤
  → Task 12 (index) [needs 6,7] ──────────┤
  → Task 13 (RRF) ─────────────────────────┤
  → Task 14 (reranker) ────────────────────┤
  → Task 15 (search) [needs 6,7,11,13,14] ┤
  → Task 16 (worker) [needs 4-12] ────────┤
  → Task 17 (server) [needs 4,5,15,16] ───┤
  → Task 18 (main) [needs all] ───────────┘
  → Task 19 (integration test) [needs 18]
```

**Parallelizable groups:**
- Tasks 4-11 can be done in parallel (no interdependencies except Task 12 needs 6+7)
- Tasks 13-14 can be done in parallel
- Task 15 needs 6,7,11,13,14
- Task 16 needs 4-12
- Tasks 17-19 are sequential

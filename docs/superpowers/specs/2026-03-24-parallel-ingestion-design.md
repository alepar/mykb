# Parallel Ingestion Design

## Overview

Add parallel document ingestion to mykb-api: a worker pool (default 8 concurrent workers), adaptive rate limiting for the Voyage AI embedding API, proper Crawl4AI deployment tuning, and a new `IngestURLs` batch gRPC RPC with a corresponding `mykb import-urls` CLI command. Target: ingest 3,724 URLs in ~2-3 hours instead of ~15 hours.

## Worker Pool

**Current:** Single worker goroutine in `worker.Start()` processes documents sequentially from a buffered channel (capacity 64).

**Proposed:** Replace with a pool of N concurrent workers (configurable via `WORKER_CONCURRENCY` env var, default 8). Each worker independently pulls documents from the shared notify channel and runs the full pipeline (crawl → chunk → embed → index).

Parallelism is at the document level, not within a document. Each document's chunks are still embedded together in a single contextualized API call — this preserves embedding quality.

**Changes:**
- `internal/worker/worker.go`: `Start()` performs startup resume (pending documents) once, then launches N goroutines. Each goroutine reads from the shared notify channel and runs `processDocument()`. The startup resume must happen before spawning the pool to avoid multiple goroutines independently calling `GetPendingDocuments()` and double-processing the same documents.
- `internal/config/config.go`: New `WORKER_CONCURRENCY` env var, default 8.
- Increase notify channel capacity from 64 to 8192 to support large batch submissions without dropping. For the `IngestURLs` RPC, use blocking sends to ensure no URLs are lost (the current non-blocking send with drop-and-retry-on-restart is fine for individual `IngestURL` calls, but batch ingestion must guarantee delivery).

## Adaptive Rate Limiting (Voyage AI)

Port the `AdaptiveLimiter` from `~/AleCode/meilisearch-movies/backend/internal/ratelimit/adaptive.go` into mykb.

**New package:** `internal/ratelimit/adaptive.go`

**Configuration:**
- Starting rate: 0.4 tokens/sec (matches movies project — per-request token load is comparable since each mykb embed call sends one document's 8-15 chunks, similar to a movies batch of ~100 short strings)
- Floor rate: starting_rate / 16 (0.025 tokens/sec)
- Bucket size: 5 tokens
- EMA alpha: 0.3
- Safety margin: 0.9 (operate at 90% of learned ceiling)
- Probe multiplier: 1.1 (probe at 110% after 25 consecutive successes)

**Retry with exponential backoff** on embed calls:
- Max retries: 5
- Base delay: 4 seconds (sequence: 4s, 8s, 16s, 32s, 64s)
- On failure (error, empty response, size mismatch): call `limiter.ReportFailure()` → rate drops to ceiling × 0.9
- On success: call `limiter.ReportSuccess()` → may trigger upward probe

**Interaction with document-level retries:** Stage-level retries (embed, crawl) are internal to the pipeline call. If all 5 stage retries are exhausted, the error propagates up to the existing document-level retry mechanism in `handleError()` (which sets `retry_count`, `next_retry_at`, up to `MaxRetries=5`). The two retry layers are independent: stage retries handle transient API failures within a single attempt, document retries handle persistent failures across attempts.

**Integration:** The limiter is created in `cmd/mykb-api/main.go` at startup and passed into the pipeline. Each worker calls `limiter.Acquire()` before calling the Voyage API in `embed.go`.

All 8 workers share the single limiter. The limiter naturally serializes embedding at the learned rate across all workers. The parallelism benefit comes from overlapping crawling with embedding — while one worker waits on the Voyage rate limiter, others are crawling.

## Crawl4AI Retry and Deployment Tuning

**No adaptive rate limiter for Crawl4AI.** Crawl4AI manages its own concurrency internally via `MAX_CONCURRENT_TASKS` and a memory-adaptive dispatcher. The mykb worker pool simply sends concurrent requests and lets Crawl4AI handle scheduling.

**Retry with exponential backoff** on crawl calls:
- Max retries: 5
- Base delay: 4 seconds
- Retry on: HTTP errors, timeouts, "no available slots" responses, empty results
- No ceiling learning — just simple retry-or-fail

**K8s manifest changes** (`k8s/crawl4ai-deployment.yaml`):
- Bump resources: requests 1Gi/250m, limits 4Gi/1000m (official recommendation is 4Gi minimum)
- Add `/dev/shm` as 1Gi `emptyDir` with `medium: Memory` (Chromium requires shared memory, crashes with default 64MB)
- Add env var `MAX_CONCURRENT_TASKS=5`

## New `IngestURLs` gRPC RPC

**Proto changes** (`proto/mykb/v1/kb.proto`):

```protobuf
rpc IngestURLs(IngestURLsRequest) returns (stream IngestURLsProgress);

message IngestURLsRequest {
  repeated string urls = 1;
  bool force = 2;  // re-ingest even if URL already exists
}

message IngestURLsProgress {
  int32 current = 1;     // 1-indexed count of documents with progress
  int32 total = 2;       // total URLs submitted
  string url = 3;
  string stage = 4;      // "queued", "crawling", "chunking", "embedding", "indexing", "done", "error", "skipped"
  string error = 5;      // non-empty if stage == "error"
}
```

**Server implementation** (`internal/server/server.go`):
1. Receive URL list from `IngestURLsRequest`
2. For each URL: create document record in postgres (skip duplicates unless `force=true`, report "skipped" for duplicates)
3. Queue all documents into the worker pool's notify channel
4. Register a progress listener for this batch
5. Stream `IngestURLsProgress` messages as documents move through pipeline stages
6. Close stream when all documents are done/errored/skipped

**Progress reporting mechanism:**

The worker needs a way to report stage transitions back to the `IngestURLs` handler. Design:

- **Progress bus:** A `sync.Map` of `documentID → chan ProgressEvent` managed by the worker. When a handler wants to observe a document, it registers a channel. The worker sends stage events to the channel if one is registered. If no channel is registered (e.g., single `IngestURL` calls or documents with no listener), events are silently dropped.
- **Lifecycle:** The `IngestURLs` handler registers channels for all its document IDs before queuing them. When a document reaches a terminal state (done/error/skipped), the worker closes the channel and removes it from the map. The handler reads from all channels (via a goroutine per document or a multiplexing select) and forwards events to the gRPC stream.
- **Client disconnect:** If the gRPC stream context is cancelled (client disconnects), the handler stops reading from channels. The worker still processes documents to completion — progress events are dropped since nobody is reading. No leak: channels are cleaned up on terminal state regardless of whether the handler is reading.
- **`IngestURLsProgress.current`:** Server-computed running counter of documents that have reached a terminal state (done, error, or skipped). Increments only on terminal events, not on intermediate stage transitions.

**gRPC keepalive:** The stream for a large batch (3,724 URLs) may stay open for 2-3 hours. The stream is not idle — progress messages flow continuously — so default gRPC keepalive settings should be sufficient. If issues arise, configure server-side keepalive (e.g., `MaxConnectionAge`) and client-side keepalive ping interval. The Traefik ingress uses `proxy_read_timeout` defaults which should be fine since the stream is active.

## New `mykb import-urls` CLI Command

**Usage:** `mykb import-urls --file urls.txt --host mykb.k3s:80`

**Behavior:**
1. Read URLs from file (one per line, skip empty lines and comments starting with `#`)
2. Call `IngestURLs` RPC with the full URL list
3. Display live progress: `[142/3724] embedding https://... | done: 141 | errors: 3`
4. On completion, print summary: total ingested, skipped (duplicates), errors
5. Return non-zero exit code if any URLs failed

**Flags:**
- `--file` / `-f`: path to URL file (required)
- `--host`: gRPC server address (from config, default `localhost:9090`)
- `--force`: re-ingest existing URLs
- `--quiet`: suppress progress output (for scripting)

## Changes Summary

| Area | Files | Change |
|------|-------|--------|
| Worker pool | `internal/worker/worker.go`, `internal/config/config.go` | N concurrent workers, `WORKER_CONCURRENCY` config |
| Rate limiter | `internal/ratelimit/adaptive.go` (new) | Port from movies project |
| Embed retries | `internal/pipeline/embed.go` | Exponential backoff + limiter integration |
| Crawl retries | `internal/pipeline/crawl.go` | Exponential backoff on transient errors |
| Proto | `proto/mykb/v1/kb.proto`, `gen/mykb/v1/` | New `IngestURLs` RPC + messages |
| Server | `internal/server/server.go` | Implement `IngestURLs`, progress bus |
| CLI | `cmd/mykb/main.go` | New `import-urls` command |
| K8s | `k8s/crawl4ai-deployment.yaml` | Bump resources, add /dev/shm, MAX_CONCURRENT_TASKS |
| Startup | `cmd/mykb-api/main.go` | Create limiter, pass to pipeline, configure worker concurrency |

## What Does Not Change

- Pipeline stages (crawl → chunk → embed → index)
- Contextualized embedding strategy (all chunks of a document together)
- Existing `IngestURL` single-URL RPC (stays as-is)
- Search flow
- Storage backends (Postgres, Qdrant, Meilisearch, filesystem)

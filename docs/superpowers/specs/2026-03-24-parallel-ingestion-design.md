# Parallel Ingestion Design

## Overview

Add parallel document ingestion to mykb-api: a worker pool (default 8 concurrent workers), adaptive rate limiting for the Voyage AI embedding API, proper Crawl4AI deployment tuning, and a new `IngestURLs` batch gRPC RPC with a corresponding `mykb import-urls` CLI command. Target: ingest 3,724 URLs in ~2-3 hours instead of ~15 hours.

## Worker Pool

**Current:** Single worker goroutine in `worker.Start()` processes documents sequentially from a buffered channel (capacity 64).

**Proposed:** Replace with a pool of N concurrent workers (configurable via `WORKER_CONCURRENCY` env var, default 8). Each worker independently pulls documents from the shared notify channel and runs the full pipeline (crawl → chunk → embed → index).

Parallelism is at the document level, not within a document. Each document's chunks are still embedded together in a single contextualized API call — this preserves embedding quality.

**Changes:**
- `internal/worker/worker.go`: `Start()` launches N goroutines instead of 1. Each runs the existing `processDocument()` loop.
- `internal/config/config.go`: New `WORKER_CONCURRENCY` env var, default 8.
- The existing notify channel and retry logic remain unchanged.

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
- The worker currently reports progress via a callback. Extend this so the worker publishes stage transitions to a shared progress bus (channel or callback registry keyed by document ID).
- The `IngestURLs` handler subscribes to progress for its batch of document IDs and forwards updates to the gRPC stream.

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

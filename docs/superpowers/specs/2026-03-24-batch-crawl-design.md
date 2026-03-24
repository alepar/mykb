# Batch Crawl Pipeline Design

## Problem

Crawl4ai serializes individual HTTP requests through a single browser via its pool `asyncio.Lock()`. Our 8 workers each send separate `/crawl` requests, which queue up and process one at a time. The parallelism we built doesn't reach crawl4ai.

## Solution

Replace per-document worker goroutines with a batch coordinator that sends multiple URLs in a single `/crawl` request. Crawl4ai's internal `arun_many` dispatcher parallelizes within a single request, bypassing the pool LOCK.

## Batch Coordinator Flow

```
1 coordinator goroutine:
  pull up to N docs from channel (N = WORKER_CONCURRENCY, default 8)
  → separate Reddit URLs (crawl individually) from regular URLs (CrawlBatch)
  → chunk each result individually
  → fan out N goroutines for embed+index (rate limited per-doc)
  → wait for all to complete
  → repeat
```

### Pulling from channel

The coordinator drains up to `WORKER_CONCURRENCY` items from the notify channel. It uses a short timeout (100ms) after the first item to avoid blocking forever when fewer items are queued — if at least 1 item is available, it collects what's there and proceeds.

### Batch Crawl

New `CrawlBatch(ctx context.Context, urls []string) (map[string]CrawlResult, map[string]error)` method on `Crawler`. Sends all URLs in one `/crawl` POST request — the API already accepts `urls: [url1, url2, ...]`. Returns successful results keyed by URL and per-URL errors keyed by URL.

**HTTP timeout:** `CrawlBatch` uses a longer timeout than single-URL crawl: `2 minutes * len(urls)` capped at 10 minutes. A batch of 8 URLs with `MAX_CONCURRENT_TASKS=5` needs ~2 rounds internally.

**Retry semantics:** Retry with backoff wraps the HTTP POST only (transport-level failures: connection errors, timeouts). If the response arrives with per-URL success/failure indicators, the batch is NOT retried — each URL's result is routed individually (success → continue pipeline, failure → `handleError`).

**Reddit URLs:** `CrawlBatch` only handles non-Reddit URLs. The coordinator filters Reddit URLs out before calling `CrawlBatch` and crawls them individually via `Crawl()` (which dispatches to `crawlReddit`). Both paths run concurrently — Reddit crawls in parallel goroutines alongside the batch crawl.

### Chunk + Embed + Index Fan-out

After batch crawl returns, the coordinator:
1. For each successful crawl result: saves markdown to filesystem, sets title and `crawled_at` in postgres, updates status to CRAWLING, sends progress. Then runs `doChunk`. These are extracted from the current `doCrawl` into a new `saveCrawlResult` helper, since `doCrawl` currently also calls `w.crawler.Crawl()` which is now handled by the batch.
2. Launches a goroutine per document for `doEmbed` + `doIndex`. Each goroutine calls `limiter.Acquire()` before embedding. The rate limiter serializes embed calls across the batch.
3. Waits via `sync.WaitGroup` for all goroutines to complete. Each goroutine handles its own errors via `handleError` and sends terminal progress (ERROR or DONE) before the WaitGroup is decremented.
4. Closes progress channels for all documents in the batch.
5. Pulls next batch.

`ProcessDocument` is kept unchanged for the startup resume path (which processes pending documents individually before the batch coordinator starts).

### Error Handling

- **Partial crawl failure:** Some URLs succeed, others fail in the batch response. Successful documents proceed through chunk/embed/index. Failed documents get `handleError()` (existing document-level retry with `next_retry_at`).
- **Total crawl failure:** HTTP POST fails entirely (crawl4ai down, timeout). Each document in the batch gets `handleError()`. Coordinator proceeds to next batch.
- **Embed/index failure:** Handled per-document by the goroutine, same as current behavior.

### Progress Reporting

Per-document progress channels still work. Each document's channel receives updates as its individual stages complete. Known limitation: during the batch crawl phase, no per-document progress is sent until the batch returns. All documents in a batch report CRAWLING near-simultaneously when the batch completes.

Progress channels are closed by the coordinator after all goroutines finish, not by `ProcessDocument`. This means the `IngestURLs` handler (which reads channels sequentially) will work correctly — it blocks on document N's channel until closed, then moves to N+1. Progress updates for later documents may be dropped by `sendProgress`'s non-blocking send if their channel buffer (32) fills up. This is acceptable since progress is informational.

## Changes

| File | Change |
|------|--------|
| `internal/pipeline/crawl.go` | Add `CrawlBatch()` method. Sends multiple URLs in one POST, returns per-URL results/errors. Longer timeout for batches. |
| `internal/worker/worker.go` | Replace N-goroutine pool with batch coordinator. Extract `saveCrawlResult` from `doCrawl`. Keep `ProcessDocument` for startup resume. |

## What Does Not Change

- Proto, server, CLI, k8s manifests
- `embed.go` — per-document contextualized embedding stays the same
- Config — `WORKER_CONCURRENCY` controls batch size (already exists)
- Rate limiter — still gates embed calls
- `IngestURLs` handler
- Existing `IngestURL` single-URL RPC
- `ProcessDocument` — still used for startup resume of pending documents

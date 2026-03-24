# Batch Crawl Pipeline Design

## Problem

Crawl4ai serializes individual HTTP requests through a single browser via its pool `asyncio.Lock()`. Our 8 workers each send separate `/crawl` requests, which queue up and process one at a time. The parallelism we built doesn't reach crawl4ai.

## Solution

Replace per-document worker goroutines with a batch coordinator that sends multiple URLs in a single `/crawl` request. Crawl4ai's internal `arun_many` dispatcher parallelizes within a single request, bypassing the pool LOCK.

## Batch Coordinator Flow

```
1 coordinator goroutine:
  pull up to N docs from channel (N = WORKER_CONCURRENCY, default 8)
  → CrawlBatch(N urls) in one /crawl POST
  → chunk each result individually
  → fan out N goroutines for embed+index (rate limited per-doc)
  → wait for all to complete
  → repeat
```

### Pulling from channel

The coordinator drains up to `WORKER_CONCURRENCY` items from the notify channel. It uses a short timeout (100ms) after the first item to avoid blocking forever when fewer items are queued — if at least 1 item is available, it collects what's there and proceeds.

### Batch Crawl

New `CrawlBatch(ctx context.Context, urls []string) (map[string]CrawlResult, []CrawlError, error)` method on `Crawler`. Sends all URLs in one `/crawl` POST request — the API already accepts `urls: [url1, url2, ...]`. Returns successful results keyed by URL and a list of per-URL errors. A total failure (HTTP error, timeout) returns an error. Retry with backoff wraps the entire batch call, not individual URLs.

### Chunk + Embed + Index Fan-out

After batch crawl returns, the coordinator:
1. For each successful crawl result: runs `doCrawlPost` (save markdown, set title, update status) and `doChunk` synchronously
2. Launches a goroutine per document for `doEmbed` + `doIndex`. Each goroutine calls `limiter.Acquire()` before embedding. The rate limiter serializes embed calls across the batch.
3. Waits via `sync.WaitGroup` for all goroutines to complete
4. Pulls next batch

### Error Handling

- **Partial crawl failure:** Some URLs succeed, others fail in the batch. Successful documents proceed through chunk/embed/index. Failed documents get `handleError()` (existing document-level retry with `next_retry_at`).
- **Total crawl failure:** All URLs in the batch fail (e.g., crawl4ai down). Each document gets `handleError()`. Coordinator proceeds to next batch.
- **Embed/index failure:** Handled per-document by the goroutine, same as current behavior.

### Progress Reporting

Per-document progress channels still work. Each document's channel receives updates:
- `CRAWLING` after batch crawl returns for that document
- `CHUNKING`, `EMBEDDING`, `INDEXING`, `DONE` as the document moves through stages
- `ERROR` if any stage fails

## Changes

| File | Change |
|------|--------|
| `internal/pipeline/crawl.go` | Add `CrawlBatch()` method that sends multiple URLs in one POST. Returns `map[string]CrawlResult` + `[]CrawlError`. Retry wraps the whole batch. |
| `internal/worker/worker.go` | Replace N-goroutine pool in `Start()` with batch coordinator. Pull N items, batch crawl, chunk, fan out embed+index, repeat. |

## What Does Not Change

- Proto, server, CLI, k8s manifests
- `embed.go` — per-document contextualized embedding stays the same
- Config — `WORKER_CONCURRENCY` controls batch size (already exists)
- Rate limiter — still gates embed calls
- `IngestURLs` handler and progress reporting
- Existing `IngestURL` single-URL RPC

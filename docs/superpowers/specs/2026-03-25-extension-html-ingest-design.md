# Firefox Extension HTML Ingest Design

## Problem

The Firefox extension currently sends only the URL to the API. The server then uses Crawl4AI to fetch the page, which frequently fails on bot-protected sites (Cloudflare, DataDome, anti-bot JS challenges). The browser has already rendered the page successfully — we should send that rendered HTML instead.

## Solution

The extension captures `document.documentElement.outerHTML` from the active tab and sends it alongside the URL. The server writes the HTML to a prefetch file, and the crawler uses Crawl4AI's `raw:<html>` mode to convert it to markdown without re-fetching.

## Extension Changes (`extension/background.js`)

On toolbar click:
1. Inject a content script into the active tab that returns `document.documentElement.outerHTML`
2. POST to `/api/ingest` with `{"url": tab.url, "html": "<outerHTML>"}`

The extension already has the `activeTab` permission which allows injecting content scripts into the active tab.

## API Changes (`internal/server/http.go`)

Add `HTML string` field to `ingestRequest`:
```go
type ingestRequest struct {
    URL   string `json:"url"`
    HTML  string `json:"html"`
    Force bool   `json:"force"`
}
```

After inserting the document and before calling `w.Notify()`, if `html` is non-empty, write it to the filesystem as a prefetch file:
```go
if req.HTML != "" {
    fs.WritePrefetchHTML(doc.ID, []byte(req.HTML))
}
```

The HTTP handler needs access to the filesystem store. Update `NewHTTPHandler` to accept it, or add `fs` to the `pgForHTTP` interface context.

## Filesystem Changes (`internal/storage/filesystem.go`)

Add three methods:
- `WritePrefetchHTML(docID string, html []byte) error` — writes `{shard}/{docID}.prefetch.html`
- `ReadPrefetchHTML(docID string) ([]byte, error)` — reads the prefetch file
- `HasPrefetchHTML(docID string) bool` — checks if prefetch file exists
- `DeletePrefetchHTML(docID string)` — removes the prefetch file (best-effort, no error return)

Uses the same 2-level directory sharding as existing document storage.

## Crawler Changes (`internal/pipeline/crawl.go`)

Modify `crawlOnce` (the method that sends one URL to Crawl4AI):
- Before building the crawl request, check if a prefetch file exists via `fs.HasPrefetchHTML(docID)`
- If it exists, read the HTML and send `raw:<html>` as the URL in the crawl request instead of the actual URL
- After successful crawl, delete the prefetch file via `fs.DeletePrefetchHTML(docID)`

This means the `Crawler` needs access to the filesystem store and the document ID. Currently `crawlOnce` only takes a URL string. Options:
- Add a `CrawlWithHTML(ctx, url, html string)` method that uses `raw:<html>` directly
- The worker calls `CrawlWithHTML` if prefetch HTML exists, otherwise calls `Crawl` as before

The cleaner approach: add `CrawlWithHTML` to `Crawler`. The worker's `doCrawl` and `saveCrawlResult` check for prefetch HTML and choose the right method.

### Worker integration

In the batch coordinator's `processBatch`:
- After loading documents from postgres, check for prefetch HTML for each doc
- For docs with prefetch HTML: call `crawler.CrawlWithHTML(ctx, url, html)` individually (not via batch, since `CrawlBatch` sends URLs to crawl4ai)
- For docs without prefetch HTML: batch crawl as normal

In `ProcessDocument` (used for startup resume and single `IngestURL`):
- `doCrawl` checks for prefetch HTML, uses `CrawlWithHTML` if available

## Data Flow

```
Browser renders page → Extension captures outerHTML
  → POST /api/ingest {url, html}
  → Server writes prefetch file, inserts document, notifies worker
  → Worker loads document, finds prefetch file
  → Sends raw:<html> to Crawl4AI (no URL fetch, just markdown conversion)
  → Crawl4AI returns fit_markdown + raw_markdown
  → Normal pipeline: chunk → embed → index
  → Prefetch file deleted
```

## What Changes

| File | Change |
|------|--------|
| `extension/background.js` | Capture outerHTML via content script, send in request |
| `internal/server/http.go` | Accept `html` field, write prefetch file, pass `fs` to handler |
| `internal/storage/filesystem.go` | Add prefetch HTML read/write/delete methods |
| `internal/pipeline/crawl.go` | Add `CrawlWithHTML` method using `raw:` prefix |
| `internal/worker/worker.go` | Check for prefetch HTML in `doCrawl` and `processBatch` |
| `cmd/mykb-api/main.go` | Pass `fs` to `NewHTTPHandler` |

## What Does Not Change

- Proto, ConnectRPC service, CLI
- Chunking, embedding, indexing pipeline
- K8s manifests
- Batch crawling for URL-only ingestion
- Web UI ingest page (still sends URL only)

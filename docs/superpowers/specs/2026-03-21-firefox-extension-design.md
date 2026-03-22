# Firefox Ingest Extension Design

## Problem

Users want to ingest the current web page into mykb with a single click from Firefox, on both desktop and mobile.

## Solution

Two parts:
1. Add an HTTP API to the mykb-api server on a separate port (9091) for ingestion and status polling
2. A Firefox WebExtension that calls this API and shows progress via toolbar badge

## HTTP API (Server Side)

Separate HTTP server on port 9091 alongside the existing gRPC server on port 9090. No multiplexing needed — simplest approach.

### Endpoints

**`POST /api/ingest`**
- Body: `{"url": "...", "force": false}`
- Response: `{"id": "<document_id>"}`
- 409 Conflict if URL already ingested (returns existing document ID)
- Calls `pg.InsertDocument` then `w.Notify(doc.ID)` (non-blocking, returns immediately)
- Sets `Access-Control-Allow-Origin: *` header

**`GET /api/ingest/{id}`**
- Response: `{"id": "...", "status": "PENDING|CRAWLING|CHUNKING|EMBEDDING|INDEXING|DONE", "error": "..." | null}`
- Reads from `pg.GetDocument(id)` — status and error are separate fields
- Sets `Access-Control-Allow-Origin: *` header

### Server Changes

| File | Change |
|------|--------|
| `cmd/mykb-api/main.go` | Start HTTP server on :9091 alongside gRPC on :9090 |
| `internal/server/http.go` | New file: HTTP handlers for POST and GET endpoints |
| `docker-compose.yml` | Expose port 9091 for HTTP API |

HTTP handlers use the existing worker `Notify` (not `NotifyWithProgress`) and Postgres status lookup. No new dependencies needed — uses `net/http` and `encoding/json`.

## Firefox Extension

WebExtension (Manifest V2 for Firefox Android compatibility) that sends the current tab's URL for ingestion.

### UX Flow

1. User clicks toolbar icon
2. Background script POSTs to `/api/ingest` with current tab URL
3. Badge turns yellow with "..." text
4. Background script polls `GET /api/ingest/{id}` every 2 seconds
5. Badge updates with status abbreviation on each poll
6. On DONE: badge turns green briefly (3s), then clears
7. On error (non-null error field): badge turns red with "!" text
8. On 409 (already ingested): badge shows blue "dup" briefly, then clears
9. Stop polling after 5 minutes (timeout safety)

Badge-only status display (no popup) — works on both desktop and mobile Firefox. Badge rendering may be limited on some Android Firefox versions — graceful degradation (ingestion still works, just no visual feedback).

### Configuration

Options page with server address field, defaults to `http://localhost:9091`. Stored in `browser.storage.local`.

### Permissions

- `activeTab` — read current tab URL on click
- `storage` — persist server address setting
- `<all_urls>` — allow fetch to configurable server address

### File Structure

```
extension/
  manifest.json
  background.js
  options.html
  options.js
  icons/
    icon-48.png
    icon-96.png
```

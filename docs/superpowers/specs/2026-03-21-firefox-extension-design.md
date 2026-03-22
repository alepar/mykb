# Firefox Ingest Extension Design

## Problem

Users want to ingest the current web page into mykb with a single click from Firefox, on both desktop and mobile.

## Solution

Two parts:
1. Add an HTTP API to the mykb-api server alongside the existing gRPC, for ingestion and status polling
2. A Firefox WebExtension that calls this API and shows progress via toolbar badge

## HTTP API (Server Side)

Add HTTP endpoints on the same port (9090) using `cmux` to multiplex gRPC and HTTP on one listener.

### Endpoints

**`POST /api/ingest`**
- Body: `{"url": "...", "force": false}`
- Response: `{"id": "<document_id>"}`
- Starts ingestion asynchronously, returns immediately

**`GET /api/ingest/<id>`**
- Response: `{"id": "...", "status": "CRAWLING|CHUNKING|EMBEDDING|DONE|ERROR", "message": "..."}`
- Returns current ingestion status for polling

### Server Changes

| File | Change |
|------|--------|
| `cmd/mykb-api/main.go` | Add `cmux` listener, start HTTP server alongside gRPC |
| `internal/server/http.go` | New file: HTTP handlers for POST and GET endpoints |
| `go.mod` | Add `github.com/soheilhy/cmux` |

HTTP handlers call the same internal worker/pipeline as the gRPC `IngestURL` method. Status lookup uses the existing PostgreSQL `documents` table status column.

## Firefox Extension

WebExtension (Manifest V2 for Firefox Android compatibility) that sends the current tab's URL for ingestion.

### UX Flow

1. User clicks toolbar icon
2. Background script POSTs to `/api/ingest` with current tab URL
3. Badge turns yellow with "..." text
4. Background script polls `GET /api/ingest/<id>` every 2 seconds
5. Badge updates with status abbreviation on each poll
6. On DONE: badge turns green briefly, then clears
7. On ERROR: badge turns red with "!" text

Badge-only status display (no popup) — works identically on desktop and mobile Firefox.

### Configuration

Options page with server address field, defaults to `http://localhost:9090`. Stored in `browser.storage.local`.

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

### Manifest V2

Manifest V2 chosen over V3 because Firefox Android has better V2 support. Required permissions: `activeTab`, `storage`.

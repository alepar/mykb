# Status Page Column Redesign

## Problem

The status page table wastes space with a separate URL column. The new step+state fields from the document state machine aren't visible in the UI.

## Changes

### Proto

Add to `Document` message in `proto/mykb/v1/kb.proto`:

```protobuf
string step = 11;
string state = 12;
```

Regenerate with `just proto`.

### Server

In `documentToProto` (`internal/server/server.go`), set:

```go
d.Step = doc.Step
d.State = doc.State
```

### Frontend

Update `Document` interface in `frontend/src/api.ts` — add `step` and `state` string fields.

Update table in `frontend/src/pages/StatusPage.tsx`:

| Column | Content |
|--------|---------|
| Title | Truncated to ~50 chars, linked to URL, full text in native `title` tooltip |
| Step | CRAWLING, CHUNKING, EMBEDDING, INDEXING, DONE |
| State | QUEUED, PROCESSING, COMPLETED, FAILED, ABANDONED |
| Error | Truncated with tooltip, only shown when non-empty |
| Updated | Relative time ("3min ago", "1hr ago", "yesterday") |

Remove the separate URL and Created columns.

## What Does Not Change

- Proto field tags 1-10 unchanged
- `status` field (tag 4) still populated via `DisplayStatus()` for backward compatibility
- StatusCounts summary bar at top of page
- 10-second auto-refresh

# Firefox Synced Tabs Import Design

## Problem

The user has Firefox on Android phones with hundreds of tabs (including inactive/archived ones). Running a Go binary on Android isn't practical without root. We need to extract these mobile tabs for ingestion into mykb.

## Solution

Firefox Sync already replicates all device tabs (including inactive ones) to `synced-tabs.db` in the desktop Firefox profile. Enhance `import-tabs` to read this SQLite database alongside the existing `recovery.jsonlz4` session extraction. No phone access needed — the data is already on the desktop.

## Data Source

`synced-tabs.db` is an SQLite database in each Firefox profile directory. The `tabs` table has one row per synced device. Each row's `record` column contains JSON:

```json
{
  "id": "device-guid",
  "clientName": "Firefox on Google Pixel 6a",
  "tabs": [
    {"title": "Page Title", "urlHistory": ["https://..."], "lastUsed": 1710000000, "inactive": false}
  ]
}
```

Extract `urlHistory[0]` as URL and `title` as title for each tab across all devices.

## Changes

### `internal/tabs/firefox.go`

Add `readSyncedTabs(profilePath string) ([]Tab, error)`:
- Opens `<profilePath>/synced-tabs.db` with `database/sql` + `modernc.org/sqlite`
- Queries `SELECT record FROM tabs`
- Parses JSON records, extracts tabs from all devices
- Returns `[]Tab` slice

Update `DiscoverTabs()`:
- Call `readSyncedTabs()` for each profile alongside `readSessionTabs()`
- Dedup by URL across both sources (synced tabs may overlap with local session tabs)

### `go.mod`

Add `modernc.org/sqlite` — pure Go SQLite driver, no CGo. Works on all platforms without a C compiler.

### No changes needed

- `filter.go` — same filtering applies
- `picker.go` — source-agnostic, shows all tabs
- `cmd/mykb/main.go` — no changes, `DiscoverTabs()` API unchanged

# Firefox Tab Import Design

## Problem

The user has Firefox profiles across multiple machines, each with hundreds of open tabs used as "save for later" bookmarks. These URLs need to be extracted and added to `urls.txt` for later ingestion into mykb.

## Solution

New `mykb import-tabs` subcommand that auto-discovers Firefox profiles on the current machine, extracts tab URLs and titles from session data, presents a TUI picker for user confirmation, and appends selected URLs to `urls.txt`.

## Command Interface

```
mykb import-tabs [--urls-file FILE]
```

- `--urls-file` defaults to `urls.txt` in the current directory
- Auto-detects Firefox profiles (Linux standard, Flatpak, Snap, macOS)
- Reads session data from each profile
- Filters obvious non-content URLs
- Shows TUI picker for remaining URLs
- Deduplicates against existing `urls.txt` entries
- Appends selected URLs to `urls.txt`

## Firefox Profile Discovery

Scans these paths:

| Platform | Path |
|----------|------|
| Linux (standard) | `~/.mozilla/firefox/` |
| Linux (Flatpak) | `~/.var/app/org.mozilla.firefox/config/mozilla/firefox/` |
| Linux (Snap) | `~/snap/firefox/common/.mozilla/firefox/` |
| macOS | `~/Library/Application Support/Firefox/Profiles/` |

For each location, parse `profiles.ini` to find profile directories, then read `sessionstore-backups/recovery.jsonlz4` (live session if Firefox running) or `previous.jsonlz4` (last session if not).

### mozLz4 Decompression

Firefox uses a custom LZ4 variant:
- 8-byte magic: `mozLz40\0`
- 4-byte little-endian: decompressed size
- Remaining bytes: LZ4 block-compressed data

Use `github.com/pierrec/lz4/v4` for decompression.

### Session JSON Structure

```json
{
  "windows": [{
    "tabs": [{
      "entries": [{"url": "...", "title": "..."}]
    }]
  }]
}
```

Each tab's last entry (most recent navigation) provides the current URL and title.

## URL Filtering

Automatically skip:
- Browser internal: `about:`, `chrome://`, `moz-extension://`, `resource://`
- Local: `localhost`, `127.0.0.1`, `::1`, `0.0.0.0`
- Known non-content app UIs: `claude.ai/chat/`, `platform.claude.com`
- Already in `urls.txt` (exact match dedup)

Everything else goes to the TUI picker.

## TUI Picker

Bubbletea-based checkbox list, separate from the existing query TUI:

```
Import Firefox Tabs (3 profiles, 247 URLs found, 31 filtered, 18 duplicates)

[x] LWN.net - Security features in Firefox 148          https://lwn.net/SubscriberLink/...
[x] Envoy Proxy Contributing Guide                      https://github.com/envoyproxy/...
[ ] Syncthing FAQ                                       https://docs.syncthing.net/users/faq...
[x] Hacker News - Show HN: ...                          https://news.ycombinator.com/item?id=...

Space: toggle  a: select all  n: deselect all  Enter: confirm  q: cancel
```

- All URLs pre-selected by default (opt-out)
- `Space` toggles, `j/k`/arrows navigate
- `a` selects all, `n` deselects all
- `Enter` appends selected to `urls.txt`, prints count
- `q` cancels with no changes
- Title from session JSON `title` field (no network fetch)

## File Structure

| File | Purpose |
|------|---------|
| `internal/tabs/firefox.go` | Profile discovery, mozLz4 decompression, session JSON parsing |
| `internal/tabs/filter.go` | URL filtering rules |
| `internal/tabs/picker.go` | Bubbletea TUI model for checkbox list |
| `cmd/mykb/main.go` | Add `import-tabs` subcommand dispatch |
| `go.mod` / `go.sum` | Add `github.com/pierrec/lz4/v4` |

`internal/tabs/` is self-contained — no dependency on `internal/tui/`. Depends on bubbletea/lipgloss (already in go.mod) and the new lz4 library.

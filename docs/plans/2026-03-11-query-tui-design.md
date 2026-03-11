# Query Results TUI Design

## Goal

Replace the current scrolling text output of `mykb query` with a two-pane TUI for browsing search results, so merged segments (which can be full pages) are easy to navigate.

## Layout

```
┌─ Sidebar (30 cols) ─┬─ Main Pane ──────────────────────┐
│ /filter...           │ Document Title                    │
│                      │ https://example.com/doc           │
│ ▸ #1 {0.92} ex.com  │ Chunks 3-5/8                      │
│   Title of Doc One   │ ─────────────────────────────     │
│                      │                                   │
│   #2 {0.87} go.dev   │ ## Working with maps              │
│   Go Maps Blog       │                                   │
│                      │ Go provides a familiar syntax...  │
│   #3 {0.71} docs.rs  │                                   │
│   Rust Collections   │ ```go                             │
│                      │ m["route"] = 66                   │
│                      │ ```                               │
│                      │                                   │
│                      │ /search term          [2/5]       │
└──────────────────────┴───────────────────────────────────┘
```

## Components

### Sidebar

Each entry: rank, score, domain (extracted from URL), title. Active entry highlighted. Filterable with `/` — non-matches hidden, first visible result auto-selected.

### Main Pane

Header: document title, URL, chunk position (e.g. `3-5/8`). Body: full markdown rendered via glamour in a scrollable viewport. Searchable with `/` — jumps to matches (`n`/`N` for next/prev), match count shown.

## Key Bindings

| Key | Action |
|-----|--------|
| `Tab` | Switch active pane (sidebar / main) |
| `/` | Activate search/filter in current pane |
| `Enter` | Confirm search/filter |
| `Esc` | Cancel search, clear filter |
| `n` / `N` | Next/prev search match (main pane) |
| `j`/`k` or arrows | Navigate sidebar / scroll main |
| `q` | Quit |

## Architecture

Single Bubble Tea model with two panes and inline search state. No async DAO, no screen/modal stack, no navigation — this is a read-only viewer for pre-fetched data.

### Data flow

1. `cmd/mykb/main.go` fetches query results + document metadata via gRPC (existing code)
2. If TTY and no `NO_COLOR`: constructs TUI model with results, runs `tea.NewProgram`
3. If not TTY: falls back to plain text output (improved formatting, see below)

### Package structure

```
internal/tui/
├── model.go      # root model, Update, View, key handling
├── sidebar.go    # sidebar rendering + filter logic
├── mainpane.go   # viewport wrapper, search, header rendering
└── style.go      # lipgloss styles
```

### Dependencies

- `bubbletea` — framework
- `bubbles/viewport` — scrollable main pane
- `bubbles/textinput` — search/filter input
- `glamour` — markdown rendering (already present)
- `lipgloss` — styling (already present)

## Plain Text Output (non-TTY)

When not a TTY, output uses markdown-friendly formatting for clear segment separation:

```
# #1 {0.92} Document Title  3-5/8
https://example.com/doc

[rendered markdown body]

---

# #2 {0.87} Another Doc  2/12
https://other.com/page

[rendered markdown body]

---
```

## Files Changed

- New: `internal/tui/model.go`, `sidebar.go`, `mainpane.go`, `style.go`
- Modify: `cmd/mykb/main.go` — TUI launch when TTY, improved plain text formatting

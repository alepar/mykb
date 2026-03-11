# Query Results TUI Design

## Goal

Replace the current scrolling text output of `mykb query` with a two-pane TUI for browsing search results, so merged segments (which can be full pages) are easy to navigate.

## Layout

```
┌─ Sidebar (30 cols) ─┬─ Main Pane ──────────────────────┐
│ Matches              │ Document Title                    │
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
│ /filter        3/10  │ /search term   match 2/5      42% │
└──────────────────────┴───────────────────────────────────┘
```

## Components

### Sidebar

"Matches" header at top, highlighted when sidebar has focus. Each entry: rank, score, domain (extracted from URL), title. Active entry has uniform gray background across all styled components. Filterable with `/` — non-matches hidden, first visible result auto-selected. Bottom bar shows filter input while typing, or `/filterterm` with `N/M` match count after confirming.

### Main Pane

Pinned header (stays visible while scrolling): document title (highlighted when main pane has focus), URL, chunk position (e.g. `3-5/8`), horizontal divider. Body: full markdown rendered via glamour in a scrollable viewport. Searchable with `/` — live highlighting (yellow background) as user types, jumps to first match. `n`/`N` for next/prev match. Bottom bar: left side shows search input or match info, right side always shows scroll position (`Top`/`Bot`/`42%`).

## Focus Indication

- Active pane has blue border, inactive has gray
- Sidebar "Matches" header: light blue background when focused, plain when not
- Main pane document title: light blue background when focused, plain when not

## Key Bindings

| Key | Action |
|-----|--------|
| `Tab` | Switch active pane (sidebar / main) |
| `/` | Activate search/filter in current pane |
| `Enter` | Confirm search/filter (keeps highlights, exits input mode) |
| `Esc` | In input mode: cancel and clear. In normal mode: clear existing search/filter |
| `n` / `N` | Next/prev search match (main pane) |
| `j`/`k` or arrows | Navigate sidebar / scroll main |
| `q` | Quit |
| `ctrl+c` | Force quit |

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
├── mainpane.go   # viewport wrapper, search, header rendering, highlight
└── style.go      # lipgloss styles
```

### Dependencies

- `bubbletea` — framework
- `bubbles/viewport` — scrollable main pane
- `bubbles/textinput` — search/filter input
- `glamour` — markdown rendering (already present)
- `lipgloss` — styling (already present)

### Search highlighting

`highlightSearch` walks each line, strips ANSI to find case-insensitive match positions, then splices highlighted text into the raw line at correct byte offsets (accounting for ANSI escape sequences). Applied live as user types, cleared on Esc.

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

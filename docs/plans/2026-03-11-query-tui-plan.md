# Query TUI Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the plain-text query output with a two-pane Bubble Tea TUI for browsing search results interactively.

**Architecture:** Single Bubble Tea model with a sidebar (result list with filtering) and a main pane (scrollable rendered markdown with search). Pre-fetched data passed in at creation — no async operations. Falls back to improved plain text when not a TTY.

**Tech Stack:** bubbletea, bubbles/viewport, bubbles/textinput, glamour, lipgloss

---

### Task 1: Add Bubble Tea dependencies

**Files:**
- Modify: `go.mod`

**Step 1: Add bubbletea and bubbles dependencies**

Run:
```bash
go get github.com/charmbracelet/bubbletea@latest github.com/charmbracelet/bubbles@latest && go mod tidy
```

**Step 2: Verify build still passes**

Run: `go build ./cmd/... ./internal/... ./gen/...`
Expected: No errors

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add bubbletea and bubbles"
```

---

### Task 2: TUI data types and styles

**Files:**
- Create: `internal/tui/style.go`
- Create: `internal/tui/model.go`

**Context:** The TUI receives pre-fetched data. We need a `ResultItem` struct that combines query result fields with document metadata (title, URL, chunk count) so the TUI doesn't depend on proto types. The `Model` struct holds all state.

**Step 1: Create `internal/tui/style.go`**

```go
package tui

import "github.com/charmbracelet/lipgloss"

var (
	SidebarWidth = 30

	// Sidebar styles
	sidebarStyle       = lipgloss.NewStyle().Width(SidebarWidth).BorderRight(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("8"))
	sidebarActiveStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	sidebarItemStyle   = lipgloss.NewStyle()
	rankStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	scoreStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	domainStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	titleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	// Main pane styles
	headerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	headerURLStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	headerChunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	searchStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	matchCountStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)
```

**Step 2: Create `internal/tui/model.go` with types and constructor**

```go
package tui

import (
	"fmt"
	"net/url"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
)

// ResultItem holds pre-fetched data for one search result.
type ResultItem struct {
	Rank          int
	Score         float32
	Title         string
	URL           string
	ChunkIndex    int
	ChunkIndexEnd int
	ChunkCount    int
	Text          string // raw markdown
}

// Domain extracts the hostname from the URL.
func (r ResultItem) Domain() string {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return r.URL
	}
	return u.Host
}

// ChunkPosition returns e.g. "3-5/8" or "4/8".
func (r ResultItem) ChunkPosition() string {
	if r.ChunkCount <= 0 {
		return ""
	}
	if r.ChunkIndexEnd > r.ChunkIndex+1 {
		return fmt.Sprintf("%d-%d/%d", r.ChunkIndex+1, r.ChunkIndexEnd, r.ChunkCount)
	}
	return fmt.Sprintf("%d/%d", r.ChunkIndex+1, r.ChunkCount)
}

type pane int

const (
	paneSidebar pane = iota
	paneMain
)

// Model is the root Bubble Tea model.
type Model struct {
	items   []ResultItem
	width   int
	height  int
	active  pane

	// Sidebar state
	selected       int   // index into filteredIdx
	filteredIdx    []int // indices into items that match filter
	sidebarFilter  textinput.Model
	sidebarSearch  bool // true when filter input is active

	// Main pane state
	viewport      viewport.Model
	renderedTexts []string // pre-rendered markdown per item
	mainSearch    textinput.Model
	mainSearching bool
	searchMatches []int // line numbers of matches in current viewport content
	searchCursor  int   // index into searchMatches
}

// New creates a TUI model from pre-fetched results.
func New(items []ResultItem) Model {
	sf := textinput.New()
	sf.Prompt = "/"
	sf.CharLimit = 100

	ms := textinput.New()
	ms.Prompt = "/"
	ms.CharLimit = 100

	m := Model{
		items:         items,
		active:        paneSidebar,
		sidebarFilter: sf,
		mainSearch:    ms,
	}

	// Initially all items visible
	m.filteredIdx = make([]int, len(items))
	for i := range items {
		m.filteredIdx[i] = i
	}

	return m
}
```

**Step 3: Verify it compiles**

Run: `go build ./internal/tui/...`
Expected: No errors (note: `Init`, `Update`, `View` not yet implemented — that's fine, no `main` references this yet)

**Step 4: Commit**

```bash
git add internal/tui/style.go internal/tui/model.go
git commit -m "feat(tui): add data types and styles"
```

---

### Task 3: Sidebar rendering and filtering

**Files:**
- Create: `internal/tui/sidebar.go`
- Create: `internal/tui/sidebar_test.go`

**Context:** The sidebar shows a list of results. Each entry is two lines: line 1 = `#N {score} domain`, line 2 = `  title`. The active entry is highlighted. When filtering, non-matching entries are hidden and the first visible result is auto-selected. Filter matches against domain and title (case-insensitive substring).

**Step 1: Write the test file `internal/tui/sidebar_test.go`**

```go
package tui

import "testing"

func testItems() []ResultItem {
	return []ResultItem{
		{Rank: 1, Score: 0.92, Title: "Go Maps Blog", URL: "https://go.dev/blog/maps", ChunkIndex: 2, ChunkIndexEnd: 5, ChunkCount: 8, Text: "some text"},
		{Rank: 2, Score: 0.87, Title: "Rust Collections", URL: "https://docs.rs/collections", ChunkIndex: 0, ChunkIndexEnd: 0, ChunkCount: 12, Text: "other text"},
		{Rank: 3, Score: 0.71, Title: "Python Dicts", URL: "https://docs.python.org/dicts", ChunkIndex: 3, ChunkIndexEnd: 0, ChunkCount: 5, Text: "dict text"},
	}
}

func TestResultItemDomain(t *testing.T) {
	r := ResultItem{URL: "https://go.dev/blog/maps"}
	if got := r.Domain(); got != "go.dev" {
		t.Errorf("Domain() = %q, want %q", got, "go.dev")
	}
}

func TestResultItemChunkPosition(t *testing.T) {
	tests := []struct {
		name string
		item ResultItem
		want string
	}{
		{"range", ResultItem{ChunkIndex: 2, ChunkIndexEnd: 5, ChunkCount: 8}, "3-5/8"},
		{"single", ResultItem{ChunkIndex: 3, ChunkIndexEnd: 0, ChunkCount: 5}, "4/5"},
		{"no count", ResultItem{ChunkIndex: 0, ChunkCount: 0}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.ChunkPosition(); got != tt.want {
				t.Errorf("ChunkPosition() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterItems(t *testing.T) {
	items := testItems()

	tests := []struct {
		name   string
		filter string
		want   int // expected number of matches
	}{
		{"empty filter shows all", "", 3},
		{"match domain", "go.dev", 1},
		{"match title", "rust", 1},
		{"case insensitive", "PYTHON", 1},
		{"no match", "zzzzz", 0},
		{"partial match", "doc", 2}, // docs.rs and docs.python.org
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterItems(items, tt.filter)
			if len(got) != tt.want {
				t.Errorf("filterItems(%q) returned %d items, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestRenderSidebarEntry(t *testing.T) {
	item := testItems()[0]
	got := renderSidebarEntry(item, 28, false)
	if got == "" {
		t.Error("renderSidebarEntry returned empty string")
	}
	// Should contain rank, score, domain
	if !containsAll(got, "#1", "0.92", "go.dev") {
		t.Errorf("renderSidebarEntry missing expected content: %q", got)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -v`
Expected: FAIL — `filterItems` and `renderSidebarEntry` not defined

**Step 3: Implement `internal/tui/sidebar.go`**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// filterItems returns indices of items matching the filter (case-insensitive substring of domain or title).
func filterItems(items []ResultItem, filter string) []int {
	if filter == "" {
		idx := make([]int, len(items))
		for i := range items {
			idx[i] = i
		}
		return idx
	}
	lower := strings.ToLower(filter)
	var idx []int
	for i, item := range items {
		if strings.Contains(strings.ToLower(item.Domain()), lower) ||
			strings.Contains(strings.ToLower(item.Title), lower) {
			idx = append(idx, i)
		}
	}
	return idx
}

// renderSidebarEntry renders a single sidebar entry (two lines).
// Line 1: #N {score} domain
// Line 2:   title (truncated to width)
func renderSidebarEntry(item ResultItem, width int, active bool) string {
	line1 := fmt.Sprintf("%s %s %s",
		rankStyle.Render(fmt.Sprintf("#%d", item.Rank)),
		scoreStyle.Render(fmt.Sprintf("{%.2f}", item.Score)),
		domainStyle.Render(item.Domain()),
	)

	title := item.Title
	if lipgloss.Width(title) > width {
		// Truncate title to fit
		runes := []rune(title)
		for lipgloss.Width(string(runes)) > width-1 {
			runes = runes[:len(runes)-1]
		}
		title = string(runes) + "…"
	}
	line2 := "  " + titleStyle.Render(title)

	entry := line1 + "\n" + line2

	if active {
		return sidebarActiveStyle.Width(width).Render(entry)
	}
	return sidebarItemStyle.Width(width).Render(entry)
}

// renderSidebar renders the full sidebar view.
func renderSidebar(m *Model) string {
	innerWidth := SidebarWidth - 2 // account for border
	if innerWidth < 10 {
		innerWidth = 10
	}

	var b strings.Builder

	// Filter input at top (always visible when searching, otherwise blank line)
	if m.sidebarSearch {
		b.WriteString(m.sidebarFilter.View())
	}
	b.WriteString("\n")

	// Result entries
	availableHeight := m.height - 2 // filter line + newline
	if availableHeight < 1 {
		availableHeight = 1
	}

	// Each entry is 2 lines + 1 blank line separator = 3 lines
	linesUsed := 0
	for i, idx := range m.filteredIdx {
		if linesUsed+2 > availableHeight {
			break
		}
		item := m.items[idx]
		active := i == m.selected
		b.WriteString(renderSidebarEntry(item, innerWidth, active))
		b.WriteString("\n")
		linesUsed += 3
	}

	return sidebarStyle.Height(m.height).Render(b.String())
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_test.go
git commit -m "feat(tui): sidebar rendering and filtering"
```

---

### Task 4: Main pane rendering and search

**Files:**
- Create: `internal/tui/mainpane.go`
- Create: `internal/tui/mainpane_test.go`

**Context:** The main pane has a 3-line header (title, URL, chunk position) and a scrollable body of glamour-rendered markdown. Search with `/` finds text in the rendered output (stripped of ANSI) and jumps to match lines. The viewport from `bubbles/viewport` handles scrolling.

**Step 1: Write test file `internal/tui/mainpane_test.go`**

```go
package tui

import "testing"

func TestRenderHeader(t *testing.T) {
	item := ResultItem{
		Rank: 1, Score: 0.92, Title: "Go Maps Blog",
		URL: "https://go.dev/blog/maps",
		ChunkIndex: 2, ChunkIndexEnd: 5, ChunkCount: 8,
	}
	got := renderHeader(item, 60)
	if got == "" {
		t.Error("renderHeader returned empty")
	}
}

func TestFindMatches(t *testing.T) {
	content := "line one\nline two has foo\nline three\nfoo again here\nlast line"
	matches := findMatches(content, "foo")
	if len(matches) != 2 {
		t.Errorf("findMatches returned %d matches, want 2", len(matches))
	}
	if matches[0] != 1 {
		t.Errorf("first match at line %d, want 1", matches[0])
	}
	if matches[1] != 3 {
		t.Errorf("second match at line %d, want 3", matches[1])
	}
}

func TestFindMatchesCaseInsensitive(t *testing.T) {
	content := "Hello World\nhello world\nno match"
	matches := findMatches(content, "HELLO")
	if len(matches) != 2 {
		t.Errorf("findMatches returned %d matches, want 2", len(matches))
	}
}

func TestFindMatchesEmpty(t *testing.T) {
	matches := findMatches("some content", "")
	if len(matches) != 0 {
		t.Errorf("findMatches with empty query returned %d matches, want 0", len(matches))
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -v -run TestRenderHeader`
Expected: FAIL — `renderHeader` not defined

**Step 3: Implement `internal/tui/mainpane.go`**

```go
package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// renderHeader renders the 3-line header for a result item.
func renderHeader(item ResultItem, width int) string {
	var b strings.Builder

	// Line 1: Title
	b.WriteString(headerTitleStyle.Render(item.Title))
	b.WriteString("\n")

	// Line 2: URL
	if item.URL != "" {
		b.WriteString(headerURLStyle.Render(item.URL))
	}
	b.WriteString("\n")

	// Line 3: Chunk position
	if pos := item.ChunkPosition(); pos != "" {
		b.WriteString(headerChunkStyle.Render("Chunks " + pos))
	}
	b.WriteString("\n")

	// Divider
	b.WriteString(headerChunkStyle.Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	return b.String()
}

// renderMarkdown renders markdown text via glamour for a given width.
func renderMarkdown(text string, width int) string {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return rendered
}

// ansiRegex strips ANSI escape sequences for plain-text search.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// findMatches returns line indices (0-based) where the search term appears (case-insensitive).
// Searches the ANSI-stripped version of content.
func findMatches(content, search string) []int {
	if search == "" {
		return nil
	}
	lower := strings.ToLower(search)
	lines := strings.Split(content, "\n")
	var matches []int
	for i, line := range lines {
		plain := ansiRegex.ReplaceAllString(line, "")
		if strings.Contains(strings.ToLower(plain), lower) {
			matches = append(matches, i)
		}
	}
	return matches
}

// renderMainPane renders the full main pane: header + viewport + optional search bar.
func renderMainPane(m *Model) string {
	mainWidth := m.width - SidebarWidth - 2
	if mainWidth < 20 {
		mainWidth = 20
	}

	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	// Search bar at bottom
	if m.mainSearching {
		bar := m.mainSearch.View()
		if len(m.searchMatches) > 0 {
			bar += " " + matchCountStyle.Render(fmt.Sprintf("[%d/%d]", m.searchCursor+1, len(m.searchMatches)))
		}
		b.WriteString(bar)
	}

	return lipgloss.NewStyle().Width(mainWidth).Render(b.String())
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/tui/mainpane.go internal/tui/mainpane_test.go
git commit -m "feat(tui): main pane rendering and search"
```

---

### Task 5: Model Init, Update, View — core TUI loop

**Files:**
- Modify: `internal/tui/model.go`

**Context:** This is the core Bubble Tea loop. `Init()` pre-renders all markdown and sets up the viewport. `Update()` handles key routing between panes. `View()` composites sidebar + main pane side by side using `lipgloss.JoinHorizontal`.

**Key routing logic:**
- `ctrl+c`: always quit
- `Tab`: switch active pane
- When sidebar is active: `j`/`k`/arrows navigate, `/` starts filter, `Enter` confirms filter, `Esc` clears filter, `q` quits
- When main pane is active: `j`/`k`/arrows scroll viewport, `/` starts search, `Enter` confirms search, `n`/`N` next/prev match, `Esc` clears search, `q` quits

**Step 1: Add Init, Update, View to `internal/tui/model.go`**

Append to the existing file:

```go
func (m Model) Init() tea.Cmd {
	return tea.WindowSize()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateViewport()
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}
	left := renderSidebar(&m)
	right := renderMainPane(&m)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global keys
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	// Search/filter input mode
	if m.sidebarSearch && m.active == paneSidebar {
		return m.handleSidebarSearch(msg)
	}
	if m.mainSearching && m.active == paneMain {
		return m.handleMainSearch(msg)
	}

	// Normal mode
	switch key {
	case "q":
		return m, tea.Quit
	case "tab":
		if m.active == paneSidebar {
			m.active = paneMain
		} else {
			m.active = paneSidebar
		}
		return m, nil
	}

	if m.active == paneSidebar {
		return m.handleSidebarKey(msg)
	}
	return m.handleMainKey(msg)
}

func (m Model) handleSidebarKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.selected < len(m.filteredIdx)-1 {
			m.selected++
			m.syncMainPane()
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
			m.syncMainPane()
		}
	case "/":
		m.sidebarSearch = true
		m.sidebarFilter.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m Model) handleSidebarSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.sidebarSearch = false
		m.sidebarFilter.Blur()
		m.applyFilter()
		return m, nil
	case "esc":
		m.sidebarSearch = false
		m.sidebarFilter.Blur()
		m.sidebarFilter.SetValue("")
		m.applyFilter()
		return m, nil
	}
	var cmd tea.Cmd
	m.sidebarFilter, cmd = m.sidebarFilter.Update(msg)
	// Live filter as user types
	m.applyFilter()
	return m, cmd
}

func (m *Model) applyFilter() {
	m.filteredIdx = filterItems(m.items, m.sidebarFilter.Value())
	m.selected = 0
	m.syncMainPane()
}

func (m Model) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		m.mainSearching = true
		m.mainSearch.Focus()
		return m, textinput.Blink
	case "n":
		if len(m.searchMatches) > 0 {
			m.searchCursor = (m.searchCursor + 1) % len(m.searchMatches)
			m.viewport.GotoLineNumber(m.searchMatches[m.searchCursor])
		}
		return m, nil
	case "N":
		if len(m.searchMatches) > 0 {
			m.searchCursor = (m.searchCursor - 1 + len(m.searchMatches)) % len(m.searchMatches)
			m.viewport.GotoLineNumber(m.searchMatches[m.searchCursor])
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m Model) handleMainSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.mainSearching = false
		m.mainSearch.Blur()
		m.searchMatches = findMatches(m.viewport.View(), m.mainSearch.Value())
		m.searchCursor = 0
		if len(m.searchMatches) > 0 {
			m.viewport.GotoLineNumber(m.searchMatches[0])
		}
		return m, nil
	case "esc":
		m.mainSearching = false
		m.mainSearch.Blur()
		m.mainSearch.SetValue("")
		m.searchMatches = nil
		m.searchCursor = 0
		return m, nil
	}
	var cmd tea.Cmd
	m.mainSearch, cmd = m.mainSearch.Update(msg)
	return m, cmd
}

func (m *Model) syncMainPane() {
	if len(m.filteredIdx) == 0 {
		m.viewport.SetContent("No results match filter.")
		return
	}
	idx := m.filteredIdx[m.selected]
	if idx < len(m.renderedTexts) {
		mainWidth := m.width - SidebarWidth - 2
		if mainWidth < 20 {
			mainWidth = 20
		}
		content := renderHeader(m.items[idx], mainWidth) + m.renderedTexts[idx]
		m.viewport.SetContent(content)
		m.viewport.GotoTop()
	}
	// Clear search state when switching results
	m.searchMatches = nil
	m.searchCursor = 0
}

func (m *Model) updateViewport() {
	mainWidth := m.width - SidebarWidth - 2
	if mainWidth < 20 {
		mainWidth = 20
	}

	// Pre-render all markdown texts
	m.renderedTexts = make([]string, len(m.items))
	for i, item := range m.items {
		m.renderedTexts[i] = renderMarkdown(item.Text, mainWidth)
	}

	// Set viewport dimensions (leave room for search bar)
	vpHeight := m.height - 1
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.viewport = viewport.New(mainWidth, vpHeight)
	m.viewport.Style = lipgloss.NewStyle()

	m.syncMainPane()
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/tui/...`
Expected: No errors

**Step 3: Run existing tests still pass**

Run: `go test ./internal/tui/ -v`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/tui/model.go
git commit -m "feat(tui): core Update/View loop with key handling"
```

---

### Task 6: Wire TUI into CLI and improve plain text output

**Files:**
- Modify: `cmd/mykb/main.go`

**Context:** The `runQuery` function currently prints results directly. We need to:
1. Build `[]tui.ResultItem` from query response + document metadata
2. If TTY and no `NO_COLOR` → launch TUI
3. Otherwise → print improved plain text (h1 header + `---` separators)

The existing `runQuery` function handles flag parsing, gRPC calls, and document metadata fetching. We keep all of that, but replace the output section (lines 234-284 of current code) with the TUI launch / plain text branch.

**Step 1: Modify `cmd/mykb/main.go`**

Replace the output section of `runQuery` (everything after the `docMap` construction through end of function) with:

```go
	// Build TUI result items
	results := resp.Results
	if !*noMerge {
		results = deduplicateByDocument(results)
	}

	items := make([]tui.ResultItem, len(results))
	for i, r := range results {
		doc := docMap[r.DocumentId]
		item := tui.ResultItem{
			Rank:          i + 1,
			Score:         r.Score,
			Title:         r.DocumentId,
			ChunkIndex:    int(r.ChunkIndex),
			ChunkIndexEnd: int(r.ChunkIndexEnd),
			Text:          r.Text,
		}
		if doc != nil {
			if doc.Title != "" {
				item.Title = doc.Title
			}
			item.URL = doc.Url
			item.ChunkCount = int(doc.ChunkCount)
		}
		items[i] = item
	}

	// Decide: TUI or plain text
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	_, noColor := os.LookupEnv("NO_COLOR")

	if isTTY && !noColor {
		p := tea.NewProgram(tui.New(items), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	} else {
		printPlainResults(items, getTerminalWidth())
	}
```

Add this function and the necessary import:

```go
func printPlainResults(items []tui.ResultItem, termWidth int) {
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(termWidth),
	)

	for i, item := range items {
		// H1 header: # #1 {0.92} Title  3-5/8
		header := fmt.Sprintf("# #%d {%.3f} %s", item.Rank, item.Score, item.Title)
		if pos := item.ChunkPosition(); pos != "" {
			header += "  " + pos
		}
		fmt.Println(header)

		if item.URL != "" {
			fmt.Println(item.URL)
		}
		fmt.Println()

		if item.Text != "" && renderer != nil {
			rendered, err := renderer.Render(item.Text)
			if err == nil {
				fmt.Print(rendered)
			} else {
				fmt.Println(item.Text)
			}
		}

		if i < len(items)-1 {
			fmt.Println("\n---\n")
		}
	}
}
```

Add `"mykb/internal/tui"` to the imports and `tea "github.com/charmbracelet/bubbletea"` to the imports. Remove the old lipgloss styles (`redStyle`, `whiteStyle`, `blueStyle`, `grayStyle`) and the `formatTextPreview` function since they're no longer used.

**Step 2: Verify it compiles**

Run: `go build -o mykb ./cmd/mykb/`
Expected: No errors

**Step 3: Manual smoke test — plain text mode**

Run: `NO_COLOR=1 ./mykb query --top-k 3 "how to iterate over map keys"`

Expected: Results printed with `# #1 {score} Title` headers separated by `---`

**Step 4: Manual smoke test — TUI mode**

Run: `./mykb query --top-k 3 "how to iterate over map keys"`

Expected: Two-pane TUI launches. Sidebar shows results. Main pane shows rendered markdown. `Tab` switches panes. `q` quits.

**Step 5: Test key bindings**

- Sidebar: `j`/`k` navigate, `/` opens filter, type to filter, `Esc` clears
- Main: `j`/`k` scroll, `/` opens search, type term, `Enter`, `n`/`N` jump
- `Tab` switches between panes
- `q` quits

**Step 6: Commit**

```bash
git add cmd/mykb/main.go
git commit -m "feat: launch TUI for query results, plain text fallback with h1/hr formatting"
```

---

### Task 7: Integration test and polish

**Files:**
- Modify: `internal/tui/model.go` (if needed)
- Modify: `internal/tui/sidebar.go` (if needed)
- Modify: `internal/tui/mainpane.go` (if needed)

**Context:** Build the CLI, run real queries against the running server, verify both TUI and plain text modes work end-to-end. Fix any rendering issues.

**Step 1: Build CLI**

Run: `go build -o mykb ./cmd/mykb/`

**Step 2: Test TUI with real data**

Run: `./mykb query --top-k 5 "how to iterate over map keys"`

Verify:
- Sidebar shows ranked results with domain and title
- Main pane shows full rendered markdown for selected result
- `Tab` switches focus between sidebar and main pane
- Sidebar: `j`/`k` navigation works, selection highlights, main pane updates
- Sidebar: `/` filtering works, matches narrow list, auto-selects first match
- Main: scrolling works with `j`/`k`/arrows
- Main: `/` search finds text, `n`/`N` jumps between matches, `[2/5]` counter shown
- `q` exits cleanly

**Step 3: Test plain text fallback**

Run: `NO_COLOR=1 ./mykb query --top-k 3 "map declaration"`

Verify: h1 headers and `---` separators between results.

Run: `./mykb query --top-k 3 "map declaration" | head -20`

Verify: Piped output also uses plain text mode (not a TTY).

**Step 4: Test no-merge mode**

Run: `./mykb query --no-merge --top-k 5 "map declaration"`

Verify: TUI shows individual chunks (not merged segments).

**Step 5: Fix any rendering issues found during testing**

Common things to check:
- Sidebar width with long titles (should truncate)
- Main pane wrapping at narrow terminal widths
- Empty search results handling
- Very long markdown content scrolling

**Step 6: Clean up binary**

Run: `rm -f ./mykb`

**Step 7: Commit any fixes**

```bash
git add internal/tui/ cmd/mykb/main.go
git commit -m "fix(tui): polish rendering from integration testing"
```

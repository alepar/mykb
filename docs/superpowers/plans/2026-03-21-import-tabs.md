# Firefox Tab Import Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `mykb import-tabs` subcommand that extracts URLs from Firefox open tabs and appends them to `urls.txt`.

**Architecture:** New `internal/tabs/` package with three files: `firefox.go` (profile discovery, mozLz4 decompression, session parsing), `filter.go` (URL filtering), `picker.go` (bubbletea TUI checkbox list). CLI dispatch added to `cmd/mykb/main.go`.

**Tech Stack:** Go, `github.com/pierrec/lz4/v4`, bubbletea, lipgloss

**Spec:** `docs/superpowers/specs/2026-03-21-import-tabs-design.md`

---

## Chunk 1: Firefox Session Extraction

### Task 1: Add lz4 dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the lz4 dependency**

```bash
cd /var/home/alepar/AleCode/mykb && go get github.com/pierrec/lz4/v4
```

- [ ] **Step 2: Verify it resolves**

```bash
go mod tidy
```

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add pierrec/lz4 dependency for mozLz4 decompression"
```

### Task 2: mozLz4 decompression and session parsing

**Files:**
- Create: `internal/tabs/firefox.go`
- Create: `internal/tabs/firefox_test.go`

- [ ] **Step 1: Write test for mozLz4 decompression**

Create `internal/tabs/firefox_test.go`:

```go
package tabs

import (
	"encoding/binary"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func makeMozLz4(data []byte) []byte {
	// Compress with lz4 block API
	dst := make([]byte, lz4.CompressBlockBound(len(data)))
	n, err := lz4.CompressBlock(data, dst, nil)
	if err != nil || n == 0 {
		panic("compress failed")
	}
	// Build mozLz4 file: magic(8) + decompSize(4) + compressed
	buf := make([]byte, 12+n)
	copy(buf, []byte("mozLz40\x00"))
	binary.LittleEndian.PutUint32(buf[8:], uint32(len(data)))
	copy(buf[12:], dst[:n])
	return buf
}

func TestDecompressMozLz4(t *testing.T) {
	original := []byte(`{"windows":[{"tabs":[{"entries":[{"url":"https://example.com","title":"Example"}]}]}]}`)
	compressed := makeMozLz4(original)

	got, err := decompressMozLz4(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestDecompressMozLz4_BadMagic(t *testing.T) {
	data := []byte("notmozlz4data")
	_, err := decompressMozLz4(data)
	if err == nil {
		t.Error("expected error for bad magic")
	}
}
```

- [ ] **Step 2: Write test for session tab extraction**

Append to `internal/tabs/firefox_test.go`:

```go
func TestExtractTabs(t *testing.T) {
	sessionJSON := []byte(`{
		"windows": [
			{
				"tabs": [
					{"entries": [{"url": "https://old.com", "title": "Old"}, {"url": "https://example.com", "title": "Example"}]},
					{"entries": [{"url": "https://other.com", "title": "Other"}]},
					{"entries": []}
				]
			},
			{
				"tabs": [
					{"entries": [{"url": "https://second.com", "title": "Second Window"}]}
				]
			}
		]
	}`)

	tabs, err := extractTabs(sessionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 3 {
		t.Fatalf("got %d tabs, want 3", len(tabs))
	}
	// Last entry is used for each tab
	if tabs[0].URL != "https://example.com" || tabs[0].Title != "Example" {
		t.Errorf("tab 0: got %q %q", tabs[0].URL, tabs[0].Title)
	}
	if tabs[1].URL != "https://other.com" {
		t.Errorf("tab 1: got %q", tabs[1].URL)
	}
	if tabs[2].URL != "https://second.com" {
		t.Errorf("tab 2: got %q", tabs[2].URL)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/tabs/ -v
```
Expected: FAIL — `decompressMozLz4` and `extractTabs` not defined.

- [ ] **Step 4: Implement decompression and extraction**

Create `internal/tabs/firefox.go`:

```go
package tabs

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pierrec/lz4/v4"
)

// Tab represents a single browser tab with its current URL and title.
type Tab struct {
	URL   string
	Title string
}

// decompressMozLz4 decompresses Firefox's mozLz4 format:
// 8-byte magic "mozLz40\0" + 4-byte LE decompressed size + LZ4 block data.
func decompressMozLz4(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("mozLz4: data too short (%d bytes)", len(data))
	}
	if string(data[:8]) != "mozLz40\x00" {
		return nil, fmt.Errorf("mozLz4: bad magic %q", data[:8])
	}
	decompSize := binary.LittleEndian.Uint32(data[8:12])
	dst := make([]byte, decompSize)
	n, err := lz4.UncompressBlock(data[12:], dst)
	if err != nil {
		return nil, fmt.Errorf("mozLz4: decompress: %w", err)
	}
	return dst[:n], nil
}

// sessionData represents the Firefox session JSON structure.
type sessionData struct {
	Windows []struct {
		Tabs []struct {
			Entries []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"entries"`
		} `json:"tabs"`
	} `json:"windows"`
}

// extractTabs parses session JSON and returns tabs (using last entry per tab).
func extractTabs(data []byte) ([]Tab, error) {
	var session sessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}
	var tabs []Tab
	for _, w := range session.Windows {
		for _, t := range w.Tabs {
			if len(t.Entries) == 0 {
				continue
			}
			last := t.Entries[len(t.Entries)-1]
			tabs = append(tabs, Tab{URL: last.URL, Title: last.Title})
		}
	}
	return tabs, nil
}

// profilePaths returns the Firefox root directories to scan for profiles.ini.
func profilePaths() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	paths := []string{
		filepath.Join(home, ".mozilla", "firefox"),
		filepath.Join(home, ".var", "app", "org.mozilla.firefox", "config", "mozilla", "firefox"),
		filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths, filepath.Join(home, "Library", "Application Support", "Firefox"))
	}
	return paths
}

// profileDir represents a discovered Firefox profile.
type profileDir struct {
	Name string // profile name from profiles.ini
	Path string // absolute path to profile directory
}

// discoverProfiles parses profiles.ini in the given Firefox root dir.
func discoverProfiles(firefoxRoot string) []profileDir {
	iniPath := filepath.Join(firefoxRoot, "profiles.ini")
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return nil
	}
	return parseProfilesINI(data, firefoxRoot)
}

// parseProfilesINI extracts profile directories from profiles.ini content.
func parseProfilesINI(data []byte, baseDir string) []profileDir {
	var profiles []profileDir
	var name, path string
	var isRelative bool
	inProfile := false

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[Profile") {
			// Save previous profile if any
			if inProfile && path != "" {
				profiles = append(profiles, makeProfileDir(name, path, isRelative, baseDir))
			}
			inProfile = true
			name = ""
			path = ""
			isRelative = false
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Non-profile section — save previous and stop tracking
			if inProfile && path != "" {
				profiles = append(profiles, makeProfileDir(name, path, isRelative, baseDir))
			}
			inProfile = false
			continue
		}
		if !inProfile {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			switch k {
			case "Name":
				name = v
			case "Path":
				path = v
			case "IsRelative":
				isRelative = v == "1"
			}
		}
	}
	// Save last profile
	if inProfile && path != "" {
		profiles = append(profiles, makeProfileDir(name, path, isRelative, baseDir))
	}
	return profiles
}

func makeProfileDir(name, path string, isRelative bool, baseDir string) profileDir {
	absPath := path
	if isRelative {
		absPath = filepath.Join(baseDir, path)
	}
	return profileDir{Name: name, Path: absPath}
}

// readSessionTabs reads tabs from a profile's session restore file.
// Tries recovery.jsonlz4 first, falls back to previous.jsonlz4.
func readSessionTabs(profilePath string) ([]Tab, error) {
	backups := filepath.Join(profilePath, "sessionstore-backups")
	for _, name := range []string{"recovery.jsonlz4", "previous.jsonlz4"} {
		data, err := os.ReadFile(filepath.Join(backups, name))
		if err != nil {
			continue
		}
		decompressed, err := decompressMozLz4(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return extractTabs(decompressed)
	}
	return nil, fmt.Errorf("no session file found in %s", backups)
}

// DiscoverTabs finds all Firefox profiles and extracts their tabs.
// Returns all tabs and a list of profile names found.
func DiscoverTabs() ([]Tab, []string, error) {
	var allTabs []Tab
	var profileNames []string
	for _, root := range profilePaths() {
		profiles := discoverProfiles(root)
		for _, p := range profiles {
			tabs, err := readSessionTabs(p.Path)
			if err != nil {
				continue // skip profiles without session data
			}
			allTabs = append(allTabs, tabs...)
			name := p.Name
			if name == "" {
				name = filepath.Base(p.Path)
			}
			profileNames = append(profileNames, name)
		}
	}
	if len(allTabs) == 0 {
		return nil, nil, fmt.Errorf("no Firefox tabs found (checked %d locations)", len(profilePaths()))
	}
	return allTabs, profileNames, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/tabs/ -v
```
Expected: PASS

- [ ] **Step 6: Write test for profiles.ini parsing**

Append to `internal/tabs/firefox_test.go`:

```go
func TestParseProfilesINI(t *testing.T) {
	ini := `[Profile1]
Name=default
IsRelative=1
Path=abc123.default

[InstallCF146F38BCAB2D21]
Default=xyz789.default-release
Locked=1

[Profile0]
Name=default-release
IsRelative=1
Path=xyz789.default-release
`
	profiles := parseProfilesINI([]byte(ini), "/home/user/.mozilla/firefox")
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Name != "default" {
		t.Errorf("profile 0 name: got %q", profiles[0].Name)
	}
	if profiles[0].Path != "/home/user/.mozilla/firefox/abc123.default" {
		t.Errorf("profile 0 path: got %q", profiles[0].Path)
	}
	if profiles[1].Name != "default-release" {
		t.Errorf("profile 1 name: got %q", profiles[1].Name)
	}
}

func TestParseProfilesINI_AbsolutePath(t *testing.T) {
	ini := `[Profile0]
Name=custom
IsRelative=0
Path=/opt/firefox-profiles/custom
`
	profiles := parseProfilesINI([]byte(ini), "/home/user/.mozilla/firefox")
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
	if profiles[0].Path != "/opt/firefox-profiles/custom" {
		t.Errorf("got %q, want absolute path", profiles[0].Path)
	}
}
```

- [ ] **Step 7: Run tests**

```bash
go test ./internal/tabs/ -v
```
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/tabs/firefox.go internal/tabs/firefox_test.go
git commit -m "feat: add Firefox session extraction with mozLz4 decompression"
```

### Task 3: URL filtering

**Files:**
- Create: `internal/tabs/filter.go`
- Create: `internal/tabs/filter_test.go`

- [ ] **Step 1: Write filter tests**

Create `internal/tabs/filter_test.go`:

```go
package tabs

import "testing"

func TestShouldFilter(t *testing.T) {
	tests := []struct {
		url    string
		filter bool
	}{
		// Browser internal
		{"about:blank", true},
		{"about:config", true},
		{"chrome://settings", true},
		{"moz-extension://abc/popup.html", true},
		{"resource://gre/modules", true},

		// Local
		{"http://localhost:3000", true},
		{"http://127.0.0.1:8080/test", true},
		{"http://[::1]:9090", true},
		{"http://0.0.0.0:5000", true},

		// Known non-content
		{"https://claude.ai/chat/abc-123", true},
		{"https://platform.claude.com/oauth/code/success", true},

		// Should NOT filter
		{"https://example.com", false},
		{"https://github.com/user/repo", false},
		{"https://news.ycombinator.com/item?id=123", false},
		{"https://claude.ai/docs/tool-use", false},
		{"http://192.168.1.1/admin", false}, // LAN IPs are user's choice
	}

	for _, tt := range tests {
		got := ShouldFilter(tt.url)
		if got != tt.filter {
			t.Errorf("ShouldFilter(%q) = %v, want %v", tt.url, got, tt.filter)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/tabs/ -run TestShouldFilter -v
```
Expected: FAIL — `ShouldFilter` not defined.

- [ ] **Step 3: Implement filter**

Create `internal/tabs/filter.go`:

```go
package tabs

import (
	"net/url"
	"strings"
)

// internalSchemes are browser-internal URL schemes that never have ingestable content.
var internalSchemes = []string{"about:", "chrome://", "moz-extension://", "resource://"}

// localHosts are hostnames that indicate local/dev URLs.
var localHosts = []string{"localhost", "127.0.0.1", "::1", "0.0.0.0"}

// filteredPrefixes are URL prefixes for known non-content app UIs.
var filteredPrefixes = []string{
	"https://claude.ai/chat/",
	"https://platform.claude.com",
}

// ShouldFilter returns true if the URL should be automatically excluded.
func ShouldFilter(rawURL string) bool {
	for _, scheme := range internalSchemes {
		if strings.HasPrefix(rawURL, scheme) {
			return true
		}
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true // unparseable URLs are filtered
	}

	host := parsed.Hostname()
	for _, h := range localHosts {
		if host == h {
			return true
		}
	}

	for _, prefix := range filteredPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return true
		}
	}

	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/tabs/ -run TestShouldFilter -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tabs/filter.go internal/tabs/filter_test.go
git commit -m "feat: add URL filter for Firefox tab import"
```

## Chunk 2: TUI Picker and CLI Integration

### Task 4: TUI picker

**Files:**
- Create: `internal/tabs/picker.go`

- [ ] **Step 1: Implement the picker model**

Create `internal/tabs/picker.go`:

```go
package tabs

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	checkedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	uncheckedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray
	titleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	urlStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	cursorStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	footerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	statsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// PickerItem is a selectable tab in the picker.
type PickerItem struct {
	Tab      Tab
	Selected bool
}

// Picker is the bubbletea model for the tab selection TUI.
type Picker struct {
	items      []PickerItem
	cursor     int
	width      int
	height     int
	scrollOff  int // first visible item index
	confirmed  bool
	cancelled  bool
	stats      PickerStats
}

// PickerStats holds counts for the header display.
type PickerStats struct {
	Profiles   int
	Total      int
	Filtered   int
	Duplicates int
}

// NewPicker creates a picker with all items pre-selected.
func NewPicker(tabs []Tab, stats PickerStats) Picker {
	items := make([]PickerItem, len(tabs))
	for i, t := range tabs {
		items[i] = PickerItem{Tab: t, Selected: true}
	}
	return Picker{items: items, stats: stats}
}

// SelectedTabs returns the tabs the user confirmed.
func (p Picker) SelectedTabs() []Tab {
	if p.cancelled {
		return nil
	}
	var result []Tab
	for _, item := range p.items {
		if item.Selected {
			result = append(result, item.Tab)
		}
	}
	return result
}

// Cancelled returns true if the user quit without confirming.
func (p Picker) Cancelled() bool {
	return p.cancelled
}

func (p Picker) Init() tea.Cmd {
	return tea.WindowSize()
}

func (p Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			p.cancelled = true
			return p, tea.Quit
		case "enter":
			p.confirmed = true
			return p, tea.Quit
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
				p.ensureVisible()
			}
		case "down", "j":
			if p.cursor < len(p.items)-1 {
				p.cursor++
				p.ensureVisible()
			}
		case " ":
			if len(p.items) > 0 {
				p.items[p.cursor].Selected = !p.items[p.cursor].Selected
			}
		case "a":
			for i := range p.items {
				p.items[i].Selected = true
			}
		case "n":
			for i := range p.items {
				p.items[i].Selected = false
			}
		}
	}
	return p, nil
}

func (p *Picker) ensureVisible() {
	visible := p.visibleCount()
	if p.cursor < p.scrollOff {
		p.scrollOff = p.cursor
	}
	if p.cursor >= p.scrollOff+visible {
		p.scrollOff = p.cursor - visible + 1
	}
}

func (p Picker) visibleCount() int {
	// Header(1) + blank(1) + footer(1) + blank(1) = 4 lines of chrome
	v := p.height - 4
	if v < 1 {
		v = 1
	}
	return v
}

func (p Picker) View() string {
	if p.width == 0 {
		return "loading..."
	}

	var b strings.Builder

	// Header
	selected := 0
	for _, item := range p.items {
		if item.Selected {
			selected++
		}
	}
	header := fmt.Sprintf("Import Firefox Tabs (%d profiles, %d URLs found, %d filtered, %d duplicates)",
		p.stats.Profiles, p.stats.Total, p.stats.Filtered, p.stats.Duplicates)
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n\n")

	// Items
	visible := p.visibleCount()
	end := p.scrollOff + visible
	if end > len(p.items) {
		end = len(p.items)
	}
	maxTitleLen := p.width/2 - 6 // "[x] " + some padding
	if maxTitleLen < 20 {
		maxTitleLen = 20
	}

	for i := p.scrollOff; i < end; i++ {
		item := p.items[i]
		checkbox := uncheckedStyle.Render("[ ]")
		if item.Selected {
			checkbox = checkedStyle.Render("[x]")
		}

		title := item.Tab.Title
		if title == "" {
			title = "(no title)"
		}
		if len(title) > maxTitleLen {
			title = title[:maxTitleLen-3] + "..."
		}

		url := item.Tab.URL
		urlMaxLen := p.width - maxTitleLen - 8
		if urlMaxLen > 0 && len(url) > urlMaxLen {
			url = url[:urlMaxLen-3] + "..."
		}

		line := fmt.Sprintf("%s %s  %s",
			checkbox,
			titleStyle.Render(fmt.Sprintf("%-*s", maxTitleLen, title)),
			urlStyle.Render(url),
		)

		if i == p.cursor {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Footer
	b.WriteString("\n")
	footer := fmt.Sprintf("Space: toggle  a: select all  n: deselect all  Enter: confirm (%d)  q: cancel",
		selected)
	b.WriteString(footerStyle.Render(footer))

	return b.String()
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./internal/tabs/
```
Expected: Success

- [ ] **Step 3: Commit**

```bash
git add internal/tabs/picker.go
git commit -m "feat: add TUI picker for Firefox tab import"
```

### Task 5: CLI integration

**Files:**
- Modify: `cmd/mykb/main.go`

- [ ] **Step 1: Add import-tabs case to main switch and implement runImportTabs**

In `cmd/mykb/main.go`, add to the switch:

```go
case "import-tabs":
    runImportTabs(os.Args[2:])
```

Add import for `"mykb/internal/tabs"` and `"bufio"`.

Add the `runImportTabs` function:

```go
func runImportTabs(args []string) {
	fs := flag.NewFlagSet("import-tabs", flag.ExitOnError)
	urlsFile := fs.String("urls-file", "urls.txt", "file to append URLs to")
	fs.Parse(args)

	// Require TTY
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "error: import-tabs requires a terminal (TTY)")
		os.Exit(1)
	}

	// Discover tabs
	allTabs, profileNames, err := tabs.DiscoverTabs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Load existing URLs for dedup
	existing := loadURLSet(*urlsFile)

	// Filter and dedup
	var pickerTabs []tabs.Tab
	filtered := 0
	duplicates := 0
	for _, t := range allTabs {
		if tabs.ShouldFilter(t.URL) {
			filtered++
			continue
		}
		if existing[t.URL] {
			duplicates++
			continue
		}
		pickerTabs = append(pickerTabs, t)
	}

	if len(pickerTabs) == 0 {
		fmt.Printf("No new URLs to import (%d tabs found, %d filtered, %d duplicates)\n",
			len(allTabs), filtered, duplicates)
		return
	}

	// Run TUI picker
	stats := tabs.PickerStats{
		Profiles:   len(profileNames),
		Total:      len(allTabs),
		Filtered:   filtered,
		Duplicates: duplicates,
	}
	picker := tabs.NewPicker(pickerTabs, stats)
	p := tea.NewProgram(picker, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	final := result.(tabs.Picker)
	if final.Cancelled() {
		fmt.Println("cancelled")
		return
	}

	selected := final.SelectedTabs()
	if len(selected) == 0 {
		fmt.Println("no URLs selected")
		return
	}

	// Append to file
	if err := appendURLs(*urlsFile, selected); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", *urlsFile, err)
		os.Exit(1)
	}
	fmt.Printf("appended %d URLs to %s\n", len(selected), *urlsFile)
}

func loadURLSet(path string) map[string]bool {
	set := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return set // file doesn't exist yet, empty set
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			set[line] = true
		}
	}
	return set
}

func appendURLs(path string, selected []tabs.Tab) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Ensure we start on a new line if file doesn't end with newline
	info, err := f.Stat()
	if err == nil && info.Size() > 0 {
		// Read last byte to check for trailing newline
		rf, err := os.Open(path)
		if err == nil {
			buf := make([]byte, 1)
			rf.Seek(-1, 2)
			rf.Read(buf)
			rf.Close()
			if buf[0] != '\n' {
				f.WriteString("\n")
			}
		}
	}

	for _, t := range selected {
		if _, err := fmt.Fprintln(f, t.URL); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 2: Update printUsage**

Add the import-tabs line:

```go
fmt.Fprintln(os.Stderr, "  mykb import-tabs [--urls-file FILE]")
```

- [ ] **Step 3: Verify it compiles**

```bash
go build -o mykb ./cmd/mykb/
```
Expected: Success

- [ ] **Step 4: Commit**

```bash
git add cmd/mykb/main.go
git commit -m "feat: add import-tabs subcommand to CLI"
```

### Task 6: Manual end-to-end test

- [ ] **Step 1: Run import-tabs on this machine**

```bash
./mykb import-tabs --urls-file /tmp/test-import.txt
```

Expected: TUI shows tabs from the Flatpak Firefox profile. Select a few, press Enter.

- [ ] **Step 2: Verify the output file**

```bash
cat /tmp/test-import.txt
```

Expected: Selected URLs, one per line.

- [ ] **Step 3: Run again to verify dedup works**

```bash
./mykb import-tabs --urls-file /tmp/test-import.txt
```

Expected: Previously imported URLs should be gone from the list (marked as duplicates in header).

- [ ] **Step 4: Clean up test file**

```bash
rm /tmp/test-import.txt
```

- [ ] **Step 5: Run all tests**

```bash
go test ./internal/tabs/ -v
```
Expected: All tests pass.

package tui

import (
	"fmt"
	"net/url"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	paneSidebar = iota
	paneMain
)

// ResultItem represents a single search result displayed in the TUI.
type ResultItem struct {
	Rank          int
	Score         float32
	Title         string
	URL           string
	ChunkIndex    int
	ChunkIndexEnd int
	ChunkCount    int
	Text          string
}

// Domain extracts the hostname from the URL.
func (r ResultItem) Domain() string {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return r.URL
	}
	return u.Host
}

// ChunkPosition returns a human-readable chunk position string like "3-5/8" or "4/8".
func (r ResultItem) ChunkPosition() string {
	if r.ChunkCount <= 0 {
		return ""
	}
	if r.ChunkIndexEnd > r.ChunkIndex {
		return fmt.Sprintf("%d-%d/%d", r.ChunkIndex, r.ChunkIndexEnd, r.ChunkCount)
	}
	return fmt.Sprintf("%d/%d", r.ChunkIndex, r.ChunkCount)
}

// Model is the top-level Bubble Tea model for the query TUI.
type Model struct {
	items  []ResultItem
	width  int
	height int
	active int // paneSidebar or paneMain

	// Sidebar state
	selected      int // index into filteredIdx
	filteredIdx   []int
	sidebarFilter textinput.Model
	sidebarSearch bool

	// Main pane state
	viewport      viewport.Model
	renderedTexts []string
	mainSearch    textinput.Model
	mainSearching bool
	searchMatches []int
	searchCursor  int
}

// New creates a new Model with the given result items.
func New(items []ResultItem) Model {
	idx := make([]int, len(items))
	for i := range items {
		idx[i] = i
	}

	sf := textinput.New()
	sf.Prompt = "filter: "
	sf.CharLimit = 128

	ms := textinput.New()
	ms.Prompt = "search: "
	ms.CharLimit = 128

	return Model{
		items:         items,
		filteredIdx:   idx,
		sidebarFilter: sf,
		mainSearch:    ms,
		active:        paneSidebar,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.WindowSize()
}

// Update implements tea.Model.
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

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading..."
	}
	sidebar := renderSidebar(&m)
	main := renderMainPane(&m)
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// Route to active input if searching/filtering
	if m.sidebarSearch {
		return m.handleSidebarSearch(msg)
	}
	if m.mainSearching {
		return m.handleMainSearch(msg)
	}

	switch msg.String() {
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
		return m.handleSidebarNav(msg)
	}
	return m.handleMainNav(msg)
}

func (m Model) handleSidebarNav(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m Model) handleMainNav(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		m.mainSearching = true
		m.mainSearch.Focus()
		return m, textinput.Blink
	case "n":
		if len(m.searchMatches) > 0 {
			m.searchCursor = (m.searchCursor + 1) % len(m.searchMatches)
			m.viewport.SetYOffset(m.searchMatches[m.searchCursor])
		}
		return m, nil
	case "N":
		if len(m.searchMatches) > 0 {
			m.searchCursor = (m.searchCursor - 1 + len(m.searchMatches)) % len(m.searchMatches)
			m.viewport.SetYOffset(m.searchMatches[m.searchCursor])
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m Model) handleSidebarSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.sidebarSearch = false
		m.sidebarFilter.Blur()
		return m, nil
	case "esc":
		m.sidebarSearch = false
		m.sidebarFilter.SetValue("")
		m.sidebarFilter.Blur()
		m.applyFilter()
		return m, nil
	}
	var cmd tea.Cmd
	m.sidebarFilter, cmd = m.sidebarFilter.Update(msg)
	m.applyFilter()
	return m, cmd
}

func (m *Model) applyFilter() {
	m.filteredIdx = filterItems(m.items, m.sidebarFilter.Value())
	if m.selected >= len(m.filteredIdx) {
		m.selected = max(0, len(m.filteredIdx)-1)
	}
	m.syncMainPane()
}

func (m Model) handleMainSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.mainSearching = false
		m.mainSearch.Blur()
		content := m.viewport.View()
		m.searchMatches = findMatches(content, m.mainSearch.Value())
		m.searchCursor = 0
		if len(m.searchMatches) > 0 {
			m.viewport.SetYOffset(m.searchMatches[0])
		}
		return m, nil
	case "esc":
		m.mainSearching = false
		m.mainSearch.SetValue("")
		m.mainSearch.Blur()
		m.searchMatches = nil
		m.searchCursor = 0
		return m, nil
	}
	var cmd tea.Cmd
	m.mainSearch, cmd = m.mainSearch.Update(msg)
	return m, cmd
}

func (m *Model) syncMainPane() {
	if len(m.filteredIdx) == 0 || m.selected >= len(m.filteredIdx) {
		m.viewport.SetContent("No results.")
		return
	}
	idx := m.filteredIdx[m.selected]
	item := m.items[idx]
	mainWidth := m.width - SidebarWidth - 2 // border
	header := renderHeader(item, mainWidth)
	body := ""
	if idx < len(m.renderedTexts) {
		body = m.renderedTexts[idx]
	}
	m.viewport.SetContent(header + "\n" + body)

	// Reset search state
	m.searchMatches = nil
	m.searchCursor = 0
	m.mainSearch.SetValue("")
}

func (m *Model) updateViewport() {
	mainWidth := m.width - SidebarWidth - 2
	if mainWidth < 0 {
		mainWidth = 0
	}

	m.renderedTexts = make([]string, len(m.items))
	for i, item := range m.items {
		m.renderedTexts[i] = renderMarkdown(item.Text, mainWidth)
	}

	m.viewport = viewport.New(mainWidth, m.height)
	m.viewport.Style = lipgloss.NewStyle()
	m.syncMainPane()
}

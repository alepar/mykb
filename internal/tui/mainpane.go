package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// renderHeader renders the header block for a result item.
// When focused is true, the title line gets the focus highlight.
func renderHeader(item ResultItem, width int, focused bool) string {
	var sb strings.Builder
	if focused {
		sb.WriteString(focusHeaderStyle.Width(width).Render(item.Title))
	} else {
		sb.WriteString(blurHeaderStyle.Width(width).Render(item.Title))
	}
	sb.WriteString("\n")
	sb.WriteString(headerURLStyle.Render(item.URL))
	sb.WriteString("\n")
	if pos := item.ChunkPosition(); pos != "" {
		sb.WriteString(headerChunkStyle.Render("Chunks " + pos))
		sb.WriteString("\n")
	}
	if width > 0 {
		sb.WriteString(headerChunkStyle.Render(strings.Repeat("─", width)))
	}
	return sb.String()
}

// renderMarkdown renders markdown text using the provided glamour renderer.
func renderMarkdown(text string, renderer *glamour.TermRenderer) string {
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return out
}

// findMatches returns 0-based line indices where the search term appears (case-insensitive, ANSI-stripped).
func findMatches(content, search string) []int {
	if search == "" {
		return nil
	}
	search = strings.ToLower(search)
	lines := strings.Split(content, "\n")
	var matches []int
	for i, line := range lines {
		stripped := ansiRegex.ReplaceAllString(line, "")
		if strings.Contains(strings.ToLower(stripped), search) {
			matches = append(matches, i)
		}
	}
	return matches
}

// renderMainPane renders the main content pane: pinned header + scrollable viewport + status bar.
func renderMainPane(m *Model) string {
	mainWidth := m.width - SidebarWidth - 2
	if mainWidth < 20 {
		mainWidth = 20
	}

	var sb strings.Builder

	// Pinned header (always visible at top)
	sb.WriteString(m.currentHeader)
	sb.WriteString("\n")

	// Scrollable viewport
	sb.WriteString(m.viewport.View())

	// Bottom bar: search input or scroll position
	sb.WriteString("\n")
	if m.mainSearching {
		sb.WriteString(m.mainSearch.View())
	} else if len(m.searchMatches) > 0 {
		info := fmt.Sprintf(" match %d/%d", m.searchCursor+1, len(m.searchMatches))
		sb.WriteString(searchStyle.Render("/" + m.mainSearch.Value()))
		sb.WriteString(matchCountStyle.Render(info))
	} else {
		// Scroll position indicator
		pct := m.viewport.ScrollPercent()
		var pos string
		if pct <= 0 {
			pos = "Top"
		} else if pct >= 1.0 {
			pos = "Bot"
		} else {
			pos = fmt.Sprintf("%d%%", int(pct*100))
		}
		sb.WriteString(scrollInfoStyle.Render(pos))
	}

	return lipgloss.NewStyle().Width(mainWidth).Render(sb.String())
}

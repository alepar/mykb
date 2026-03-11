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
func renderHeader(item ResultItem, width int) string {
	var sb strings.Builder
	sb.WriteString(headerTitleStyle.Render(item.Title))
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

// renderMainPane renders the main content pane including the viewport and optional search bar.
func renderMainPane(m *Model) string {
	mainWidth := m.width - SidebarWidth - 2
	if mainWidth < 20 {
		mainWidth = 20
	}

	var sb strings.Builder
	sb.WriteString(m.viewport.View())

	if m.mainSearching {
		sb.WriteString("\n")
		sb.WriteString(m.mainSearch.View())
	} else if len(m.searchMatches) > 0 {
		sb.WriteString("\n")
		info := fmt.Sprintf(" match %d/%d", m.searchCursor+1, len(m.searchMatches))
		sb.WriteString(searchStyle.Render("/" + m.mainSearch.Value()))
		sb.WriteString(matchCountStyle.Render(info))
	}

	return lipgloss.NewStyle().Width(mainWidth).Render(sb.String())
}

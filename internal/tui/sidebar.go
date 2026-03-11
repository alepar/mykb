package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// filterItems returns indices into items whose domain or title match the filter (case-insensitive substring).
// An empty filter returns all indices.
func filterItems(items []ResultItem, filter string) []int {
	filter = strings.ToLower(strings.TrimSpace(filter))
	var result []int
	for i, item := range items {
		if filter == "" {
			result = append(result, i)
			continue
		}
		domain := strings.ToLower(item.Domain())
		title := strings.ToLower(item.Title)
		if strings.Contains(domain, filter) || strings.Contains(title, filter) {
			result = append(result, i)
		}
	}
	return result
}

// renderSidebarEntry renders a single sidebar entry as two lines.
func renderSidebarEntry(item ResultItem, width int, active bool) string {
	rank := rankStyle.Render(fmt.Sprintf("#%d", item.Rank))
	score := scoreStyle.Render(fmt.Sprintf("{%.2f}", item.Score))
	domain := domainStyle.Render(item.Domain())
	line1 := fmt.Sprintf("%s %s %s", rank, score, domain)

	titleText := item.Title
	if width >= 8 && lipgloss.Width(titleText) > width-2 {
		runes := []rune(titleText)
		for len(runes) > 0 && lipgloss.Width(string(runes)) > width-3 {
			runes = runes[:len(runes)-1]
		}
		titleText = string(runes) + "\u2026"
	}
	line2 := "  " + titleStyle.Render(titleText)

	entry := line1 + "\n" + line2
	if active {
		entry = sidebarActiveStyle.Width(width).Render(entry)
	} else {
		entry = sidebarItemStyle.Width(width).Render(entry)
	}
	return entry
}

// renderSidebar renders the full sidebar pane.
func renderSidebar(m *Model) string {
	var sb strings.Builder

	if m.sidebarSearch {
		sb.WriteString(m.sidebarFilter.View())
		sb.WriteString("\n")
	}

	contentWidth := SidebarWidth - 2 // account for border
	if contentWidth < 0 {
		contentWidth = 0
	}

	for i, idx := range m.filteredIdx {
		isActive := i == m.selected
		entry := renderSidebarEntry(m.items[idx], contentWidth, isActive)
		sb.WriteString(entry)
		sb.WriteString("\n")
	}

	return sidebarStyle.Height(m.height).Render(sb.String())
}

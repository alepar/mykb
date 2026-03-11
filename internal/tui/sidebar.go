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
	// When active, apply background to individual styled components so the
	// highlight covers the entire entry uniformly.
	bg := lipgloss.Color("")
	if active {
		bg = lipgloss.Color("236")
	}
	rank := rankStyle.Background(bg).Render(fmt.Sprintf("#%d", item.Rank))
	score := scoreStyle.Background(bg).Render(fmt.Sprintf("{%.2f}", item.Score))
	domain := domainStyle.Background(bg).Render(item.Domain())
	line1 := fmt.Sprintf("%s %s %s", rank, score, domain)

	titleText := item.Title
	if width >= 8 && lipgloss.Width(titleText) > width-2 {
		runes := []rune(titleText)
		for len(runes) > 0 && lipgloss.Width(string(runes)) > width-3 {
			runes = runes[:len(runes)-1]
		}
		titleText = string(runes) + "\u2026"
	}
	line2 := "  " + titleStyle.Background(bg).Render(titleText)

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

	contentWidth := SidebarWidth - 2 // account for border
	if contentWidth < 0 {
		contentWidth = 0
	}

	// "Matches" header — highlighted when sidebar has focus
	if m.active == paneSidebar {
		sb.WriteString(focusHeaderStyle.Width(contentWidth).Render("Matches"))
	} else {
		sb.WriteString(blurHeaderStyle.Width(contentWidth).Render("Matches"))
	}
	sb.WriteString("\n")

	// Entries
	for i, idx := range m.filteredIdx {
		isActive := i == m.selected
		entry := renderSidebarEntry(m.items[idx], contentWidth, isActive)
		sb.WriteString(entry)
		sb.WriteString("\n")
	}

	// Pad to push bottom bar to the last line.
	// Header = 1 line, each entry = 3 lines (2 content + 1 blank), bottom bar = 1 line
	usedLines := 1 + len(m.filteredIdx)*3 + 1
	for i := usedLines; i < m.height; i++ {
		sb.WriteString("\n")
	}

	// Bottom bar: filter input or active filter state + match count
	if m.sidebarSearch {
		left := m.sidebarFilter.View()
		right := matchCountStyle.Render(fmt.Sprintf("%d/%d", len(m.filteredIdx), len(m.items)))
		gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		sb.WriteString(left + strings.Repeat(" ", gap) + right)
	} else if m.sidebarFilter.Value() != "" {
		left := searchStyle.Render("/" + m.sidebarFilter.Value())
		right := matchCountStyle.Render(fmt.Sprintf("%d/%d", len(m.filteredIdx), len(m.items)))
		gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		sb.WriteString(left + strings.Repeat(" ", gap) + right)
	}

	// Focus indication: blue border when sidebar has focus, gray when not
	borderColor := sidebarBlurBorder
	if m.active == paneSidebar {
		borderColor = sidebarFocusBorder
	}
	return sidebarStyle.BorderForeground(borderColor).Height(m.height).Render(sb.String())
}

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

// highlightSearch replaces all case-insensitive occurrences of search in the
// ANSI-rendered content with a highlighted version. It walks each line,
// strips ANSI to find match positions, then splices highlights into the raw line.
func highlightSearch(content, search string) string {
	if search == "" {
		return content
	}
	lower := strings.ToLower(search)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		stripped := ansiRegex.ReplaceAllString(line, "")
		if !strings.Contains(strings.ToLower(stripped), lower) {
			continue
		}
		// Build a mapping from stripped-index to raw-index
		// Walk both strings in parallel, skipping ANSI sequences in raw
		rawBytes := []byte(line)
		strippedBytes := []byte(stripped)
		// strippedToRaw[i] = index into rawBytes where stripped byte i starts
		strippedToRaw := make([]int, len(strippedBytes)+1)
		si := 0
		ri := 0
		for si < len(strippedBytes) && ri < len(rawBytes) {
			// Skip ANSI escape sequences
			if rawBytes[ri] == 0x1b && ri+1 < len(rawBytes) && rawBytes[ri+1] == '[' {
				ri += 2
				for ri < len(rawBytes) && !((rawBytes[ri] >= 'A' && rawBytes[ri] <= 'Z') || (rawBytes[ri] >= 'a' && rawBytes[ri] <= 'z')) {
					ri++
				}
				if ri < len(rawBytes) {
					ri++ // skip the final letter
				}
				continue
			}
			strippedToRaw[si] = ri
			si++
			ri++
		}
		strippedToRaw[len(strippedBytes)] = len(rawBytes)

		// Find all matches in stripped string and splice highlights into raw
		var result []byte
		lastRaw := 0
		sLower := strings.ToLower(stripped)
		pos := 0
		for {
			idx := strings.Index(sLower[pos:], lower)
			if idx < 0 {
				break
			}
			matchStart := pos + idx
			matchEnd := matchStart + len(lower)

			rawStart := strippedToRaw[matchStart]
			rawEnd := strippedToRaw[matchEnd]

			// Copy everything before the match
			result = append(result, rawBytes[lastRaw:rawStart]...)
			// Extract the matched raw text (preserving original case) and highlight it
			matchText := string(rawBytes[rawStart:rawEnd])
			// Strip any ANSI from the match portion so highlight renders cleanly
			matchPlain := ansiRegex.ReplaceAllString(matchText, "")
			result = append(result, []byte(highlightStyle.Render(matchPlain))...)
			lastRaw = rawEnd
			pos = matchEnd
		}
		result = append(result, rawBytes[lastRaw:]...)
		lines[i] = string(result)
	}
	return strings.Join(lines, "\n")
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

package tabs

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	checkedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	uncheckedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	titleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	urlStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	cursorStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	footerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	statsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

type PickerItem struct {
	Tab      Tab
	Selected bool
}

type Picker struct {
	items      []PickerItem
	cursor     int
	width      int
	height     int
	scrollOff  int
	confirmed  bool
	cancelled  bool
	stats      PickerStats
}

type PickerStats struct {
	Profiles   int
	Total      int
	Filtered   int
	Duplicates int
}

func NewPicker(tabs []Tab, stats PickerStats) Picker {
	items := make([]PickerItem, len(tabs))
	for i, t := range tabs {
		items[i] = PickerItem{Tab: t, Selected: true}
	}
	return Picker{items: items, stats: stats}
}

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

	visible := p.visibleCount()
	end := p.scrollOff + visible
	if end > len(p.items) {
		end = len(p.items)
	}
	maxTitleLen := p.width/2 - 6
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

	b.WriteString("\n")
	footer := fmt.Sprintf("Space: toggle  a: select all  n: deselect all  Enter: confirm (%d)  q: cancel",
		selected)
	b.WriteString(footerStyle.Render(footer))

	return b.String()
}

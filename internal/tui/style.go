package tui

import "github.com/charmbracelet/lipgloss"

var (
	SidebarWidth = 30

	sidebarStyle       = lipgloss.NewStyle().Width(SidebarWidth).BorderRight(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("8"))
	sidebarActiveStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	sidebarItemStyle   = lipgloss.NewStyle()
	rankStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	scoreStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	domainStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	titleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	headerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	headerURLStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	headerChunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	searchStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	matchCountStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

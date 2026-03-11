package tui

import "github.com/charmbracelet/lipgloss"

const SidebarWidth = 30

var (
	// Sidebar styles
	sidebarStyle        = lipgloss.NewStyle().Width(SidebarWidth).BorderRight(true).BorderStyle(lipgloss.NormalBorder())
	sidebarFocusBorder  = lipgloss.Color("12") // blue when focused
	sidebarBlurBorder   = lipgloss.Color("8")  // gray when blurred
	sidebarActiveStyle  = lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	sidebarItemStyle    = lipgloss.NewStyle()
	rankStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	scoreStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	domainStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	titleStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	// Main pane styles
	headerTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	headerURLStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	headerChunkStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	searchStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	matchCountStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	scrollInfoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	mainFocusBorder   = lipgloss.Color("12")
	mainBlurBorder    = lipgloss.Color("8")
)

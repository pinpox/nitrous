package main

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Colors
var (
	colorPrimary   = lipgloss.Color("#7B68EE")
	colorSecondary = lipgloss.Color("#5B5682")
	colorMuted     = lipgloss.Color("#636363")
	colorHighlight = lipgloss.Color("#E0DAFF")
	colorStatusBg = lipgloss.Color("#24283B")
	colorWhite    = lipgloss.Color("#C0CAF5")
	colorGreen    = lipgloss.Color("#9ECE6A")
	colorRed      = lipgloss.Color("#F7768E")
)

// Layout constants
const (
	sidebarWidth = 22
	inputHeight  = 3
	headerHeight = 1
	statusHeight = 1
)

// Styles
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			Padding(0, 1)

	sidebarStyle = lipgloss.NewStyle().
			Width(sidebarWidth).
			BorderRight(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorSecondary)

	sidebarItemStyle = lipgloss.NewStyle().
				Foreground(colorWhite).
				Padding(0, 1)

	sidebarSelectedStyle = lipgloss.NewStyle().
				Foreground(colorHighlight).
				Background(colorSecondary).
				Bold(true).
				Padding(0, 1)

	chatAuthorStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	chatOwnAuthorStyle = lipgloss.NewStyle().
				Foreground(colorGreen).
				Bold(true)

	chatTimestampStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(colorStatusBg).
			Padding(0, 1)

	statusConnectedStyle = lipgloss.NewStyle().
				Foreground(colorGreen)

	chatSystemStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	tabActiveStyle = lipgloss.NewStyle().
			Foreground(colorHighlight).
			Bold(true).
			Padding(0, 1)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 1)
)

// detectGlamourStyle queries the terminal background and returns "dark" or "light".
// Must be called before the TUI starts.
func detectGlamourStyle() string {
	if termenv.HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// newMarkdownRenderer creates a glamour terminal renderer at the given width.
// style should be "dark" or "light" (detected once at startup via detectGlamourStyle).
func newMarkdownRenderer(width int, style string) *glamour.TermRenderer {
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	return r
}

// renderMarkdown renders markdown content to terminal-styled text.
// Falls back to plain text if the renderer is nil or rendering fails.
func renderMarkdown(r *glamour.TermRenderer, content string) string {
	if r == nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return out
}

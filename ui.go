package main

import (
	"encoding/hex"

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
	colorStatusBg  = lipgloss.Color("#24283B")
	colorWhite     = lipgloss.Color("#C0CAF5")
	colorGreen     = lipgloss.Color("#9ECE6A")
	colorRed       = lipgloss.Color("#F7768E")
)

// Distinct author colors — chosen for readability on dark backgrounds.
var authorColorsDark = []lipgloss.Color{
	"#7B68EE", // medium slate blue
	"#FF6B6B", // coral red
	"#4ECDC4", // teal
	"#FFD93D", // gold
	"#C084FC", // violet
	"#FF8C42", // orange
	"#6BCB77", // green
	"#4D96FF", // blue
	"#FF6EC7", // hot pink
	"#00D2FF", // cyan
	"#E879F9", // fuchsia
	"#A3E635", // lime
}

// Distinct author colors — chosen for readability on light backgrounds.
var authorColorsLight = []lipgloss.Color{
	"#4B38AE", // deep slate blue
	"#C0392B", // dark red
	"#1A8A7D", // dark teal
	"#B8860B", // dark goldenrod
	"#7B2D8E", // dark violet
	"#C0561A", // dark orange
	"#2E7D32", // dark green
	"#1A5DB0", // dark blue
	"#B03060", // dark pink
	"#007A99", // dark cyan
	"#9B30FF", // dark fuchsia
	"#558B2F", // dark lime
}

// authorColors is set at init time based on terminal background.
var authorColors []lipgloss.Color

// initAuthorColors selects the author color palette based on terminal background.
// Must be called before the TUI starts (e.g., alongside detectGlamourStyle).
func initAuthorColors() {
	if termenv.HasDarkBackground() {
		authorColors = authorColorsDark
	} else {
		authorColors = authorColorsLight
	}
}

// colorForPubkey derives a stable color from a hex pubkey.
func colorForPubkey(pubkey string) lipgloss.Color {
	colors := authorColors
	if len(colors) == 0 {
		colors = authorColorsDark
	}
	if len(pubkey) < 2 {
		return colors[0]
	}
	b, err := hex.DecodeString(pubkey[:2])
	if err != nil || len(b) == 0 {
		return colors[0]
	}
	return colors[int(b[0])%len(colors)]
}

// Layout constants
const (
	minSidebarWidth = 12
	sidebarPadding  = 3 // "#", "~", or "@" prefix + left/right padding
	sidebarBorder   = 1 // right border on sidebar
	inputMinHeight  = 1
	inputMaxHeight  = 8
)

// Styles
var (
	sidebarStyle = lipgloss.NewStyle().
		BorderRight(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorSecondary)

	sidebarItemStyle = lipgloss.NewStyle().
		Foreground(colorWhite).
		Padding(0, 1)

	sidebarUnreadStyle = lipgloss.NewStyle().
		Foreground(colorWhite).
		Bold(true).
		Padding(0, 1)

	sidebarSelectedStyle = lipgloss.NewStyle().
		Foreground(colorHighlight).
		Background(colorSecondary).
		Bold(true).
		Padding(0, 1)

	sidebarSectionStyle = lipgloss.NewStyle().
		Foreground(colorMuted).
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

	qrTitleStyle = lipgloss.NewStyle().
		Foreground(colorPrimary).
		Bold(true)

	acSuggestionStyle = lipgloss.NewStyle().
		Foreground(colorWhite).
		Padding(0, 1)

	acSelectedStyle = lipgloss.NewStyle().
		Foreground(colorHighlight).
		Background(colorSecondary).
		Bold(true).
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

// newMarkdownRenderer creates a glamour terminal renderer.
// style should be "dark" or "light" (detected once at startup via detectGlamourStyle).
// Word wrapping is disabled here; the chat renderer handles wrapping itself
// to account for the per-message prefix width.
func newMarkdownRenderer(style string) *glamour.TermRenderer {
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath(style),
		glamour.WithWordWrap(0),
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

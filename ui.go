package main

import (
	"encoding/hex"
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/muesli/termenv"
)

// Theme bundles all colour values and pre-built lipgloss styles for the UI.
// A single Theme instance is stored on the model and threaded through all
// rendering code, replacing the former package-level style variables.
type Theme struct {
	// Colour roles
	Primary   color.Color
	Secondary color.Color
	Muted     color.Color
	Highlight color.Color
	StatusBg  color.Color
	Text      color.Color
	Success   color.Color

	// Pre-built styles
	Sidebar         lipgloss.Style
	SidebarItem     lipgloss.Style
	SidebarUnread   lipgloss.Style
	SidebarSelected lipgloss.Style
	SidebarSection  lipgloss.Style
	ChatAuthor      lipgloss.Style
	ChatOwnAuthor   lipgloss.Style
	ChatTimestamp   lipgloss.Style
	StatusBar       lipgloss.Style
	StatusConnected lipgloss.Style
	ChatSystem      lipgloss.Style
	QRTitle         lipgloss.Style
	ACSuggestion    lipgloss.Style
	ACSelected      lipgloss.Style
	Selection       lipgloss.Style
	ChatMention     lipgloss.Style

	// Author colour palette for per-pubkey colouring.
	AuthorColors []color.Color

	// Glamour markdown renderer style ("dark" or "light").
	GlamourStyle string
}

// buildTheme constructs a fully populated Theme for dark or light backgrounds.
func buildTheme(isDark bool) Theme {
	var t Theme
	if isDark {
		t.Primary = lipgloss.Color("#7B68EE")
		t.Secondary = lipgloss.Color("#5B5682")
		t.Muted = lipgloss.Color("#636363")
		t.Highlight = lipgloss.Color("#E0DAFF")
		t.StatusBg = lipgloss.Color("#24283B")
		t.Text = lipgloss.Color("#C0CAF5")
		t.Success = lipgloss.Color("#9ECE6A")
		t.AuthorColors = []color.Color{
			lipgloss.Color("#7B68EE"), // medium slate blue
			lipgloss.Color("#FF6B6B"), // coral red
			lipgloss.Color("#4ECDC4"), // teal
			lipgloss.Color("#FFD93D"), // gold
			lipgloss.Color("#C084FC"), // violet
			lipgloss.Color("#FF8C42"), // orange
			lipgloss.Color("#4D96FF"), // blue
			lipgloss.Color("#FF6EC7"), // hot pink
			lipgloss.Color("#00D2FF"), // cyan
			lipgloss.Color("#E879F9"), // fuchsia
			lipgloss.Color("#F5A623"), // amber
			lipgloss.Color("#7FDBCA"), // mint
		}
		t.GlamourStyle = "dark"
	} else {
		t.Primary = lipgloss.Color("#4B38AE")
		t.Secondary = lipgloss.Color("#B8B0D8")
		t.Muted = lipgloss.Color("#7A7A7A")
		t.Highlight = lipgloss.Color("#3B2E8A")
		t.StatusBg = lipgloss.Color("#E8E4F0")
		t.Text = lipgloss.Color("#2E2E3E")
		t.Success = lipgloss.Color("#2E7D32")
		t.AuthorColors = []color.Color{
			lipgloss.Color("#4B38AE"), // deep slate blue
			lipgloss.Color("#C0392B"), // dark red
			lipgloss.Color("#1A8A7D"), // dark teal
			lipgloss.Color("#B8860B"), // dark goldenrod
			lipgloss.Color("#7B2D8E"), // dark violet
			lipgloss.Color("#C0561A"), // dark orange
			lipgloss.Color("#1A5DB0"), // dark blue
			lipgloss.Color("#B03060"), // dark pink
			lipgloss.Color("#007A99"), // dark cyan
			lipgloss.Color("#9B30FF"), // dark fuchsia
			lipgloss.Color("#CC7A00"), // dark amber
			lipgloss.Color("#1A6B5A"), // dark mint
		}
		t.GlamourStyle = "light"
	}

	// Build styles from the palette.
	t.Sidebar = lipgloss.NewStyle().
		BorderRight(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(t.Secondary)

	t.SidebarItem = lipgloss.NewStyle().
		Foreground(t.Text).
		Padding(0, 1)

	t.SidebarUnread = lipgloss.NewStyle().
		Foreground(t.Text).
		Bold(true).
		Padding(0, 1)

	t.SidebarSelected = lipgloss.NewStyle().
		Foreground(t.Highlight).
		Background(t.Secondary).
		Bold(true).
		Padding(0, 1)

	t.SidebarSection = lipgloss.NewStyle().
		Foreground(t.Muted).
		Bold(true).
		Padding(0, 1)

	t.ChatAuthor = lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	t.ChatOwnAuthor = lipgloss.NewStyle().
		Foreground(t.Success).
		Bold(true)

	t.ChatTimestamp = lipgloss.NewStyle().
		Foreground(t.Muted)

	t.StatusBar = lipgloss.NewStyle().
		Foreground(t.Text).
		Background(t.StatusBg).
		Padding(0, 1)

	t.StatusConnected = lipgloss.NewStyle().
		Foreground(t.Success)

	t.ChatSystem = lipgloss.NewStyle().
		Foreground(t.Muted)

	t.QRTitle = lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	t.ACSuggestion = lipgloss.NewStyle().
		Foreground(t.Text).
		Padding(0, 1)

	t.ACSelected = lipgloss.NewStyle().
		Foreground(t.Highlight).
		Background(t.Secondary).
		Bold(true).
		Padding(0, 1)

	t.Selection = lipgloss.NewStyle().Reverse(true)

	t.ChatMention = lipgloss.NewStyle().
		Foreground(t.Highlight).
		Bold(true)

	return t
}

// resolveTheme checks the config override first, falls back to terminal
// background detection, and returns a fully built Theme.
func resolveTheme(cfg Config) Theme {
	switch cfg.Theme {
	case "dark":
		return buildTheme(true)
	case "light":
		return buildTheme(false)
	default: // "auto" or ""
		return buildTheme(termenv.HasDarkBackground())
	}
}

// colorForPubkey derives a stable color from a hex pubkey using the given
// author colour palette.
func colorForPubkey(pubkey string, colors []color.Color) color.Color {
	if len(colors) == 0 {
		// Fallback to a hard-coded dark default so we never panic.
		return lipgloss.Color("#7B68EE")
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

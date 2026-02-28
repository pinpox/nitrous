package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// selectionStyle applies reverse video for selection highlighting.
var selectionStyle = lipgloss.NewStyle().Reverse(true)

// applySelectionHighlight overlays reverse-video on the selected region
// of the viewport output.
func (m *model) applySelectionHighlight(vp string) string {
	vpLines := strings.Split(vp, "\n")

	sw := m.sidebarWidth() + sidebarBorder
	titleHeight := lipgloss.Height(m.renderTitleBar())

	// Normalize selection coordinates to viewport-local.
	sy, ey := m.selectFrom[1]-titleHeight, m.selectTo[1]-titleHeight
	sx, ex := m.selectFrom[0]-sw, m.selectTo[0]-sw
	if sy > ey || (sy == ey && sx > ex) {
		sy, ey = ey, sy
		sx, ex = ex, sx
	}

	for y := range vpLines {
		if y < sy || y > ey {
			continue
		}
		plain := ansi.Strip(vpLines[y])
		var from, to int
		if sy == ey {
			from = clampCol(sx, len(plain))
			to = clampCol(ex, len(plain))
		} else if y == sy {
			from = clampCol(sx, len(plain))
			to = len(plain)
		} else if y == ey {
			from = 0
			to = clampCol(ex, len(plain))
		} else {
			from = 0
			to = len(plain)
		}
		if from >= to {
			continue
		}
		// Rebuild line: prefix + highlighted + suffix (using plain text
		// to avoid ANSI nesting issues).
		vpLines[y] = plain[:from] + selectionStyle.Render(plain[from:to]) + plain[to:]
	}
	return strings.Join(vpLines, "\n")
}

// extractSelectedText extracts plain text from the viewport between
// the selection start and end screen coordinates.
func (m *model) extractSelectedText() string {
	content := m.viewport.View()
	vpLines := strings.Split(content, "\n")

	sw := m.sidebarWidth() + sidebarBorder
	titleHeight := lipgloss.Height(m.renderTitleBar())

	// Convert screen Y to viewport line index.
	startY := m.selectFrom[1] - titleHeight
	endY := m.selectTo[1] - titleHeight
	startX := m.selectFrom[0] - sw
	endX := m.selectTo[0] - sw

	// Normalize: start should be before end.
	if startY > endY || (startY == endY && startX > endX) {
		startY, endY = endY, startY
		startX, endX = endX, startX
	}

	if startY < 0 {
		startY = 0
		startX = 0
	}
	if endY >= len(vpLines) {
		endY = len(vpLines) - 1
	}
	if startY > endY {
		return ""
	}

	var selected []string
	for y := startY; y <= endY; y++ {
		if y < 0 || y >= len(vpLines) {
			continue
		}
		line := ansi.Strip(vpLines[y])

		if startY == endY {
			// Single line selection.
			from := clampCol(startX, len(line))
			to := clampCol(endX, len(line))
			if from < to {
				selected = append(selected, line[from:to])
			}
		} else if y == startY {
			from := clampCol(startX, len(line))
			selected = append(selected, line[from:])
		} else if y == endY {
			to := clampCol(endX, len(line))
			selected = append(selected, line[:to])
		} else {
			selected = append(selected, line)
		}
	}

	return strings.Join(selected, "\n")
}

func clampCol(x, lineLen int) int {
	if x < 0 {
		return 0
	}
	if x > lineLen {
		return lineLen
	}
	return x
}

// copyToClipboard copies text to the system clipboard.
// Tries wl-copy (Wayland), then xclip (X11), then OSC 52 escape sequence.
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		// Try wl-copy first (Wayland).
		if path, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				log.Printf("clipboard: copied %d bytes via wl-copy", len(text))
				return clipboardCopiedMsg{}
			}
		}

		// Try xclip (X11).
		if path, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.Command(path, "-selection", "clipboard")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				log.Printf("clipboard: copied %d bytes via xclip", len(text))
				return clipboardCopiedMsg{}
			}
		}

		// Try xsel (X11).
		if path, err := exec.LookPath("xsel"); err == nil {
			cmd := exec.Command(path, "--clipboard", "--input")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err == nil {
				log.Printf("clipboard: copied %d bytes via xsel", len(text))
				return clipboardCopiedMsg{}
			}
		}

		// Fallback: OSC 52 (terminal clipboard escape sequence).
		fmt.Printf("\033]52;c;%s\a", text)
		log.Printf("clipboard: sent %d bytes via OSC 52", len(text))
		return clipboardCopiedMsg{}
	}
}

type clipboardCopiedMsg struct{}

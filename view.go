package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"
	"fiatjaf.com/nostr"
)

// sidebarItemAt maps a Y coordinate to a sidebar item index.
// Returns the unified activeItem index and true if the row is a clickable item,
// or 0 and false if it's a section header or out of bounds.
func (m *model) sidebarItemAt(y int) (int, bool) {
	row := 0
	// CHANNELS header
	row++ // "CHANNELS"
	for i, it := range m.sidebar {
		if it.Kind() != SidebarChannel {
			break
		}
		if y == row {
			return i, true
		}
		row++
	}
	// GROUPS header
	row++ // "GROUPS"
	for i, it := range m.sidebar {
		if it.Kind() != SidebarGroup {
			continue
		}
		if y == row {
			return i, true
		}
		row++
	}
	// DMS header
	row++ // "DMS"
	for i, it := range m.sidebar {
		if it.Kind() != SidebarDM {
			continue
		}
		if y == row {
			return i, true
		}
		row++
	}
	return 0, false
}

func (m *model) sidebarWidth() int {
	longest := 0
	for _, it := range m.sidebar {
		if n := lipgloss.Width(it.DisplayName()); n > longest {
			longest = n
		}
	}
	w := longest + sidebarPadding
	if w < minSidebarWidth {
		w = minSidebarWidth
	}
	return w
}

// renderTitleBar returns the rendered title bar for the current selection.
func (m *model) renderTitleBar() string {
	var title string
	if item := m.activeSidebarItem(); item != nil {
		title = item.Prefix() + item.DisplayName()
	}
	return lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Padding(0, 1).Render(title)
}

func (m *model) updateLayout() {
	contentWidth := m.width - m.sidebarWidth() - sidebarBorder
	if contentWidth < 10 {
		contentWidth = 10
	}

	// Set widths first so measured heights are accurate.
	m.viewport.Width = contentWidth
	m.input.SetWidth(contentWidth)

	// Measure fixed-height components dynamically.
	titleHeight := lipgloss.Height(m.renderTitleBar())
	statusHeight := lipgloss.Height(m.viewStatusBar())
	inputHeight := lipgloss.Height(m.input.View())
	acHeight := 0
	if len(m.acSuggestions) > 0 {
		acHeight = lipgloss.Height(m.viewAutocomplete())
	}

	contentHeight := m.height - titleHeight - statusHeight - inputHeight - acHeight
	if contentHeight < 1 {
		contentHeight = 1
	}

	m.viewport.Height = contentHeight
	m.updateViewport()
}

func (m *model) updateViewport() {
	m.clearUnread()
	var msgs []ChatMessage
	if item := m.activeSidebarItem(); item != nil {
		msgs = m.msgs[item.ItemID()]
	} else {
		msgs = m.globalMsgs
	}

	var lines []string
	for _, msg := range msgs {
		if msg.Author == "system" {
			lines = append(lines, chatSystemStyle.Render("  "+msg.Content))
			continue
		}
		var authorStyle lipgloss.Style
		if msg.IsMine {
			authorStyle = chatOwnAuthorStyle
		} else if msg.PubKey != "" {
			authorStyle = lipgloss.NewStyle().Foreground(colorForPubkey(msg.PubKey)).Bold(true)
		} else {
			authorStyle = chatAuthorStyle
		}
		displayName := msg.Author
		if msg.PubKey != "" {
			if msg.IsMine {
				displayName = m.resolveAuthor(m.keys.PK.Hex())
			} else {
				displayName = m.resolveAuthor(msg.PubKey)
			}
		}
		ts := chatTimestampStyle.Render(msg.Timestamp.Time().Format("15:04"))
		author := authorStyle.Render(displayName)
		// Convert single newlines to paragraph breaks for glamour.
		mdContent := strings.ReplaceAll(msg.Content, "\n", "\n\n")
		// Replace single newlines with double, then collapse runs of 3+ into double.
		for strings.Contains(mdContent, "\n\n\n") {
			mdContent = strings.ReplaceAll(mdContent, "\n\n\n", "\n\n")
		}
		content := renderMarkdown(m.mdRender, mdContent)
		prefix := fmt.Sprintf("%s %s: ", ts, author)
		prefixW := lipgloss.Width(prefix)
		pad := strings.Repeat(" ", prefixW)
		wrapWidth := m.viewport.Width - prefixW
		if wrapWidth < 1 {
			wrapWidth = 1
		}
		// Trim leading/trailing blank lines from glamour output.
		// strings.TrimSpace can't handle ANSI codes, and lipgloss.Width
		// counts indentation spaces as visible. Strip ANSI first, then
		// check for whitespace-only content.
		rawLines := strings.Split(content, "\n")
		for len(rawLines) > 0 && strings.TrimSpace(ansi.Strip(rawLines[0])) == "" {
			rawLines = rawLines[1:]
		}
		for len(rawLines) > 0 && strings.TrimSpace(ansi.Strip(rawLines[len(rawLines)-1])) == "" {
			rawLines = rawLines[:len(rawLines)-1]
		}
		// Word-wrap at word boundaries, then hard-wrap any remaining
		// overflows (long unbroken words like URLs).
		var contentLines []string
		for _, cl := range rawLines {
			wrapped := wordwrap.String(cl, wrapWidth)
			for _, wl := range strings.Split(wrapped, "\n") {
				if lipgloss.Width(wl) > wrapWidth {
					contentLines = append(contentLines, strings.Split(wrap.String(wl, wrapWidth), "\n")...)
				} else {
					contentLines = append(contentLines, wl)
				}
			}
		}
		if len(contentLines) == 0 {
			contentLines = []string{""}
		}
		first := prefix + contentLines[0]
		lines = append(lines, first)
		for _, cl := range contentLines[1:] {
			lines = append(lines, pad+cl)
		}
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.GotoBottom()
}

func (m *model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	if m.qrOverlay != "" {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.qrOverlay)
	}

	sidebar := m.viewSidebar()
	content := m.viewContent()
	statusBar := m.viewStatusBar()

	mainArea := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)

	return lipgloss.JoinVertical(lipgloss.Left, mainArea, statusBar)
}

func (m *model) viewSidebar() string {
	contentHeight := m.height - lipgloss.Height(m.viewStatusBar())
	sw := m.sidebarWidth()
	var items []string

	// CHANNELS section
	items = append(items, sidebarSectionStyle.Render("CHANNELS"))
	for i, it := range m.sidebar {
		if it.Kind() != SidebarChannel {
			break
		}
		name := it.Prefix() + it.DisplayName()
		if lipgloss.Width(name) > sw-2 {
			name = ansi.Truncate(name, sw-2, "")
		}
		if i == m.activeItem {
			items = append(items, sidebarSelectedStyle.Render(name))
		} else if m.unread[it.ItemID()] {
			items = append(items, sidebarUnreadStyle.Render(name))
		} else {
			items = append(items, sidebarItemStyle.Render(name))
		}
	}

	// GROUPS section
	items = append(items, sidebarSectionStyle.Render("GROUPS"))
	for i, it := range m.sidebar {
		if it.Kind() != SidebarGroup {
			continue
		}
		name := it.Prefix() + it.DisplayName()
		if lipgloss.Width(name) > sw-2 {
			name = ansi.Truncate(name, sw-2, "")
		}
		if i == m.activeItem {
			items = append(items, sidebarSelectedStyle.Render(name))
		} else if m.unread[it.ItemID()] {
			items = append(items, sidebarUnreadStyle.Render(name))
		} else {
			items = append(items, sidebarItemStyle.Render(name))
		}
	}

	// DMS section
	items = append(items, sidebarSectionStyle.Render("DMS"))
	for i, it := range m.sidebar {
		if it.Kind() != SidebarDM {
			continue
		}
		name := it.Prefix() + it.DisplayName()
		if lipgloss.Width(name) > sw-2 {
			name = ansi.Truncate(name, sw-2, "")
		}
		if i == m.activeItem {
			items = append(items, sidebarSelectedStyle.Render(name))
		} else if m.unread[it.ItemID()] {
			items = append(items, sidebarUnreadStyle.Render(name))
		} else {
			items = append(items, sidebarItemStyle.Render(name))
		}
	}

	content := strings.Join(items, "\n")

	return sidebarStyle.Width(sw).Height(contentHeight).MaxHeight(contentHeight).Render(content)
}

func (m *model) viewContent() string {
	totalHeight := m.height - lipgloss.Height(m.viewStatusBar())

	titleBar := m.renderTitleBar()
	inputView := m.input.View()
	vp := m.viewport.View()

	var inner string
	if len(m.acSuggestions) > 0 {
		acView := m.viewAutocomplete()
		inner = lipgloss.JoinVertical(lipgloss.Left, titleBar, vp, acView, inputView)
	} else {
		inner = lipgloss.JoinVertical(lipgloss.Left, titleBar, vp, inputView)
	}

	return lipgloss.NewStyle().Height(totalHeight).MaxHeight(totalHeight).Render(inner)
}

func (m *model) connectedRelayCount() int {
	count := 0
	m.pool.Relays.Range(func(_ string, relay *nostr.Relay) bool {
		if relay.IsConnected() {
			count++
		}
		return true
	})
	return count
}

func (m *model) viewStatusBar() string {
	connected := m.connectedRelayCount()
	total := len(m.relays)
	bar := statusConnectedStyle.Render(fmt.Sprintf("● %d/%d relays", connected, total))
	return statusBarStyle.Width(m.width).Render(bar)
}

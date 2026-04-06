package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fiatjaf.com/nostr"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"
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
	return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Primary).Padding(0, 1).Render(title)
}

func (m *model) updateLayout() {
	contentWidth := m.width - m.sidebarWidth() - sidebarBorder
	if contentWidth < 10 {
		contentWidth = 10
	}

	// Set widths first so measured heights are accurate.
	m.viewport.SetWidth(contentWidth)
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

	m.viewport.SetHeight(contentHeight)
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

	// First pass: resolve display names and find the longest one for alignment.
	type resolvedMsg struct {
		msg         ChatMessage
		displayName string
	}
	var resolved []resolvedMsg
	maxNameW := 0
	for _, msg := range msgs {
		if msg.Author == "system" {
			resolved = append(resolved, resolvedMsg{msg: msg})
			continue
		}
		displayName := msg.Author
		if msg.PubKey != "" {
			if msg.IsMine {
				displayName = m.resolveAuthor(m.keys.PK.Hex())
			} else {
				displayName = m.resolveAuthor(msg.PubKey)
			}
		}
		nameW := lipgloss.Width(displayName)
		if nameW > maxNameW {
			maxNameW = nameW
		}
		resolved = append(resolved, resolvedMsg{msg: msg, displayName: displayName})
	}

	var lines []string
	for _, rm := range resolved {
		msg := rm.msg
		if msg.Author == "system" {
			lines = append(lines, m.theme.ChatSystem.Render("  "+msg.Content))
			continue
		}
		var authorStyle lipgloss.Style
		if msg.IsMine {
			authorStyle = m.theme.ChatOwnAuthor
		} else if msg.PubKey != "" {
			authorStyle = lipgloss.NewStyle().Foreground(colorForPubkey(msg.PubKey, m.theme.AuthorColors)).Bold(true)
		} else {
			authorStyle = m.theme.ChatAuthor
		}
		displayName := rm.displayName
		// Right-align the name to the colon (weechat-style).
		nameW := lipgloss.Width(displayName)
		namePad := ""
		if nameW < maxNameW {
			namePad = strings.Repeat(" ", maxNameW-nameW)
		}
		ts := m.theme.ChatTimestamp.Render(msg.Timestamp.Time().Format("15:04"))
		author := namePad + authorStyle.Render(displayName)
		// Convert single newlines to paragraph breaks for glamour,
		// but leave newlines inside fenced code blocks untouched.
		mentionResolved, mentionNames := renderMentions(msg.Content, m.profiles)
		mdContent := doubleNewlinesOutsideCode(mentionResolved)
		content := styleMentions(renderMarkdown(m.mdRender, mdContent), mentionNames, m.theme.ChatMention)
		prefix := fmt.Sprintf("%s %s: ", ts, author)
		prefixW := lipgloss.Width(prefix)
		pad := strings.Repeat(" ", prefixW)
		wrapWidth := m.viewport.Width() - prefixW
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
		// overflows (long unbroken words like URLs) at the full viewport
		// width so continuation lines aren't indented under the author prefix.
		fullWidth := m.viewport.Width()
		type cLine struct {
			text     string
			hardWrap bool // true = from hard-wrapping a long token (no prefix pad)
		}
		var contentLines []cLine
		for _, cl := range rawLines {
			wrapped := wordwrap.String(cl, wrapWidth)
			for _, wl := range strings.Split(wrapped, "\n") {
				if lipgloss.Width(wl) > wrapWidth {
					hardWrapped := strings.Split(wrap.String(wl, fullWidth), "\n")
					for i, hw := range hardWrapped {
						contentLines = append(contentLines, cLine{text: hw, hardWrap: i > 0})
					}
				} else {
					contentLines = append(contentLines, cLine{text: wl})
				}
			}
		}
		if len(contentLines) == 0 {
			contentLines = []cLine{{text: ""}}
		}
		first := prefix + contentLines[0].text
		lines = append(lines, first)
		for _, cl := range contentLines[1:] {
			if cl.hardWrap {
				lines = append(lines, cl.text)
			} else {
				lines = append(lines, pad+cl.text)
			}
		}
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.GotoBottom()
}

func (m *model) View() tea.View {
	v := tea.NewView(m.viewString())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	return v
}

func (m *model) viewString() string {
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
	baseView := lipgloss.JoinVertical(lipgloss.Left, mainArea, statusBar)

	// Show channel selector popup on top if active
	if m.showChannelSelector {
		popup := m.viewChannelSelector()
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, popup)
	}

	return baseView
}

func (m *model) viewSidebar() string {
	contentHeight := m.height - lipgloss.Height(m.viewStatusBar())
	sw := m.sidebarWidth()
	var items []string

	// CHANNELS section
	items = append(items, m.theme.SidebarSection.Render("CHANNELS"))
	for i, it := range m.sidebar {
		if it.Kind() != SidebarChannel {
			break
		}
		name := it.Prefix() + it.DisplayName()
		if lipgloss.Width(name) > sw-2 {
			name = ansi.Truncate(name, sw-2, "")
		}
		if i == m.activeItem {
			items = append(items, m.theme.SidebarSelected.Render(name))
		} else if m.unread[it.ItemID()] {
			items = append(items, m.theme.SidebarUnread.Render(name))
		} else {
			items = append(items, m.theme.SidebarItem.Render(name))
		}
	}

	// GROUPS section
	items = append(items, m.theme.SidebarSection.Render("GROUPS"))
	for i, it := range m.sidebar {
		if it.Kind() != SidebarGroup {
			continue
		}
		name := it.Prefix() + it.DisplayName()
		if lipgloss.Width(name) > sw-2 {
			name = ansi.Truncate(name, sw-2, "")
		}
		if i == m.activeItem {
			items = append(items, m.theme.SidebarSelected.Render(name))
		} else if m.unread[it.ItemID()] {
			items = append(items, m.theme.SidebarUnread.Render(name))
		} else {
			items = append(items, m.theme.SidebarItem.Render(name))
		}
	}

	// DMS section
	items = append(items, m.theme.SidebarSection.Render("DMS"))
	for i, it := range m.sidebar {
		if it.Kind() != SidebarDM {
			continue
		}
		name := it.Prefix() + it.DisplayName()
		if lipgloss.Width(name) > sw-2 {
			name = ansi.Truncate(name, sw-2, "")
		}
		if i == m.activeItem {
			items = append(items, m.theme.SidebarSelected.Render(name))
		} else if m.unread[it.ItemID()] {
			items = append(items, m.theme.SidebarUnread.Render(name))
		} else {
			items = append(items, m.theme.SidebarItem.Render(name))
		}
	}

	content := strings.Join(items, "\n")

	return m.theme.Sidebar.Width(sw).Height(contentHeight).MaxHeight(contentHeight).Render(content)
}

func (m *model) viewContent() string {
	totalHeight := m.height - lipgloss.Height(m.viewStatusBar())

	titleBar := m.renderTitleBar()
	inputView := m.input.View()
	vp := m.viewport.View()

	if m.selecting {
		vp = m.applySelectionHighlight(vp)
	}

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
	bar := m.theme.StatusConnected.Render(fmt.Sprintf("● %d/%d relays", connected, total))
	return m.theme.StatusBar.Width(m.width).Render(bar)
}

// doubleNewlinesOutsideCode doubles single newlines for markdown paragraph
// breaks, but preserves newlines inside fenced code blocks (``` ... ```).
func doubleNewlinesOutsideCode(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	lines := strings.Split(s, "\n")
	inCode := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		}
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
			if !inCode {
				b.WriteByte('\n')
			}
		}
	}
	// Collapse runs of 3+ newlines into double.
	result := b.String()
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

// viewChannelSelector renders the channel/DM selector popup.
func (m *model) viewChannelSelector() string {
	width := 60
	height := 20
	maxItems := height - 4 // Account for borders and input

	// Build the popup content
	var items []string
	items = append(items, fmt.Sprintf("Go to: %s█", m.channelSelectorInput))
	items = append(items, "")

	// Show filtered items
	start := 0
	end := len(m.channelSelectorItems)

	// Scroll if there are more items than can fit
	if len(m.channelSelectorItems) > maxItems {
		if m.channelSelectorIndex >= maxItems/2 {
			start = m.channelSelectorIndex - maxItems/2
			end = start + maxItems
			if end > len(m.channelSelectorItems) {
				end = len(m.channelSelectorItems)
				start = end - maxItems
			}
		} else {
			end = maxItems
		}
	}

	for i := start; i < end; i++ {
		if i >= len(m.channelSelectorItems) {
			break
		}
		item := m.channelSelectorItems[i]
		prefix := item.Prefix()
		name := item.DisplayName()
		line := fmt.Sprintf("%s%s", prefix, name)

		if i == m.channelSelectorIndex {
			line = m.theme.SidebarSelected.Render(line)
		}
		items = append(items, line)
	}

	// Pad to minimum height
	for len(items) < height-2 {
		items = append(items, "")
	}

	content := strings.Join(items, "\n")

	// Style the popup with border
	popup := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Width(width).
		Height(height).
		Padding(1).
		Render(content)

	return popup
}

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type focus int

const (
	focusSidebar focus = iota
	focusInput
)

type sidebarTab int

const (
	tabChannels sidebarTab = iota
	tabDMs
)

type modalMode int

const (
	modalNone modalMode = iota
	modalCreateChannel
	modalNewDM
)

type model struct {
	// Config and keys
	cfg    Config
	keys   Keys
	pool   *nostr.SimplePool
	relays []string

	// TUI dimensions
	width  int
	height int

	// Focus and navigation
	focus       focus
	tab         sidebarTab
	sidebarIdx  int
	modal       modalMode
	modalInput  textarea.Model

	// Channels
	channels       []Channel
	activeChannel  int
	channelMsgs    map[string][]ChatMessage
	channelEvents  <-chan nostr.RelayEvent
	channelCancel  context.CancelFunc

	// DMs
	dmPeers       []string // pubkeys of DM peers
	activeDMPeer  int
	dmMsgs        map[string][]ChatMessage
	dmEvents      <-chan nostr.RelayEvent
	dmCancel      context.CancelFunc

	// Components
	viewport viewport.Model
	input    textarea.Model
	mdRender *glamour.TermRenderer

	// Status
	statusMsg string
	errMsg    string
}

func newModel(cfg Config, keys Keys, pool *nostr.SimplePool) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = "> "
	ta.CharLimit = 2000
	ta.MaxHeight = inputHeight
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.Blur()

	vp := viewport.New(80, 20)

	modalTA := textarea.New()
	modalTA.Placeholder = "Enter name..."
	modalTA.CharLimit = 200
	modalTA.MaxHeight = 1
	modalTA.ShowLineNumbers = false

	return model{
		cfg:         cfg,
		keys:        keys,
		pool:        pool,
		relays:      cfg.Relays,
		focus:       focusSidebar,
		tab:         tabChannels,
		channelMsgs: make(map[string][]ChatMessage),
		dmMsgs:      make(map[string][]ChatMessage),
		dmPeers:     []string{},
		viewport:    vp,
		input:       ta,
		modalInput:  modalTA,
		mdRender:    newMarkdownRenderer(60),
		statusMsg:   fmt.Sprintf("connected to %d relays", len(cfg.Relays)),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		fetchChannels(m.pool, m.relays),
		m.startDMSubscription(),
	)
}

func (m *model) startDMSubscription() tea.Cmd {
	if m.dmCancel != nil {
		m.dmCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.dmCancel = cancel

	ch := m.pool.SubscribeMany(ctx, m.relays, nostr.Filter{
		Kinds: []int{4},
		Tags:  nostr.TagMap{"p": {m.keys.PK}},
		Limit: 50,
	})
	m.dmEvents = ch

	return waitForDMEvent(ch, m.keys)
}

func (m *model) startChannelSubscription(channelID string) tea.Cmd {
	if m.channelCancel != nil {
		m.channelCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.channelCancel = cancel

	ch := m.pool.SubscribeMany(ctx, m.relays, nostr.Filter{
		Kinds: []int{42},
		Tags:  nostr.TagMap{"e": {channelID}},
		Limit: 50,
	})
	m.channelEvents = ch

	return waitForChannelEvent(ch, m.keys)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		return m, nil

	case channelsLoadedMsg:
		// Append or replace channels
		if m.channels == nil {
			m.channels = []Channel(msg)
		} else {
			m.channels = append(m.channels, []Channel(msg)...)
		}
		// Auto-open first channel if none active
		if len(m.channels) > 0 && m.channelEvents == nil {
			m.activeChannel = 0
			cmd := m.startChannelSubscription(m.channels[0].ID)
			return m, cmd
		}
		return m, nil

	case channelEventMsg:
		cm := ChatMessage(msg)
		if len(m.channels) > 0 {
			chID := m.channels[m.activeChannel].ID
			m.channelMsgs[chID] = appendMessage(m.channelMsgs[chID], cm, m.cfg.MaxMessages)
			m.updateViewport()
		}
		// Re-chain to keep listening
		if m.channelEvents != nil {
			return m, waitForChannelEvent(m.channelEvents, m.keys)
		}
		return m, nil

	case dmEventMsg:
		cm := ChatMessage(msg)
		peer := cm.Author
		if cm.IsMine {
			// Shouldn't happen for incoming DMs, but handle it
			peer = cm.Author
		}
		m.dmMsgs[peer] = appendMessage(m.dmMsgs[peer], cm, m.cfg.MaxMessages)
		// Add peer to list if new
		if !containsStr(m.dmPeers, peer) {
			m.dmPeers = append(m.dmPeers, peer)
		}
		if m.tab == tabDMs {
			m.updateViewport()
		}
		// Re-chain DM listener
		if m.dmEvents != nil {
			return m, waitForDMEvent(m.dmEvents, m.keys)
		}
		return m, nil

	case publishedMsg:
		return m, nil

	case nostrErrMsg:
		m.errMsg = msg.Error()
		return m, nil

	case tea.KeyMsg:
		// Handle modal input first
		if m.modal != modalNone {
			return m.handleModalKey(msg)
		}

		switch msg.String() {
		case "ctrl+c":
			if m.channelCancel != nil {
				m.channelCancel()
			}
			if m.dmCancel != nil {
				m.dmCancel()
			}
			return m, tea.Quit

		case "tab":
			if m.focus == focusSidebar {
				if m.tab == tabChannels {
					m.tab = tabDMs
				} else {
					m.tab = tabChannels
				}
				m.sidebarIdx = 0
				m.updateViewport()
			}
			return m, nil

		case "ctrl+n":
			m.modal = modalCreateChannel
			m.modalInput.Placeholder = "Channel name..."
			m.modalInput.Reset()
			m.modalInput.Focus()
			return m, nil

		case "ctrl+d":
			m.modal = modalNewDM
			m.modalInput.Placeholder = "Recipient npub..."
			m.modalInput.Reset()
			m.modalInput.Focus()
			return m, nil

		case "esc":
			if m.focus == focusInput {
				m.focus = focusSidebar
				m.input.Blur()
			}
			return m, nil

		case "enter":
			if m.focus == focusSidebar {
				m.focus = focusInput
				m.input.Focus()
				// Open selected channel/DM
				if m.tab == tabChannels && len(m.channels) > 0 {
					m.activeChannel = m.sidebarIdx
					cmd := m.startChannelSubscription(m.channels[m.activeChannel].ID)
					m.updateViewport()
					return m, cmd
				}
				return m, nil
			}
			// Send message
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()

			if m.tab == tabChannels && len(m.channels) > 0 {
				chID := m.channels[m.activeChannel].ID
				return m, publishChannelMessage(m.pool, m.relays, chID, text, m.keys)
			}
			if m.tab == tabDMs && len(m.dmPeers) > 0 {
				peer := m.dmPeers[m.activeDMPeer]
				return m, sendDM(m.pool, m.relays, peer, text, m.keys)
			}
			return m, nil

		case "up", "k":
			if m.focus == focusSidebar {
				if m.sidebarIdx > 0 {
					m.sidebarIdx--
				}
				return m, nil
			}

		case "down", "j":
			if m.focus == focusSidebar {
				max := m.sidebarLen() - 1
				if max < 0 {
					max = 0
				}
				if m.sidebarIdx < max {
					m.sidebarIdx++
				}
				return m, nil
			}

		case "pgup":
			m.viewport.ScrollUp(10)
			return m, nil

		case "pgdown":
			m.viewport.ScrollDown(10)
			return m, nil
		}
	}

	// Pass remaining keys to textarea when focused
	if m.focus == focusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.modal = modalNone
		m.modalInput.Blur()
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.modalInput.Value())
		mode := m.modal
		m.modal = modalNone
		m.modalInput.Blur()
		if val == "" {
			return m, nil
		}

		switch mode {
		case modalCreateChannel:
			return m, createChannel(m.pool, m.relays, val, m.keys)
		case modalNewDM:
			// Decode npub to hex pubkey
			pk := val
			if strings.HasPrefix(val, "npub") {
				prefix, decoded, err := nip19.Decode(val)
				if err != nil || prefix != "npub" {
					m.errMsg = "invalid npub"
					return m, nil
				}
				pk = decoded.(string)
			}
			if !containsStr(m.dmPeers, pk) {
				m.dmPeers = append(m.dmPeers, pk)
			}
			m.tab = tabDMs
			for i, p := range m.dmPeers {
				if p == pk {
					m.activeDMPeer = i
					m.sidebarIdx = i
					break
				}
			}
			m.focus = focusInput
			m.input.Focus()
			m.updateViewport()
			return m, nil
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.modalInput, cmd = m.modalInput.Update(msg)
	return m, cmd
}

func (m *model) updateLayout() {
	contentWidth := m.width - sidebarWidth - 2 // border
	contentHeight := m.height - headerHeight - statusHeight - inputHeight - 2

	if contentWidth < 10 {
		contentWidth = 10
	}
	if contentHeight < 1 {
		contentHeight = 1
	}

	m.viewport.Width = contentWidth
	m.viewport.Height = contentHeight
	m.input.SetWidth(contentWidth)
	m.mdRender = newMarkdownRenderer(contentWidth - 4)
}

func (m *model) updateViewport() {
	var msgs []ChatMessage
	if m.tab == tabChannels && len(m.channels) > 0 {
		chID := m.channels[m.activeChannel].ID
		msgs = m.channelMsgs[chID]
	} else if m.tab == tabDMs && len(m.dmPeers) > 0 {
		peer := m.dmPeers[m.activeDMPeer]
		msgs = m.dmMsgs[peer]
	}

	var lines []string
	for _, msg := range msgs {
		authorStyle := chatAuthorStyle
		if msg.IsMine {
			authorStyle = chatOwnAuthorStyle
		}
		ts := chatTimestampStyle.Render(msg.Timestamp.Time().Format("15:04"))
		author := authorStyle.Render(msg.Author)
		content := strings.TrimSpace(renderMarkdown(m.mdRender, msg.Content))
		line := fmt.Sprintf("%s %s: %s", ts, author, content)
		lines = append(lines, line)
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.GotoBottom()
}

func (m model) sidebarLen() int {
	if m.tab == tabChannels {
		return len(m.channels)
	}
	return len(m.dmPeers)
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Modal overlay
	if m.modal != modalNone {
		return m.viewModal()
	}

	header := m.viewHeader()
	sidebar := m.viewSidebar()
	content := m.viewContent()
	statusBar := m.viewStatusBar()

	// Join sidebar and content horizontally
	mainArea := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)

	return lipgloss.JoinVertical(lipgloss.Left, header, mainArea, statusBar)
}

func (m model) viewHeader() string {
	channelsTab := tabInactiveStyle.Render("[channels]")
	dmsTab := tabInactiveStyle.Render("[dms]")
	if m.tab == tabChannels {
		channelsTab = tabActiveStyle.Render("[channels]")
	} else {
		dmsTab = tabActiveStyle.Render("[dms]")
	}

	title := headerStyle.Render("nitrous")
	tabs := channelsTab + " " + dmsTab

	gap := m.width - lipgloss.Width(title) - lipgloss.Width(tabs) - 2
	if gap < 0 {
		gap = 0
	}

	return title + strings.Repeat(" ", gap) + tabs
}

func (m model) viewSidebar() string {
	contentHeight := m.height - headerHeight - statusHeight

	var items []string
	if m.tab == tabChannels {
		for i, ch := range m.channels {
			name := "#" + ch.Name
			if len(name) > sidebarWidth-2 {
				name = name[:sidebarWidth-2]
			}
			if i == m.sidebarIdx && m.focus == focusSidebar {
				items = append(items, sidebarSelectedStyle.Render(name))
			} else {
				items = append(items, sidebarItemStyle.Render(name))
			}
		}
	} else {
		for i, peer := range m.dmPeers {
			name := "@" + peer
			if len(name) > sidebarWidth-2 {
				name = name[:sidebarWidth-2]
			}
			if i == m.sidebarIdx && m.focus == focusSidebar {
				items = append(items, sidebarSelectedStyle.Render(name))
			} else {
				items = append(items, sidebarItemStyle.Render(name))
			}
		}
	}

	content := strings.Join(items, "\n")
	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	if content == "" {
		lines = 0
	}
	for lines < contentHeight {
		content += "\n"
		lines++
	}

	return sidebarStyle.Height(contentHeight).Render(content)
}

func (m model) viewContent() string {
	contentHeight := m.height - headerHeight - statusHeight - inputHeight

	// Room title
	var title string
	if m.tab == tabChannels && len(m.channels) > 0 {
		title = "#" + m.channels[m.activeChannel].Name
	} else if m.tab == tabDMs && len(m.dmPeers) > 0 {
		title = "@" + m.dmPeers[m.activeDMPeer]
	}

	vp := m.viewport.View()

	// Pad viewport to fill height
	vpLines := strings.Count(vp, "\n") + 1
	for vpLines < contentHeight-1 {
		vp += "\n"
		vpLines++
	}

	titleBar := headerStyle.Render(title)
	inputView := m.input.View()

	return lipgloss.JoinVertical(lipgloss.Left, titleBar, vp, inputView)
}

func (m model) viewStatusBar() string {
	left := statusConnectedStyle.Render(fmt.Sprintf("● %d relays", len(m.relays)))
	npub := m.keys.NPub
	if len(npub) > 20 {
		npub = npub[:20] + "..."
	}
	right := npub

	if m.errMsg != "" {
		left = statusErrorStyle.Render("✗ " + m.errMsg)
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 0 {
		gap = 0
	}

	bar := left + strings.Repeat(" ", gap) + right
	return statusBarStyle.Width(m.width).Render(bar)
}

func (m model) viewModal() string {
	var title string
	switch m.modal {
	case modalCreateChannel:
		title = "Create Channel"
	case modalNewDM:
		title = "New DM (enter npub)"
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		headerStyle.Render(title),
		"",
		m.modalInput.View(),
		"",
		chatTimestampStyle.Render("Enter to confirm · Esc to cancel"),
	)

	modal := modalStyle.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
}

func appendMessage(msgs []ChatMessage, msg ChatMessage, maxMessages int) []ChatMessage {
	msgs = append(msgs, msg)
	if len(msgs) > maxMessages {
		msgs = msgs[len(msgs)-maxMessages:]
	}
	return msgs
}

func containsStr(sl []string, s string) bool {
	for _, v := range sl {
		if v == s {
			return true
		}
	}
	return false
}

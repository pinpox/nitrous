package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type sidebarTab int

const (
	tabChannels sidebarTab = iota
	tabDMs
)

type model struct {
	// Config and keys
	cfg         Config
	cfgFlagPath string
	keys        Keys
	pool        *nostr.SimplePool
	relays      []string
	rooms       []Room // from rooms file

	// TUI dimensions
	width  int
	height int

	// Navigation
	tab sidebarTab

	// Channels
	channels      []Channel
	activeChannel int
	channelMsgs   map[string][]ChatMessage
	channelSubID  string // ID of the channel we're subscribed to
	channelEvents <-chan nostr.RelayEvent
	channelCancel context.CancelFunc

	// DMs
	dmPeers      []string // pubkeys of DM peers
	activeDMPeer int
	dmMsgs       map[string][]ChatMessage
	dmEvents     <-chan nostr.RelayEvent
	dmCancel     context.CancelFunc

	// Components
	viewport viewport.Model
	input    textarea.Model
	mdRender *glamour.TermRenderer
	mdStyle  string

	// Global messages (shown when no channel/DM is active)
	globalMsgs []ChatMessage

	// Dedup
	seenEvents map[string]bool

	// Profile resolution (NIP-01 kind 0)
	profiles       map[string]string // pubkey -> display name
	profilePending map[string]bool   // pubkeys with in-flight fetches

	// Input tracking
	lastInputHeight int

	// Status
	statusMsg string
}

func newModel(cfg Config, cfgFlagPath string, keys Keys, pool *nostr.SimplePool, rooms []Room, mdRender *glamour.TermRenderer, mdStyle string) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (/help for commands)"
	ta.Prompt = "> "
	ta.CharLimit = 2000
	ta.SetHeight(inputMinHeight)
	ta.MaxHeight = inputMaxHeight
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ta.Focus()

	vp := viewport.New(80, 20)

	// Populate channels from rooms file
	var channels []Channel
	for _, r := range rooms {
		channels = append(channels, Channel{ID: r.ID, Name: r.Name})
	}

	// Pre-cache own display name from config fallback chain.
	ownName := shortPK(keys.PK)
	if cfg.Profile.DisplayName != "" {
		ownName = cfg.Profile.DisplayName
	} else if cfg.Profile.Name != "" {
		ownName = cfg.Profile.Name
	} else if cfg.DisplayName != "" {
		ownName = cfg.DisplayName
	}

	profiles := map[string]string{keys.PK: ownName}

	return model{
		cfg:         cfg,
		cfgFlagPath: cfgFlagPath,
		keys:        keys,
		pool:        pool,
		relays:      cfg.Relays,
		rooms:       rooms,
		width:  80,
		height: 24,
		tab:    tabChannels,
		channels:       channels,
		channelMsgs:    make(map[string][]ChatMessage),
		dmMsgs:         make(map[string][]ChatMessage),
		dmPeers:        []string{},
		seenEvents:     make(map[string]bool),
		profiles:        profiles,
		profilePending:  make(map[string]bool),
		lastInputHeight: inputMinHeight,
		viewport:        vp,
		input:          ta,
		mdRender:       mdRender,
		mdStyle:        mdStyle,
		statusMsg:      fmt.Sprintf("connected to %d relays", len(cfg.Relays)),
	}
}

func (m *model) Init() tea.Cmd {
	log.Println("Init() called")
	m.addSystemMsg("nitrous — nostr chat")
	m.addSystemMsg(fmt.Sprintf("npub: %s", m.keys.NPub))
	for _, r := range m.relays {
		m.addSystemMsg(fmt.Sprintf("connecting to %s ...", r))
	}
	if len(m.channels) > 0 {
		m.addSystemMsg(fmt.Sprintf("joining #%s ...", m.channels[0].Name))
	} else {
		m.addSystemMsg("no rooms configured — use /create #name or /join <event-id>")
	}

	cmds := []tea.Cmd{
		textarea.Blink,
		subscribeDMCmd(m.pool, m.relays, m.keys.PK),
	}
	if len(m.channels) > 0 {
		cmds = append(cmds, subscribeChannelCmd(m.pool, m.relays, m.channels[0].ID))
	}
	if m.cfg.Profile.Name != "" || m.cfg.Profile.DisplayName != "" || m.cfg.Profile.About != "" || m.cfg.Profile.Picture != "" {
		cmds = append(cmds, publishProfileCmd(m.pool, m.relays, m.cfg.Profile, m.keys))
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		log.Printf("WindowSizeMsg: %dx%d", msg.Width, msg.Height)
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		return m, nil

	case channelCreatedMsg:
		log.Printf("channelCreatedMsg: id=%s name=%q", msg.ID, msg.Name)
		m.channels = append(m.channels, Channel{ID: msg.ID, Name: msg.Name})
		m.tab = tabChannels
		m.activeChannel = len(m.channels) - 1
		room := Room{Name: msg.Name, ID: msg.ID}
		if err := AppendRoom(m.cfgFlagPath, room); err != nil {
			log.Printf("channelCreatedMsg: failed to save room: %v", err)
			m.addSystemMsg("failed to save room: " + err.Error())
		} else {
			m.rooms = append(m.rooms, room)
		}
		m.updateViewport()
		return m, subscribeChannelCmd(m.pool, m.relays, msg.ID)

	case channelMetaMsg:
		log.Printf("channelMetaMsg: id=%s name=%q", msg.ID, msg.Name)
		// Update the channel name in our list
		for i, ch := range m.channels {
			if ch.ID == msg.ID {
				m.channels[i].Name = msg.Name
				break
			}
		}
		// Save to rooms file
		room := Room{Name: msg.Name, ID: msg.ID}
		if err := AppendRoom(m.cfgFlagPath, room); err != nil {
			log.Printf("channelMetaMsg: failed to save room: %v", err)
			m.addSystemMsg("failed to save room: " + err.Error())
		} else {
			m.rooms = append(m.rooms, room)
		}
		return m, nil

	case channelSubStartedMsg:
		log.Println("channelSubStartedMsg received")
		if m.channelCancel != nil {
			m.channelCancel()
		}
		m.channelSubID = msg.channelID
		m.channelEvents = msg.events
		m.channelCancel = msg.cancel
		if len(m.channels) > 0 {
			m.addSystemMsg("subscribed to #" + m.channels[m.activeChannel].Name)
		}
		return m, waitForChannelEvent(m.channelEvents, m.channelSubID, m.keys)

	case dmSubStartedMsg:
		log.Println("dmSubStartedMsg received")
		if m.dmCancel != nil {
			m.dmCancel()
		}
		m.dmEvents = msg.events
		m.dmCancel = msg.cancel
		m.addSystemMsg("DM subscription active")
		return m, waitForDMEvent(m.dmEvents, m.keys)

	case channelEventMsg:
		cm := ChatMessage(msg)
		log.Printf("channelEventMsg: author=%s channel=%s id=%s", cm.Author, cm.ChannelID, cm.EventID)
		if m.seenEvents[cm.EventID] {
			if m.channelEvents != nil {
				return m, waitForChannelEvent(m.channelEvents, m.channelSubID, m.keys)
			}
			return m, nil
		}
		m.seenEvents[cm.EventID] = true
		chID := cm.ChannelID
		m.channelMsgs[chID] = appendMessage(m.channelMsgs[chID], cm, m.cfg.MaxMessages)
		if chID == m.channels[m.activeChannel].ID {
			m.updateViewport()
		}
		var batchCmds []tea.Cmd
		if profileCmd := m.maybeRequestProfile(cm.PubKey); profileCmd != nil {
			batchCmds = append(batchCmds, profileCmd)
		}
		if m.channelEvents != nil {
			batchCmds = append(batchCmds, waitForChannelEvent(m.channelEvents, m.channelSubID, m.keys))
		}
		return m, tea.Batch(batchCmds...)

	case dmEventMsg:
		cm := ChatMessage(msg)
		log.Printf("dmEventMsg: author=%s id=%s", cm.Author, cm.EventID)
		if m.seenEvents[cm.EventID] {
			if m.dmEvents != nil {
				return m, waitForDMEvent(m.dmEvents, m.keys)
			}
			return m, nil
		}
		m.seenEvents[cm.EventID] = true
		peer := cm.PubKey
		m.dmMsgs[peer] = appendMessage(m.dmMsgs[peer], cm, m.cfg.MaxMessages)
		if !containsStr(m.dmPeers, peer) {
			m.dmPeers = append(m.dmPeers, peer)
		}
		if m.tab == tabDMs {
			m.updateViewport()
		}
		var batchCmds []tea.Cmd
		if profileCmd := m.maybeRequestProfile(peer); profileCmd != nil {
			batchCmds = append(batchCmds, profileCmd)
		}
		if m.dmEvents != nil {
			batchCmds = append(batchCmds, waitForDMEvent(m.dmEvents, m.keys))
		}
		return m, tea.Batch(batchCmds...)

	case profileResolvedMsg:
		log.Printf("profileResolvedMsg: %s -> %q", shortPK(msg.PubKey), msg.DisplayName)
		m.profiles[msg.PubKey] = msg.DisplayName
		delete(m.profilePending, msg.PubKey)
		m.updateViewport()
		return m, nil

	case nostrErrMsg:
		log.Printf("nostrErrMsg: %s", msg.Error())
		m.addSystemMsg(msg.Error())
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.channelCancel != nil {
				m.channelCancel()
			}
			if m.dmCancel != nil {
				m.dmCancel()
			}
			return m, tea.Quit

		case "ctrl+up":
			if m.tab == tabChannels && len(m.channels) > 1 {
				m.activeChannel--
				if m.activeChannel < 0 {
					m.activeChannel = len(m.channels) - 1
				}
				m.updateViewport()
				return m, subscribeChannelCmd(m.pool, m.relays, m.channels[m.activeChannel].ID)
			}
			return m, nil

		case "ctrl+down":
			if m.tab == tabChannels && len(m.channels) > 1 {
				m.activeChannel++
				if m.activeChannel >= len(m.channels) {
					m.activeChannel = 0
				}
				m.updateViewport()
				return m, subscribeChannelCmd(m.pool, m.relays, m.channels[m.activeChannel].ID)
			}
			return m, nil

		case "pgup":
			m.viewport.ScrollUp(10)
			return m, nil

		case "pgdown":
			m.viewport.ScrollDown(10)
			return m, nil

		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			m.input.SetHeight(inputMinHeight)
			m.lastInputHeight = inputMinHeight
			m.updateLayout()

			// Slash commands
			if strings.HasPrefix(text, "/") {
				return m.handleCommand(text)
			}

			// Regular message
			if m.tab == tabChannels && len(m.channels) > 0 {
				chID := m.channels[m.activeChannel].ID
				return m, publishChannelMessage(m.pool, m.relays, chID, text, m.keys)
			}
			if m.tab == tabDMs && len(m.dmPeers) > 0 {
				peer := m.dmPeers[m.activeDMPeer]
				return m, sendDM(m.pool, m.relays, peer, text, m.keys)
			}
			return m, nil
		}
	}

	// Pre-grow textarea before newline insertion so the internal viewport
	// calculates its scroll offset with the correct height.
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if s := keyMsg.String(); s == "alt+enter" || s == "ctrl+j" {
			target := m.input.LineCount() + 1
			if target > inputMaxHeight {
				target = inputMaxHeight
			}
			if target != m.lastInputHeight {
				m.input.SetHeight(target)
				m.lastInputHeight = target
				m.updateLayout()
			}
		}
	}

	// Always pass keys to textarea
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)

	// Shrink textarea when lines are removed (e.g. backspace joining lines).
	m.syncInputHeight()

	return m, tea.Batch(cmds...)
}

func (m *model) handleCommand(text string) (tea.Model, tea.Cmd) {
	parts := strings.SplitN(text, " ", 2)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "/create":
		if arg == "" || !strings.HasPrefix(arg, "#") {
			m.addSystemMsg("usage: /create #roomname")
			return m, nil
		}
		name := strings.TrimPrefix(arg, "#")
		return m, createChannelCmd(m.pool, m.relays, name, m.keys)

	case "/join":
		if arg == "" {
			m.addSystemMsg("usage: /join #name or /join <event-id>")
			return m, nil
		}
		return m.joinChannel(arg)

	case "/dm":
		if arg == "" {
			m.addSystemMsg("usage: /dm <npub or hex pubkey>")
			return m, nil
		}
		return m.openDM(arg)

	case "/help":
		m.addSystemMsg("/create #name — create a new channel")
		m.addSystemMsg("/join #name — join a channel from your rooms file")
		m.addSystemMsg("/join <event-id> — join a channel by ID")
		m.addSystemMsg("/dm <npub> — open a DM conversation")
		m.addSystemMsg("/help — show this help")
		return m, nil

	default:
		m.addSystemMsg("unknown command: " + cmd)
		return m, nil
	}
}

// joinChannel handles /join. #name looks up the rooms file, a raw hex ID
// joins directly and appends to the rooms file.
func (m *model) joinChannel(arg string) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(arg, "#") {
		// Lookup by name
		name := strings.TrimPrefix(arg, "#")
		for i, ch := range m.channels {
			if strings.EqualFold(ch.Name, name) {
				log.Printf("joinChannel: found %q -> %s", name, ch.ID)
				m.tab = tabChannels
				m.activeChannel = i
				m.updateViewport()
				return m, subscribeChannelCmd(m.pool, m.relays, ch.ID)
			}
		}
		m.addSystemMsg("unknown room: " + name + " (add it to your rooms file)")
		return m, nil
	}

	// Raw hex event ID — check if already known
	id := arg
	for i, ch := range m.channels {
		if ch.ID == id {
			log.Printf("joinChannel: already have %s as %q", id, ch.Name)
			m.tab = tabChannels
			m.activeChannel = i
			m.updateViewport()
			return m, subscribeChannelCmd(m.pool, m.relays, ch.ID)
		}
	}

	// New room — add with placeholder, fetch metadata to get the real name
	m.channels = append(m.channels, Channel{Name: id[:8], ID: id})
	m.tab = tabChannels
	m.activeChannel = len(m.channels) - 1
	m.updateViewport()
	return m, tea.Batch(
		subscribeChannelCmd(m.pool, m.relays, id),
		fetchChannelMetaCmd(m.pool, m.relays, id),
	)
}

// openDM switches to a DM conversation, adding the peer if new.
func (m *model) openDM(input string) (tea.Model, tea.Cmd) {
	pk := input
	if strings.HasPrefix(input, "npub") {
		prefix, decoded, err := nip19.Decode(input)
		if err != nil || prefix != "npub" {
			m.addSystemMsg("invalid npub")
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
			break
		}
	}
	m.updateViewport()
	return m, nil
}

// addSystemMsg appends a local-only notice into the current chat view.
func (m *model) addSystemMsg(text string) {
	msg := ChatMessage{
		Author:    "system",
		Content:   text,
		Timestamp: nostr.Now(),
	}
	if m.tab == tabChannels && len(m.channels) > 0 {
		chID := m.channels[m.activeChannel].ID
		m.channelMsgs[chID] = appendMessage(m.channelMsgs[chID], msg, m.cfg.MaxMessages)
	} else if m.tab == tabDMs && len(m.dmPeers) > 0 {
		peer := m.dmPeers[m.activeDMPeer]
		m.dmMsgs[peer] = appendMessage(m.dmMsgs[peer], msg, m.cfg.MaxMessages)
	} else {
		m.globalMsgs = appendMessage(m.globalMsgs, msg, m.cfg.MaxMessages)
	}
	m.updateViewport()
}

// resolveAuthor returns the cached display name for a pubkey, or shortPK as fallback.
func (m *model) resolveAuthor(pubkey string) string {
	if name, ok := m.profiles[pubkey]; ok {
		return name
	}
	return shortPK(pubkey)
}

// maybeRequestProfile returns a fetchProfileCmd if we haven't seen this pubkey before.
func (m *model) maybeRequestProfile(pubkey string) tea.Cmd {
	if pubkey == "" {
		return nil
	}
	if _, ok := m.profiles[pubkey]; ok {
		return nil
	}
	if m.profilePending[pubkey] {
		return nil
	}
	m.profilePending[pubkey] = true
	return fetchProfileCmd(m.pool, m.relays, pubkey)
}

// syncInputHeight resizes the textarea to match its content and re-layouts if needed.
// Handles shrinking (e.g. backspace joining lines) and any growth not caught by pre-grow.
func (m *model) syncInputHeight() {
	lines := m.input.LineCount()
	if lines < inputMinHeight {
		lines = inputMinHeight
	}
	if lines > inputMaxHeight {
		lines = inputMaxHeight
	}
	if lines != m.lastInputHeight {
		m.input.SetHeight(lines)
		m.lastInputHeight = lines
		m.updateLayout()
	}
}

// sidebarWidth returns the width needed for the sidebar based on the longest
// channel name or DM peer display name.
func (m *model) sidebarWidth() int {
	longest := 0
	for _, ch := range m.channels {
		if n := len(ch.Name); n > longest {
			longest = n
		}
	}
	for _, peer := range m.dmPeers {
		if n := len(m.resolveAuthor(peer)); n > longest {
			longest = n
		}
	}
	w := longest + sidebarPadding
	if w < minSidebarWidth {
		w = minSidebarWidth
	}
	return w
}

func (m *model) updateLayout() {
	contentWidth := m.width - m.sidebarWidth() - sidebarBorder
	contentHeight := m.height - headerHeight - contentTitleHeight - statusHeight - m.lastInputHeight

	if contentWidth < 10 {
		contentWidth = 10
	}
	if contentHeight < 1 {
		contentHeight = 1
	}

	m.viewport.Width = contentWidth
	m.viewport.Height = contentHeight
	m.input.SetWidth(contentWidth)
	m.updateViewport()
}

func (m *model) updateViewport() {
	var msgs []ChatMessage
	if m.tab == tabChannels && len(m.channels) > 0 {
		chID := m.channels[m.activeChannel].ID
		msgs = m.channelMsgs[chID]
	} else if m.tab == tabDMs && len(m.dmPeers) > 0 {
		peer := m.dmPeers[m.activeDMPeer]
		msgs = m.dmMsgs[peer]
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
			displayName = m.resolveAuthor(msg.PubKey)
		}
		ts := chatTimestampStyle.Render(msg.Timestamp.Time().Format("15:04"))
		author := authorStyle.Render(displayName)
		// Convert single newlines to paragraph breaks for glamour.
		mdContent := strings.ReplaceAll(msg.Content, "\n", "\n\n")
		content := renderMarkdown(m.mdRender, mdContent)
		prefix := fmt.Sprintf("%s %s: ", ts, author)
		prefixW := lipgloss.Width(prefix)
		pad := strings.Repeat(" ", prefixW)
		wrapWidth := m.viewport.Width - prefixW
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

	header := m.viewHeader()
	sidebar := m.viewSidebar()
	content := m.viewContent()
	statusBar := m.viewStatusBar()

	mainArea := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)

	return lipgloss.JoinVertical(lipgloss.Left, header, mainArea, statusBar)
}

func (m *model) viewHeader() string {
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

func (m *model) viewSidebar() string {
	contentHeight := m.height - headerHeight - statusHeight
	sw := m.sidebarWidth()
	var items []string
	if m.tab == tabChannels {
		for i, ch := range m.channels {
			name := "#" + ch.Name
			if len(name) > sw-2 {
				name = name[:sw-2]
			}
			if i == m.activeChannel {
				items = append(items, sidebarSelectedStyle.Render(name))
			} else {
				items = append(items, sidebarItemStyle.Render(name))
			}
		}
	} else {
		for i, peer := range m.dmPeers {
			name := "@" + m.resolveAuthor(peer)
			if len(name) > sw-2 {
				name = name[:sw-2]
			}
			if i == m.activeDMPeer {
				items = append(items, sidebarSelectedStyle.Render(name))
			} else {
				items = append(items, sidebarItemStyle.Render(name))
			}
		}
	}

	content := strings.Join(items, "\n")

	return sidebarStyle.Width(sw).Height(contentHeight).MaxHeight(contentHeight).Render(content)
}

func (m *model) viewContent() string {
	totalHeight := m.height - headerHeight - statusHeight

	var title string
	if m.tab == tabChannels && len(m.channels) > 0 {
		title = "#" + m.channels[m.activeChannel].Name
	} else if m.tab == tabDMs && len(m.dmPeers) > 0 {
		title = "@" + m.resolveAuthor(m.dmPeers[m.activeDMPeer])
	}

	titleBar := headerStyle.Render(title)
	inputView := m.input.View()
	vp := m.viewport.View()

	inner := lipgloss.JoinVertical(lipgloss.Left, titleBar, vp, inputView)

	return lipgloss.NewStyle().Height(totalHeight).MaxHeight(totalHeight).Render(inner)
}

func (m *model) viewStatusBar() string {
	left := statusConnectedStyle.Render(fmt.Sprintf("● %d relays · %d rooms", len(m.relays), len(m.channels)))
	npub := m.keys.NPub
	if len(npub) > 20 {
		npub = npub[:20] + "..."
	}
	right := npub

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 0 {
		gap = 0
	}

	bar := left + strings.Repeat(" ", gap) + right
	return statusBarStyle.Width(m.width).Render(bar)
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

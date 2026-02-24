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
	qrterminal "github.com/mdp/qrterminal/v3"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type model struct {
	// Config and keys
	cfg         Config
	cfgFlagPath string
	keys        Keys
	pool        *nostr.SimplePool
	kr          nostr.Keyer
	relays      []string
	rooms       []Room // from rooms file

	// TUI dimensions
	width  int
	height int

	// Unified sidebar selection: 0..len(channels)-1 = channels, len(channels).. = DMs
	activeItem int

	// Channels
	channels      []Channel
	channelMsgs   map[string][]ChatMessage
	channelSubID  string // ID of the channel we're subscribed to
	channelEvents <-chan nostr.RelayEvent
	channelCancel context.CancelFunc

	// DMs
	dmPeers    []string // pubkeys of DM peers
	dmMsgs     map[string][]ChatMessage
	dmEvents   <-chan nostr.Event
	dmCancel   context.CancelFunc
	lastDMSeen nostr.Timestamp

	// Components
	viewport viewport.Model
	input    textarea.Model
	mdRender *glamour.TermRenderer
	mdStyle  string

	// Global messages (shown when no channel/DM is active)
	globalMsgs []ChatMessage

	// Dedup
	seenEvents   map[string]bool
	localDMEchoes map[string]bool // "peer:content" keys for sent DMs awaiting relay echo

	// Profile resolution (NIP-01 kind 0)
	profiles       map[string]string // pubkey -> display name
	profilePending map[string]bool   // pubkeys with in-flight fetches

	// Input tracking
	lastInputHeight int

	// Status
	statusMsg string

	// QR overlay (non-empty = show full-screen QR)
	qrOverlay string
}

// isChannelSelected returns true if the active sidebar item is a channel.
func (m *model) isChannelSelected() bool {
	return m.activeItem < len(m.channels)
}

// activeChannelIdx returns the channel index, or -1 if a DM is selected.
func (m *model) activeChannelIdx() int {
	if m.isChannelSelected() {
		return m.activeItem
	}
	return -1
}

// activeChannelID returns the selected channel ID, or "" if a DM is selected.
func (m *model) activeChannelID() string {
	if idx := m.activeChannelIdx(); idx >= 0 && idx < len(m.channels) {
		return m.channels[idx].ID
	}
	return ""
}

// activeDMPeerIdx returns the DM peer index, or -1 if a channel is selected.
func (m *model) activeDMPeerIdx() int {
	if !m.isChannelSelected() {
		return m.activeItem - len(m.channels)
	}
	return -1
}

// activeDMPeerPK returns the selected DM peer pubkey, or "" if a channel is selected.
func (m *model) activeDMPeerPK() string {
	if idx := m.activeDMPeerIdx(); idx >= 0 && idx < len(m.dmPeers) {
		return m.dmPeers[idx]
	}
	return ""
}

// sidebarTotal returns the total number of items in the unified sidebar.
func (m *model) sidebarTotal() int {
	return len(m.channels) + len(m.dmPeers)
}

func newModel(cfg Config, cfgFlagPath string, keys Keys, pool *nostr.SimplePool, kr nostr.Keyer, rooms []Room, contacts []Contact, mdRender *glamour.TermRenderer, mdStyle string) model {
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
	}

	profiles := map[string]string{keys.PK: ownName}

	// Seed DM peers and profiles from contacts file.
	var dmPeers []string
	for _, c := range contacts {
		dmPeers = append(dmPeers, c.PubKey)
		profiles[c.PubKey] = c.Name
	}

	return model{
		cfg:         cfg,
		cfgFlagPath: cfgFlagPath,
		keys:        keys,
		pool:        pool,
		kr:          kr,
		relays:      cfg.Relays,
		rooms:       rooms,
		width:       80,
		height:      24,
		activeItem:  0,
		channels:       channels,
		channelMsgs:    make(map[string][]ChatMessage),
		dmMsgs:         make(map[string][]ChatMessage),
		dmPeers:        dmPeers,
		lastDMSeen:     LoadLastDMSeen(cfgFlagPath),
		seenEvents:     make(map[string]bool),
		localDMEchoes:  make(map[string]bool),
		profiles:       profiles,
		profilePending: make(map[string]bool),
		lastInputHeight: inputMinHeight,
		viewport:       vp,
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
		subscribeDMCmd(m.pool, m.relays, m.kr, m.lastDMSeen),
		publishDMRelaysCmd(m.pool, m.relays, m.keys),
	}
	if len(m.channels) > 0 && m.isChannelSelected() {
		cmds = append(cmds, subscribeChannelCmd(m.pool, m.relays, m.channels[0].ID))
	}
	if m.cfg.Profile.Name != "" || m.cfg.Profile.DisplayName != "" || m.cfg.Profile.About != "" || m.cfg.Profile.Picture != "" {
		cmds = append(cmds, publishProfileCmd(m.pool, m.relays, m.cfg.Profile, m.keys))
	}
	// Fetch profiles for all known DM peers so display names are up to date.
	for _, peer := range m.dmPeers {
		cmds = append(cmds, fetchProfileCmd(m.pool, m.relays, peer))
		m.profilePending[peer] = true
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
		m.activeItem = len(m.channels) - 1
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
		for i, ch := range m.channels {
			if ch.ID == msg.ID {
				m.channels[i].Name = msg.Name
				break
			}
		}
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
		if idx := m.activeChannelIdx(); idx >= 0 && idx < len(m.channels) {
			m.addSystemMsg("subscribed to #" + m.channels[idx].Name)
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
		if chID == m.activeChannelID() {
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
		log.Printf("dmEventMsg: author=%s id=%s mine=%v", cm.Author, cm.EventID, cm.IsMine)
		if m.seenEvents[cm.EventID] {
			if m.dmEvents != nil {
				return m, waitForDMEvent(m.dmEvents, m.keys)
			}
			return m, nil
		}
		m.seenEvents[cm.EventID] = true

		// Content-based dedup for our own DMs: the local echo from sendDM and
		// the relay echo from the subscription have different synthetic EventIDs,
		// so seenEvents can't catch the duplicate. Track by peer+content instead.
		if cm.IsMine {
			echoKey := cm.PubKey + ":" + cm.Content
			if m.localDMEchoes[echoKey] {
				log.Printf("dmEventMsg: skipping relay echo (already have local echo)")
				delete(m.localDMEchoes, echoKey)
				if m.dmEvents != nil {
					return m, waitForDMEvent(m.dmEvents, m.keys)
				}
				return m, nil
			}
			m.localDMEchoes[echoKey] = true
		}

		peer := cm.PubKey
		m.dmMsgs[peer] = appendMessage(m.dmMsgs[peer], cm, m.cfg.MaxMessages)
		if !containsStr(m.dmPeers, peer) {
			m.dmPeers = append(m.dmPeers, peer)
			if err := AppendContact(m.cfgFlagPath, Contact{Name: m.resolveAuthor(peer), PubKey: peer}); err != nil {
				log.Printf("dmEventMsg: failed to save contact: %v", err)
			}
		}
		if cm.Timestamp > m.lastDMSeen {
			m.lastDMSeen = cm.Timestamp
			if err := SaveLastDMSeen(m.cfgFlagPath, m.lastDMSeen); err != nil {
				log.Printf("dmEventMsg: failed to save last DM seen: %v", err)
			}
		}
		if !m.isChannelSelected() {
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

	case dmSubEndedMsg:
		log.Println("dmSubEndedMsg: DM subscription ended, scheduling reconnect")
		m.dmEvents = nil
		m.addSystemMsg("DM subscription lost, reconnecting...")
		return m, dmReconnectDelayCmd()

	case dmReconnectMsg:
		log.Println("dmReconnectMsg: reconnecting DM subscription")
		return m, subscribeDMCmd(m.pool, m.relays, m.kr, m.lastDMSeen)

	case channelSubEndedMsg:
		log.Printf("channelSubEndedMsg: channel %s subscription ended, scheduling reconnect", shortPK(msg.channelID))
		m.channelEvents = nil
		if msg.channelID == m.activeChannelID() {
			m.addSystemMsg("channel subscription lost, reconnecting...")
		}
		return m, channelReconnectDelayCmd(msg.channelID)

	case channelReconnectMsg:
		log.Printf("channelReconnectMsg: reconnecting channel %s", shortPK(msg.channelID))
		if msg.channelID == m.activeChannelID() {
			return m, subscribeChannelCmd(m.pool, m.relays, msg.channelID)
		}
		return m, nil

	case profileResolvedMsg:
		log.Printf("profileResolvedMsg: %s -> %q", shortPK(msg.PubKey), msg.DisplayName)
		m.profiles[msg.PubKey] = msg.DisplayName
		delete(m.profilePending, msg.PubKey)
		if containsStr(m.dmPeers, msg.PubKey) {
			if err := UpdateContactName(m.cfgFlagPath, msg.PubKey, msg.DisplayName); err != nil {
				log.Printf("profileResolvedMsg: failed to update contact name: %v", err)
			}
		}
		m.updateViewport()
		return m, nil

	case nostrErrMsg:
		log.Printf("nostrErrMsg: %s", msg.Error())
		m.addSystemMsg(msg.Error())
		return m, nil

	case tea.KeyMsg:
		// Dismiss QR overlay on any key (except ctrl+c which still quits).
		if m.qrOverlay != "" {
			if msg.String() == "ctrl+c" {
				if m.channelCancel != nil {
					m.channelCancel()
				}
				if m.dmCancel != nil {
					m.dmCancel()
				}
				return m, tea.Quit
			}
			m.qrOverlay = ""
			return m, nil
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

		case "ctrl+up":
			total := m.sidebarTotal()
			if total > 1 {
				prev := m.activeItem
				m.activeItem--
				if m.activeItem < 0 {
					m.activeItem = total - 1
				}
				m.updateViewport()
				if m.isChannelSelected() && (prev >= len(m.channels) || m.activeItem != prev) {
					return m, subscribeChannelCmd(m.pool, m.relays, m.activeChannelID())
				}
			}
			return m, nil

		case "ctrl+down":
			total := m.sidebarTotal()
			if total > 1 {
				prev := m.activeItem
				m.activeItem++
				if m.activeItem >= total {
					m.activeItem = 0
				}
				m.updateViewport()
				if m.isChannelSelected() && (prev >= len(m.channels) || m.activeItem != prev) {
					return m, subscribeChannelCmd(m.pool, m.relays, m.activeChannelID())
				}
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
			if m.isChannelSelected() && len(m.channels) > 0 {
				chID := m.activeChannelID()
				return m, publishChannelMessage(m.pool, m.relays, chID, text, m.keys)
			}
			if !m.isChannelSelected() && len(m.dmPeers) > 0 {
				peer := m.activeDMPeerPK()
				return m, sendDM(m.pool, m.relays, peer, text, m.keys, m.kr)
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

	case "/me":
		m.qrOverlay = renderQR("Your npub:", m.keys.NPub)
		return m, nil

	case "/room":
		if !m.isChannelSelected() || len(m.channels) == 0 {
			m.addSystemMsg("no active channel — switch to a channel first")
			return m, nil
		}
		ch := m.channels[m.activeChannelIdx()]
		m.qrOverlay = renderQR("#"+ch.Name, ch.ID)
		return m, nil

	case "/leave":
		return m.leaveCurrentItem()

	case "/help":
		m.addSystemMsg("/create #name — create a new channel")
		m.addSystemMsg("/join #name — join a channel from your rooms file")
		m.addSystemMsg("/join <event-id> — join a channel by ID")
		m.addSystemMsg("/dm <npub> — open a DM conversation")
		m.addSystemMsg("/leave — leave the current channel or DM")
		m.addSystemMsg("/me — show QR code of your npub")
		m.addSystemMsg("/room — show QR code of the current channel")
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
				m.activeItem = i
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
			m.activeItem = i
			m.updateViewport()
			return m, subscribeChannelCmd(m.pool, m.relays, ch.ID)
		}
	}

	// New room — add with placeholder, fetch metadata to get the real name
	m.channels = append(m.channels, Channel{Name: id[:8], ID: id})
	m.activeItem = len(m.channels) - 1
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
		if err := AppendContact(m.cfgFlagPath, Contact{Name: m.resolveAuthor(pk), PubKey: pk}); err != nil {
			log.Printf("openDM: failed to save contact: %v", err)
		}
	}

	for i, p := range m.dmPeers {
		if p == pk {
			m.activeItem = len(m.channels) + i
			break
		}
	}
	m.updateViewport()
	return m, nil
}

// leaveCurrentItem removes the currently selected channel or DM from the
// sidebar and deletes it from the rooms/contacts file.
func (m *model) leaveCurrentItem() (tea.Model, tea.Cmd) {
	total := m.sidebarTotal()
	if total == 0 {
		m.addSystemMsg("nothing to leave")
		return m, nil
	}

	if m.isChannelSelected() && len(m.channels) > 0 {
		idx := m.activeChannelIdx()
		ch := m.channels[idx]

		// Cancel subscription if this is the active one.
		if ch.ID == m.channelSubID && m.channelCancel != nil {
			m.channelCancel()
			m.channelEvents = nil
			m.channelCancel = nil
			m.channelSubID = ""
		}

		// Remove from channels list and message history.
		m.channels = append(m.channels[:idx], m.channels[idx+1:]...)
		delete(m.channelMsgs, ch.ID)

		// Remove from rooms file.
		if err := RemoveRoom(m.cfgFlagPath, ch.ID); err != nil {
			log.Printf("leaveCurrentItem: failed to remove room: %v", err)
		}

		log.Printf("leaveCurrentItem: left channel #%s", ch.Name)
	} else if !m.isChannelSelected() && len(m.dmPeers) > 0 {
		idx := m.activeDMPeerIdx()
		peer := m.dmPeers[idx]

		// Remove from peers list and message history.
		m.dmPeers = append(m.dmPeers[:idx], m.dmPeers[idx+1:]...)
		delete(m.dmMsgs, peer)

		// Remove from contacts file.
		if err := RemoveContact(m.cfgFlagPath, peer); err != nil {
			log.Printf("leaveCurrentItem: failed to remove contact: %v", err)
		}

		log.Printf("leaveCurrentItem: left DM with %s", m.resolveAuthor(peer))
	}

	// Clamp activeItem to valid range.
	total = m.sidebarTotal()
	if total == 0 {
		m.activeItem = 0
	} else if m.activeItem >= total {
		m.activeItem = total - 1
	}

	m.updateViewport()

	// Subscribe to the new active channel if one is selected.
	if m.isChannelSelected() && len(m.channels) > 0 {
		return m, subscribeChannelCmd(m.pool, m.relays, m.activeChannelID())
	}
	return m, nil
}

// addSystemMsg appends a local-only notice into the current chat view.
func (m *model) addSystemMsg(text string) {
	msg := ChatMessage{
		Author:    "system",
		Content:   text,
		Timestamp: nostr.Now(),
	}
	if m.isChannelSelected() && len(m.channels) > 0 {
		chID := m.activeChannelID()
		m.channelMsgs[chID] = appendMessage(m.channelMsgs[chID], msg, m.cfg.MaxMessages)
	} else if !m.isChannelSelected() && len(m.dmPeers) > 0 {
		peer := m.activeDMPeerPK()
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
	contentHeight := m.height - contentTitleHeight - statusHeight - m.lastInputHeight

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
	if m.isChannelSelected() && len(m.channels) > 0 {
		chID := m.activeChannelID()
		msgs = m.channelMsgs[chID]
	} else if !m.isChannelSelected() && len(m.dmPeers) > 0 {
		peer := m.activeDMPeerPK()
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
			if msg.IsMine {
				displayName = m.resolveAuthor(m.keys.PK)
			} else {
				displayName = m.resolveAuthor(msg.PubKey)
			}
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
	contentHeight := m.height - statusHeight
	sw := m.sidebarWidth()
	var items []string

	// CHANNELS section
	items = append(items, sidebarSectionStyle.Render("CHANNELS"))
	for i, ch := range m.channels {
		name := "#" + ch.Name
		if len(name) > sw-2 {
			name = name[:sw-2]
		}
		if i == m.activeItem {
			items = append(items, sidebarSelectedStyle.Render(name))
		} else {
			items = append(items, sidebarItemStyle.Render(name))
		}
	}

	// DMS section
	items = append(items, sidebarSectionStyle.Render("DMS"))
	for i, peer := range m.dmPeers {
		name := "@" + m.resolveAuthor(peer)
		if len(name) > sw-2 {
			name = name[:sw-2]
		}
		idx := len(m.channels) + i
		if idx == m.activeItem {
			items = append(items, sidebarSelectedStyle.Render(name))
		} else {
			items = append(items, sidebarItemStyle.Render(name))
		}
	}

	content := strings.Join(items, "\n")

	return sidebarStyle.Width(sw).Height(contentHeight).MaxHeight(contentHeight).Render(content)
}

func (m *model) viewContent() string {
	totalHeight := m.height - statusHeight

	var title string
	if m.isChannelSelected() && len(m.channels) > 0 {
		title = "#" + m.channels[m.activeChannelIdx()].Name
	} else if !m.isChannelSelected() && len(m.dmPeers) > 0 {
		title = "@" + m.resolveAuthor(m.dmPeers[m.activeDMPeerIdx()])
	}

	titleBar := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Padding(0, 1).Render(title)
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

// renderQR renders a QR code with a title line above it.
func renderQR(title, content string) string {
	var buf strings.Builder
	buf.WriteString(qrTitleStyle.Render(title))
	buf.WriteString("\n\n")
	qrterminal.GenerateWithConfig(content, qrterminal.Config{
		Level:          qrterminal.M,
		Writer:         &buf,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		QuietZone:      1,
	})
	return buf.String()
}

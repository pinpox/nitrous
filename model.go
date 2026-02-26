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
	qrterminal "github.com/mdp/qrterminal/v3"
	"github.com/nbd-wtf/go-nostr"
)

// Group represents a NIP-29 relay-based group.
type Group struct {
	RelayURL    string
	GroupID     string
	Name        string
	RelayPubKey string // pubkey of the relay (author of kind 39000 metadata)
}

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

	// Unified sidebar selection:
	//   0..len(channels)-1                          = channels (#)
	//   len(channels)..len(channels)+len(groups)-1  = groups (~)
	//   len(channels)+len(groups)..                  = DMs (@)
	activeItem int
	sidebar    []SidebarItem // unified sidebar list (temporary duplication with channels/groups/dmPeers)

	// Channels
	channels      []Channel
	channelMsgs   map[string][]ChatMessage
	channelSubID  string // ID of the channel we're subscribed to
	channelEvents <-chan nostr.RelayEvent
	channelCancel context.CancelFunc

	// NIP-29 Groups
	groups         []Group
	groupMsgs      map[string][]ChatMessage // keyed by groupKey(relayURL, groupID)
	groupRecentIDs map[string][]string      // per-group ring buffer of event IDs (max 50)
	groupSubKey    string                   // groupKey of the group we're subscribed to
	groupEvents    <-chan nostr.RelayEvent
	groupCancel    context.CancelFunc

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

	// Unread indicators (keyed by channel ID, group key, or DM peer pubkey)
	unread        map[string]bool
	dmSeenAtStart nostr.Timestamp // lastDMSeen at startup, to suppress unread for replayed messages

	// Profile resolution (NIP-01 kind 0)
	profiles       map[string]string // pubkey -> display name
	profilePending map[string]bool   // pubkeys with in-flight fetches

	// Input tracking
	lastInputHeight int

	// Autocomplete
	acSuggestions []string
	acIndex       int
	acMention     bool // true when completing an @mention (vs slash command)

	// Input history
	inputHistory []string // sent messages, newest last
	historyIndex int      // -1 = current input, 0..len-1 = history position from end
	historySaved string   // unsent input saved when entering history

	// Status
	statusMsg string

	// QR overlay (non-empty = show full-screen QR)
	qrOverlay string

	// NIP-51 list timestamps — used to detect whether relay data is newer.
	contactsListTS nostr.Timestamp
	channelsListTS nostr.Timestamp
	groupsListTS   nostr.Timestamp
}

// isChannelSelected returns true if the active sidebar item is a channel.
func (m *model) isChannelSelected() bool {
	return m.activeItem < len(m.channels)
}

// isGroupSelected returns true if the active sidebar item is a NIP-29 group.
func (m *model) isGroupSelected() bool {
	return m.activeItem >= len(m.channels) && m.activeItem < len(m.channels)+len(m.groups)
}

// isDMSelected returns true if the active sidebar item is a DM.
func (m *model) isDMSelected() bool {
	return m.activeItem >= len(m.channels)+len(m.groups)
}

// activeChannelIdx returns the channel index, or -1 if not a channel.
func (m *model) activeChannelIdx() int {
	if m.isChannelSelected() {
		return m.activeItem
	}
	return -1
}

// activeChannelID returns the selected channel ID, or "" if not a channel.
func (m *model) activeChannelID() string {
	if idx := m.activeChannelIdx(); idx >= 0 && idx < len(m.channels) {
		return m.channels[idx].ID
	}
	return ""
}

// activeGroupIdx returns the group index, or -1 if not a group.
func (m *model) activeGroupIdx() int {
	if m.isGroupSelected() {
		return m.activeItem - len(m.channels)
	}
	return -1
}

// activeGroupKey returns the groupKey of the selected group, or "".
func (m *model) activeGroupKey() string {
	if idx := m.activeGroupIdx(); idx >= 0 && idx < len(m.groups) {
		g := m.groups[idx]
		return groupKey(g.RelayURL, g.GroupID)
	}
	return ""
}

// activeDMPeerIdx returns the DM peer index, or -1 if not a DM.
func (m *model) activeDMPeerIdx() int {
	if m.isDMSelected() {
		return m.activeItem - len(m.channels) - len(m.groups)
	}
	return -1
}

// activeDMPeerPK returns the selected DM peer pubkey, or "" if not a DM.
func (m *model) activeDMPeerPK() string {
	if idx := m.activeDMPeerIdx(); idx >= 0 && idx < len(m.dmPeers) {
		return m.dmPeers[idx]
	}
	return ""
}

// sidebarTotal returns the total number of items in the unified sidebar.
func (m *model) sidebarTotal() int {
	return len(m.channels) + len(m.groups) + len(m.dmPeers)
}

// activeSidebarItem returns the currently selected SidebarItem, or nil.
func (m *model) activeSidebarItem() SidebarItem {
	if m.activeItem >= 0 && m.activeItem < len(m.sidebar) {
		return m.sidebar[m.activeItem]
	}
	return nil
}

// rebuildSidebar reconstructs the sidebar slice from the current channels, groups, and dmPeers.
func (m *model) rebuildSidebar() {
	m.sidebar = make([]SidebarItem, 0, len(m.channels)+len(m.groups)+len(m.dmPeers))
	for _, ch := range m.channels {
		m.sidebar = append(m.sidebar, ChannelItem{Channel: ch})
	}
	for _, g := range m.groups {
		m.sidebar = append(m.sidebar, GroupItem{Group: g})
	}
	for _, peer := range m.dmPeers {
		m.sidebar = append(m.sidebar, DMItem{PubKey: peer, Name: m.resolveAuthor(peer)})
	}
}

// subscribeIfNeeded returns a subscribe command if the active item changed type.
func (m *model) subscribeIfNeeded(prev int) tea.Cmd {
	if m.isChannelSelected() {
		prevWasChannel := prev < len(m.channels)
		if !prevWasChannel || m.activeItem != prev {
			return subscribeChannelCmd(m.pool, m.relays, m.activeChannelID())
		}
	}
	if m.isGroupSelected() {
		prevWasGroup := prev >= len(m.channels) && prev < len(m.channels)+len(m.groups)
		if !prevWasGroup || m.activeItem != prev {
			g := m.groups[m.activeGroupIdx()]
			return subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
		}
	}
	return nil
}

func newModel(cfg Config, cfgFlagPath string, keys Keys, pool *nostr.SimplePool, kr nostr.Keyer, rooms []Room, savedGroups []SavedGroup, contacts []Contact, mdRender *glamour.TermRenderer, mdStyle string) model {
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

	// Populate groups from groups file
	var groups []Group
	for _, sg := range savedGroups {
		groups = append(groups, Group{RelayURL: sg.RelayURL, GroupID: sg.GroupID, Name: sg.Name})
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

	// Build initial sidebar.
	sidebar := make([]SidebarItem, 0, len(channels)+len(groups)+len(dmPeers))
	for _, ch := range channels {
		sidebar = append(sidebar, ChannelItem{Channel: ch})
	}
	for _, g := range groups {
		sidebar = append(sidebar, GroupItem{Group: g})
	}
	for _, peer := range dmPeers {
		name := profiles[peer]
		if name == "" {
			name = shortPK(peer)
		}
		sidebar = append(sidebar, DMItem{PubKey: peer, Name: name})
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
		sidebar:     sidebar,
		channels:       channels,
		channelMsgs:    make(map[string][]ChatMessage),
		groups:          groups,
		groupMsgs:       make(map[string][]ChatMessage),
		groupRecentIDs:  make(map[string][]string),
		dmMsgs:         make(map[string][]ChatMessage),
		dmPeers:        dmPeers,
		lastDMSeen:     LoadLastDMSeen(cfgFlagPath),
		dmSeenAtStart:  LoadLastDMSeen(cfgFlagPath),
		seenEvents:     make(map[string]bool),
		unread:         make(map[string]bool),
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
	} else if len(m.groups) > 0 {
		m.addSystemMsg(fmt.Sprintf("joining ~%s ...", m.groups[0].Name))
	} else {
		m.addSystemMsg("no rooms configured — use /channel create #name or /join <event-id>")
	}

	cmds := []tea.Cmd{
		textarea.Blink,
		subscribeDMCmd(m.pool, m.relays, m.kr, m.lastDMSeen),
		publishDMRelaysCmd(m.pool, m.relays, m.keys),
		fetchNIP51ListsCmd(m.pool, m.relays, m.keys, m.kr),
	}
	if len(m.channels) > 0 && m.isChannelSelected() {
		cmds = append(cmds, subscribeChannelCmd(m.pool, m.relays, m.channels[0].ID))
	}
	if len(m.groups) > 0 && m.isGroupSelected() {
		g := m.groups[0]
		cmds = append(cmds, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID))
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
	} else if m.isGroupSelected() && len(m.groups) > 0 {
		gk := m.activeGroupKey()
		m.groupMsgs[gk] = appendMessage(m.groupMsgs[gk], msg, m.cfg.MaxMessages)
	} else if m.isDMSelected() && len(m.dmPeers) > 0 {
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

// clearUnread removes the unread indicator for the currently active item.
func (m *model) clearUnread() {
	if m.isChannelSelected() && len(m.channels) > 0 {
		delete(m.unread, m.activeChannelID())
	} else if m.isGroupSelected() && len(m.groups) > 0 {
		delete(m.unread, m.activeGroupKey())
	} else if m.isDMSelected() && len(m.dmPeers) > 0 {
		delete(m.unread, m.activeDMPeerPK())
	}
}

func appendMessage(msgs []ChatMessage, msg ChatMessage, maxMessages int) []ChatMessage {
	// Insert in timestamp order (historical events may arrive newest-first).
	i := len(msgs)
	for i > 0 && msgs[i-1].Timestamp > msg.Timestamp {
		i--
	}
	msgs = append(msgs, ChatMessage{})
	copy(msgs[i+1:], msgs[i:])
	msgs[i] = msg

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
	buf.WriteString("\n")
	buf.WriteString(chatSystemStyle.Render(content))
	return buf.String()
}

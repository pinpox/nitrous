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
	savedRooms  []Room // from rooms file

	// TUI dimensions
	width  int
	height int

	// Unified sidebar — single source of truth for channels, groups, and DMs.
	// Layout order: all channels first, then all groups, then all DMs.
	activeItem int
	sidebar    []SidebarItem

	// Per-room subscription (channel or group); nil when none is active.
	roomSub *roomSub

	// NIP-29 Group recent event IDs (per-group ring buffer, max 50)
	groupRecentIDs map[string][]string

	// DM subscription state
	dmEvents   <-chan nostr.Event
	dmCancel   context.CancelFunc
	lastDMSeen nostr.Timestamp

	// Unified message store (keyed by channel ID, groupKey, or DM peer pubkey)
	msgs map[string][]ChatMessage

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

// roomSub holds the active per-room subscription (channel or group).
type roomSub struct {
	kind   SidebarKind
	roomID string // channel ID or groupKey
	events <-chan nostr.RelayEvent
	cancel context.CancelFunc
}

// waitForRoomEvent returns a Cmd that waits for the next event on the active room subscription.
func (m *model) waitForRoomEvent() tea.Cmd {
	if m.roomSub == nil {
		return nil
	}
	switch m.roomSub.kind {
	case SidebarChannel:
		return waitForChannelEvent(m.roomSub.events, m.roomSub.roomID, m.keys)
	case SidebarGroup:
		relayURL, _ := splitGroupKey(m.roomSub.roomID)
		return waitForGroupEvent(m.roomSub.events, m.roomSub.roomID, relayURL, m.keys)
	}
	return nil
}

// cancelRoomSub cancels the active room subscription if any.
func (m *model) cancelRoomSub() {
	if m.roomSub != nil {
		m.roomSub.cancel()
		m.roomSub = nil
	}
}

// isChannelSelected returns true if the active sidebar item is a channel.
func (m *model) isChannelSelected() bool {
	item := m.activeSidebarItem()
	return item != nil && item.Kind() == SidebarChannel
}

// isGroupSelected returns true if the active sidebar item is a NIP-29 group.
func (m *model) isGroupSelected() bool {
	item := m.activeSidebarItem()
	return item != nil && item.Kind() == SidebarGroup
}

// isDMSelected returns true if the active sidebar item is a DM.
func (m *model) isDMSelected() bool {
	item := m.activeSidebarItem()
	return item != nil && item.Kind() == SidebarDM
}

// activeChannelID returns the selected channel ID, or "" if not a channel.
func (m *model) activeChannelID() string {
	if item := m.activeSidebarItem(); item != nil {
		if ci, ok := item.(ChannelItem); ok {
			return ci.Channel.ID
		}
	}
	return ""
}

// activeGroupKey returns the groupKey of the selected group, or "".
func (m *model) activeGroupKey() string {
	if item := m.activeSidebarItem(); item != nil {
		if gi, ok := item.(GroupItem); ok {
			return groupKey(gi.Group.RelayURL, gi.Group.GroupID)
		}
	}
	return ""
}

// activeDMPeerPK returns the selected DM peer pubkey, or "" if not a DM.
func (m *model) activeDMPeerPK() string {
	if item := m.activeSidebarItem(); item != nil {
		if di, ok := item.(DMItem); ok {
			return di.PubKey
		}
	}
	return ""
}

// sidebarTotal returns the total number of items in the unified sidebar.
func (m *model) sidebarTotal() int {
	return len(m.sidebar)
}

// activeSidebarItem returns the currently selected SidebarItem, or nil.
func (m *model) activeSidebarItem() SidebarItem {
	if m.activeItem >= 0 && m.activeItem < len(m.sidebar) {
		return m.sidebar[m.activeItem]
	}
	return nil
}

// subscribeIfNeeded returns a subscribe command if the active item changed.
func (m *model) subscribeIfNeeded(prev int) tea.Cmd {
	item := m.activeSidebarItem()
	if item == nil {
		return nil
	}
	if m.activeItem == prev {
		return nil
	}
	switch it := item.(type) {
	case ChannelItem:
		return subscribeChannelCmd(m.pool, m.relays, it.Channel.ID)
	case GroupItem:
		return subscribeGroupCmd(m.pool, it.Group.RelayURL, it.Group.GroupID)
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

	// Pre-cache own display name from config fallback chain.
	ownName := shortPK(keys.PK)
	if cfg.Profile.DisplayName != "" {
		ownName = cfg.Profile.DisplayName
	} else if cfg.Profile.Name != "" {
		ownName = cfg.Profile.Name
	}

	profiles := map[string]string{keys.PK: ownName}

	// Seed profiles from contacts.
	for _, c := range contacts {
		profiles[c.PubKey] = c.Name
	}

	// Build initial sidebar: channels, then groups, then DMs.
	var sidebar []SidebarItem
	for _, r := range rooms {
		sidebar = append(sidebar, ChannelItem{Channel: Channel{ID: r.ID, Name: r.Name}})
	}
	for _, sg := range savedGroups {
		sidebar = append(sidebar, GroupItem{Group: Group{RelayURL: sg.RelayURL, GroupID: sg.GroupID, Name: sg.Name}})
	}
	for _, c := range contacts {
		name := profiles[c.PubKey]
		if name == "" {
			name = shortPK(c.PubKey)
		}
		sidebar = append(sidebar, DMItem{PubKey: c.PubKey, Name: name})
	}

	return model{
		cfg:         cfg,
		cfgFlagPath: cfgFlagPath,
		keys:        keys,
		pool:        pool,
		kr:          kr,
		relays:      cfg.Relays,
		savedRooms:  rooms,
		width:       80,
		height:      24,
		activeItem:  0,
		sidebar:     sidebar,
		groupRecentIDs:  make(map[string][]string),
		msgs:           make(map[string][]ChatMessage),
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
	channels := m.allChannels()
	groups := m.allGroups()
	if len(channels) > 0 {
		m.addSystemMsg(fmt.Sprintf("joining #%s ...", channels[0].Name))
	} else if len(groups) > 0 {
		m.addSystemMsg(fmt.Sprintf("joining ~%s ...", groups[0].Name))
	} else {
		m.addSystemMsg("no rooms configured — use /channel create #name or /join <event-id>")
	}

	cmds := []tea.Cmd{
		textarea.Blink,
		subscribeDMCmd(m.pool, m.relays, m.kr, m.lastDMSeen),
		publishDMRelaysCmd(m.pool, m.relays, m.keys),
		fetchNIP51ListsCmd(m.pool, m.relays, m.keys, m.kr),
	}
	if len(channels) > 0 && m.isChannelSelected() {
		cmds = append(cmds, subscribeChannelCmd(m.pool, m.relays, channels[0].ID))
	}
	if len(groups) > 0 && m.isGroupSelected() {
		cmds = append(cmds, subscribeGroupCmd(m.pool, groups[0].RelayURL, groups[0].GroupID))
	}
	if m.cfg.Profile.Name != "" || m.cfg.Profile.DisplayName != "" || m.cfg.Profile.About != "" || m.cfg.Profile.Picture != "" {
		cmds = append(cmds, publishProfileCmd(m.pool, m.relays, m.cfg.Profile, m.keys))
	}
	// Fetch profiles for all known DM peers so display names are up to date.
	for _, peer := range m.allDMPeers() {
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
	if item := m.activeSidebarItem(); item != nil {
		key := item.ItemID()
		m.msgs[key] = appendMessage(m.msgs[key], msg, m.cfg.MaxMessages)
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
	if item := m.activeSidebarItem(); item != nil {
		delete(m.unread, item.ItemID())
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

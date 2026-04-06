package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fiatjaf.com/nostr"
	"github.com/charmbracelet/glamour"
	qrterminal "github.com/mdp/qrterminal/v3"
)

// Group represents a NIP-29 relay-based group.
type Group struct {
	RelayURL    string
	GroupID     string
	Name        string
	RelayPubKey string // pubkey of the relay (author of kind 39000 metadata)
}

type model struct {
	// Configurable keybindings
	keymap KeyMap

	// Theme (dark/light colours and styles)
	theme Theme

	// Config and keys
	cfg         Config
	cfgFlagPath string
	keys        Keys
	pool        *nostr.Pool
	kr          nostr.Keyer

	// Focus tracking for notifications
	focused   bool
	startedAt nostr.Timestamp // suppress notifications for messages older than startup
	relays    []string

	// TUI dimensions
	width  int
	height int

	// Unified sidebar — single source of truth for channels, groups, and DMs.
	// Layout order: all channels first, then all groups, then all DMs.
	activeItem int
	sidebar    []SidebarItem

	// Per-room subscriptions — all channels and groups are subscribed simultaneously.
	roomSubs map[string]*roomSub

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

	// Global messages (shown when no channel/DM is active)
	globalMsgs []ChatMessage

	// Dedup
	seenEvents      map[string]time.Time
	seenEventsClean time.Time            // last time stale entries were evicted
	localDMEchoes   map[string]time.Time // "peer:content" keys for sent DMs awaiting relay echo

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

	// Channel selector popup state
	showChannelSelector  bool
	channelSelectorInput string
	channelSelectorItems []SidebarItem // filtered items
	channelSelectorIndex int           // selected index in filtered list

	// Mouse selection state
	selecting  bool
	selectFrom [2]int // [x, y] screen coordinates at press
	selectTo   [2]int // [x, y] screen coordinates during drag

	// NIP-51 list timestamps — used to detect whether relay data is newer.
	contactsListTS nostr.Timestamp
	channelsListTS nostr.Timestamp
	groupsListTS   nostr.Timestamp
	nip51Loaded    bool // true after the initial NIP-51 fetch completes

	// fetchedContacts preserves the contacts loaded from the relay so that
	// publishing never drops peers that are in the relay list but missing
	// from the sidebar (e.g. due to replay-guard filtering during startup).
	fetchedContacts map[string]bool // pubkeys from the last NIP-51 contacts fetch

	// Logging
	logDir string // empty = logging disabled

	// Cache directory for downloaded attachments ($XDG_CACHE_HOME/nitrous).
	cacheDir string
}

// roomSub holds a per-room subscription (channel or group).
type roomSub struct {
	kind   SidebarKind
	roomID string // channel ID or groupKey
	events <-chan nostr.RelayEvent
	cancel context.CancelFunc
}

// waitForRoomSub returns a Cmd that waits for the next event on a specific room subscription.
func waitForRoomSub(sub *roomSub, keys Keys) tea.Cmd {
	if sub == nil {
		return nil
	}
	switch sub.kind {
	case SidebarChannel:
		return waitForChannelEvent(sub.events, sub.roomID, keys)
	case SidebarGroup:
		relayURL, _ := splitGroupKey(sub.roomID)
		return waitForGroupEvent(sub.events, sub.roomID, relayURL, keys)
	}
	return nil
}

// cancelRoomSub cancels and removes a specific room subscription.
func (m *model) cancelRoomSub(roomID string) {
	if sub, ok := m.roomSubs[roomID]; ok {
		sub.cancel()
		delete(m.roomSubs, roomID)
	}
}

// cancelAllRoomSubs cancels all room subscriptions.
func (m *model) cancelAllRoomSubs() {
	for id, sub := range m.roomSubs {
		sub.cancel()
		delete(m.roomSubs, id)
	}
}

// quit cancels all subscriptions and returns the quit command. Used by
// the ctrl+c keybinding and the /exit slash command.
func (m *model) quit() (tea.Model, tea.Cmd) {
	m.cancelAllRoomSubs()
	if m.dmCancel != nil {
		m.dmCancel()
	}
	return m, tea.Quit
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

func newModel(cfg Config, cfgFlagPath string, keys Keys, pool *nostr.Pool, kr nostr.Keyer, mdRender *glamour.TermRenderer, theme Theme, keymap KeyMap) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (/help for commands)"
	ta.Prompt = "> "
	ta.CharLimit = 2000
	ta.SetHeight(inputMinHeight)
	ta.MaxHeight = inputMaxHeight
	ta.ShowLineNumbers = false
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ta.Focus()

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	// Pre-cache own display name from config fallback chain.
	ownName := shortPK(keys.PK.Hex())
	if cfg.Profile.DisplayName != "" {
		ownName = cfg.Profile.DisplayName
	} else if cfg.Profile.Name != "" {
		ownName = cfg.Profile.Name
	}

	profiles := map[string]string{keys.PK.Hex(): ownName}

	// Sidebar starts empty — populated by NIP-51 fetch from relays.

	lastSeen := LoadLastDMSeen(cfgFlagPath)

	// Resolve log directory.
	var logDir string
	if cfg.LoggingEnabled() {
		if cfg.LogDir != "" {
			logDir = cfg.LogDir
		} else {
			logDir = filepath.Join(filepath.Dir(configPath(cfgFlagPath)), "logs")
		}
	}

	// Resolve cache directory for attachments.
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			cacheDir = filepath.Join(home, ".cache")
		}
	}
	cacheDir = filepath.Join(cacheDir, "nitrous")

	return model{
		keymap:          keymap,
		theme:           theme,
		cfg:             cfg,
		cfgFlagPath:     cfgFlagPath,
		keys:            keys,
		pool:            pool,
		kr:              kr,
		focused:         true,
		startedAt:       nostr.Now(),
		relays:          cfg.Relays,
		width:           80,
		height:          24,
		activeItem:      0,
		roomSubs:        make(map[string]*roomSub),
		groupRecentIDs:  make(map[string][]string),
		msgs:            make(map[string][]ChatMessage),
		lastDMSeen:      lastSeen,
		dmSeenAtStart:   lastSeen,
		seenEvents:      make(map[string]time.Time),
		seenEventsClean: time.Now(),
		unread:          make(map[string]bool),
		localDMEchoes:   make(map[string]time.Time),
		profiles:        profiles,
		profilePending:  make(map[string]bool),
		fetchedContacts: make(map[string]bool),
		lastInputHeight: inputMinHeight,
		historyIndex:    -1,
		viewport:        vp,
		input:           ta,
		mdRender:        mdRender,
		statusMsg:       fmt.Sprintf("connected to %d relays", len(cfg.Relays)),
		logDir:          logDir,
		cacheDir:        cacheDir,
	}
}

func (m *model) Init() tea.Cmd {
	log.Println("Init() called")
	m.addSystemMsg("nitrous — nostr chat")
	m.addSystemMsg(fmt.Sprintf("npub: %s", m.keys.NPub))
	for _, r := range m.relays {
		m.addSystemMsg(fmt.Sprintf("connecting to %s ...", r))
	}
	m.addSystemMsg("fetching lists from relays ...")

	cmds := []tea.Cmd{
		textarea.Blink,
		subscribeDMCmd(m.pool, m.relays, m.kr, m.lastDMSeen),
		publishDMRelaysCmd(m.pool, m.relays, m.keys),
		fetchNIP51ListsCmd(m.pool, m.relays, m.keys, m.kr),
	}
	if m.cfg.Profile.Name != "" || m.cfg.Profile.DisplayName != "" || m.cfg.Profile.About != "" || m.cfg.Profile.Picture != "" {
		cmds = append(cmds, publishProfileCmd(m.pool, m.relays, m.cfg.Profile, m.keys))
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

// pubkeyByName returns the hex pubkey for a display name, searching the profiles map.
// Returns empty string if no match or ambiguous.
func (m *model) pubkeyByName(name string) string {
	lower := strings.ToLower(name)
	var match string
	for pk, n := range m.profiles {
		if strings.ToLower(n) == lower {
			if match != "" {
				return "" // ambiguous
			}
			match = pk
		}
	}
	return match
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

// updateChannelSelectorItems filters the sidebar items based on the current input.
func (m *model) updateChannelSelectorItems() {
	m.channelSelectorItems = nil
	filter := strings.ToLower(m.channelSelectorInput)

	for _, item := range m.sidebar {
		displayName := strings.ToLower(item.DisplayName())
		// Prefix matching - check if the display name starts with the filter
		if strings.HasPrefix(displayName, filter) {
			m.channelSelectorItems = append(m.channelSelectorItems, item)
		}
	}

	// Reset index if it's out of bounds
	if m.channelSelectorIndex >= len(m.channelSelectorItems) {
		m.channelSelectorIndex = 0
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

// loadHistory loads message history from a log file and marks event IDs as seen.
// It returns the set of unique author pubkeys found so profiles can be fetched.
func (m *model) loadHistory(roomType, roomKey string) []string {
	msgs, err := loadLogHistory(m.logDir, roomType, roomKey, m.cfg.MaxMessages)
	if err != nil {
		log.Printf("loadHistory: %v", err)
		return nil
	}
	seen := make(map[string]bool)
	for _, msg := range msgs {
		if msg.EventID != "" {
			m.markSeenEvent(msg.EventID)
		}
		m.msgs[roomKey] = appendMessage(m.msgs[roomKey], msg, m.cfg.MaxMessages)
		if msg.PubKey != "" {
			seen[msg.PubKey] = true
		}
	}
	if len(msgs) > 0 {
		log.Printf("loadHistory: loaded %d messages for %s/%s", len(msgs), roomType, roomKey)
	}
	var authors []string
	for pk := range seen {
		authors = append(authors, pk)
	}
	return authors
}

// renderQR renders a QR code with a title line above it.
func (m *model) renderQR(title, content string) string {
	var buf strings.Builder
	buf.WriteString(m.theme.QRTitle.Render(title))
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
	buf.WriteString(m.theme.ChatSystem.Render(content))
	return buf.String()
}

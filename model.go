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

// Group represents a NIP-29 relay-based group.
type Group struct {
	RelayURL string
	GroupID  string
	Name     string
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
		groups:          groups,
		groupMsgs:       make(map[string][]ChatMessage),
		groupRecentIDs:  make(map[string][]string),
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
	} else if len(m.groups) > 0 {
		m.addSystemMsg(fmt.Sprintf("joining ~%s ...", m.groups[0].Name))
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
		if m.isDMSelected() {
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

	case groupSubStartedMsg:
		log.Println("groupSubStartedMsg received")
		if m.groupCancel != nil {
			m.groupCancel()
		}
		m.groupSubKey = msg.groupKey
		m.groupEvents = msg.events
		m.groupCancel = msg.cancel
		if _, ok := m.groupRecentIDs[msg.groupKey]; !ok {
			m.groupRecentIDs[msg.groupKey] = nil
		}
		if idx := m.activeGroupIdx(); idx >= 0 && idx < len(m.groups) {
			m.addSystemMsg("subscribed to ~" + m.groups[idx].Name)
		}
		subRelayURL, _ := splitGroupKey(m.groupSubKey)
		return m, waitForGroupEvent(m.groupEvents, m.groupSubKey, subRelayURL, m.keys)

	case groupEventMsg:
		cm := ChatMessage(msg)
		log.Printf("groupEventMsg: author=%s group=%s id=%s", cm.Author, cm.GroupKey, cm.EventID)
		if m.seenEvents[cm.EventID] {
			if m.groupEvents != nil {
				subRelayURL, _ := splitGroupKey(m.groupSubKey)
				return m, waitForGroupEvent(m.groupEvents, m.groupSubKey, subRelayURL, m.keys)
			}
			return m, nil
		}
		m.seenEvents[cm.EventID] = true
		gk := cm.GroupKey
		// Track recent event IDs for NIP-29 "previous" tags.
		ids := m.groupRecentIDs[gk]
		ids = append(ids, cm.EventID)
		if len(ids) > 50 {
			ids = ids[len(ids)-50:]
		}
		m.groupRecentIDs[gk] = ids
		m.groupMsgs[gk] = appendMessage(m.groupMsgs[gk], cm, m.cfg.MaxMessages)
		if gk == m.activeGroupKey() {
			m.updateViewport()
		}
		var batchCmds []tea.Cmd
		if profileCmd := m.maybeRequestProfile(cm.PubKey); profileCmd != nil {
			batchCmds = append(batchCmds, profileCmd)
		}
		if m.groupEvents != nil {
			subRelayURL, _ := splitGroupKey(m.groupSubKey)
			batchCmds = append(batchCmds, waitForGroupEvent(m.groupEvents, m.groupSubKey, subRelayURL, m.keys))
		}
		return m, tea.Batch(batchCmds...)

	case groupSubEndedMsg:
		log.Printf("groupSubEndedMsg: group %s subscription ended, scheduling reconnect", msg.groupKey)
		m.groupEvents = nil
		if msg.groupKey == m.activeGroupKey() {
			m.addSystemMsg("group subscription lost, reconnecting...")
		}
		return m, groupReconnectDelayCmd(msg.groupKey)

	case groupReconnectMsg:
		log.Printf("groupReconnectMsg: reconnecting group %s", msg.groupKey)
		if msg.groupKey == m.activeGroupKey() {
			if idx := m.activeGroupIdx(); idx >= 0 {
				g := m.groups[idx]
				return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
			}
		}
		return m, nil

	case groupMetaMsg:
		log.Printf("groupMetaMsg: relay=%s group=%s name=%q", msg.RelayURL, msg.GroupID, msg.Name)
		for i, g := range m.groups {
			if g.RelayURL == msg.RelayURL && g.GroupID == msg.GroupID {
				m.groups[i].Name = msg.Name
				break
			}
		}
		if err := UpdateSavedGroupName(m.cfgFlagPath, msg.RelayURL, msg.GroupID, msg.Name); err != nil {
			log.Printf("groupMetaMsg: failed to update group name: %v", err)
		}
		m.updateViewport()
		return m, nil

	case groupCreatedMsg:
		log.Printf("groupCreatedMsg: relay=%s group=%s name=%q", msg.RelayURL, msg.GroupID, msg.Name)
		// Check if already in list (shouldn't happen, but be safe).
		for i, g := range m.groups {
			if g.RelayURL == msg.RelayURL && g.GroupID == msg.GroupID {
				m.activeItem = len(m.channels) + i
				m.updateViewport()
				return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
			}
		}
		m.groups = append(m.groups, Group{RelayURL: msg.RelayURL, GroupID: msg.GroupID, Name: msg.Name})
		m.activeItem = len(m.channels) + len(m.groups) - 1
		if err := AppendSavedGroup(m.cfgFlagPath, SavedGroup{Name: msg.Name, RelayURL: msg.RelayURL, GroupID: msg.GroupID}); err != nil {
			log.Printf("groupCreatedMsg: failed to save group: %v", err)
			m.addSystemMsg("failed to save group: " + err.Error())
		}
		m.addSystemMsg(fmt.Sprintf("created group ~%s on %s", msg.Name, msg.RelayURL))
		m.updateViewport()
		gk := groupKey(msg.RelayURL, msg.GroupID)
		return m, tea.Batch(
			subscribeGroupCmd(m.pool, msg.RelayURL, msg.GroupID),
			// Kind 9007 (create) doesn't set metadata on most relays;
			// publish a kind 9002 (edit metadata) to set the name.
			editGroupMetadataCmd(m.pool, msg.RelayURL, msg.GroupID, map[string]string{"name": msg.Name}, m.groupRecentIDs[gk], m.keys),
		)

	case groupInviteCreatedMsg:
		log.Printf("groupInviteCreatedMsg: relay=%s group=%s code=%s", msg.RelayURL, msg.GroupID, msg.Code)
		m.addSystemMsg(fmt.Sprintf("invite code: %s", msg.Code))
		return m, nil

	case groupJoinedMsg:
		log.Printf("groupJoinedMsg: relay=%s group=%s", msg.RelayURL, msg.GroupID)
		// Check if already in list
		for i, g := range m.groups {
			if g.RelayURL == msg.RelayURL && g.GroupID == msg.GroupID {
				m.activeItem = len(m.channels) + i
				m.updateViewport()
				return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
			}
		}
		name := msg.Name
		if name == "" {
			name = shortPK(msg.GroupID)
		}
		m.groups = append(m.groups, Group{RelayURL: msg.RelayURL, GroupID: msg.GroupID, Name: name})
		m.activeItem = len(m.channels) + len(m.groups) - 1
		if err := AppendSavedGroup(m.cfgFlagPath, SavedGroup{Name: name, RelayURL: msg.RelayURL, GroupID: msg.GroupID}); err != nil {
			log.Printf("groupJoinedMsg: failed to save group: %v", err)
			m.addSystemMsg("failed to save group: " + err.Error())
		}
		m.updateViewport()
		return m, tea.Batch(
			subscribeGroupCmd(m.pool, msg.RelayURL, msg.GroupID),
			fetchGroupMetaCmd(m.pool, msg.RelayURL, msg.GroupID),
		)

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
				if m.groupCancel != nil {
					m.groupCancel()
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
			if m.groupCancel != nil {
				m.groupCancel()
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
				return m, m.subscribeIfNeeded(prev)
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
				return m, m.subscribeIfNeeded(prev)
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
			if m.isGroupSelected() && len(m.groups) > 0 {
				g := m.groups[m.activeGroupIdx()]
				gk := groupKey(g.RelayURL, g.GroupID)
				return m, publishGroupMessage(m.pool, g.RelayURL, g.GroupID, text, m.groupRecentIDs[gk], m.keys)
			}
			if m.isDMSelected() && len(m.dmPeers) > 0 {
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
		if arg == "" {
			m.addSystemMsg("usage: /create #roomname | /create ~groupname wss://relay")
			return m, nil
		}
		if strings.HasPrefix(arg, "#") {
			name := strings.TrimPrefix(arg, "#")
			return m, createChannelCmd(m.pool, m.relays, name, m.keys)
		}
		if strings.HasPrefix(arg, "~") {
			createParts := strings.Fields(arg)
			if len(createParts) < 2 || !strings.HasPrefix(createParts[1], "wss://") {
				m.addSystemMsg("usage: /create ~groupname wss://relay")
				return m, nil
			}
			name := strings.TrimPrefix(createParts[0], "~")
			relayURL := createParts[1]
			return m, createGroupCmd(m.pool, relayURL, name, m.keys)
		}
		m.addSystemMsg("usage: /create #roomname | /create ~groupname wss://relay")
		return m, nil

	case "/join":
		if arg == "" {
			m.addSystemMsg("usage: /join #name | <event-id> | naddr1... | host'groupid")
			return m, nil
		}
		// NIP-29 group: naddr or host'groupid
		if strings.HasPrefix(arg, "naddr") || strings.Contains(arg, "'") {
			return m.joinGroup(arg)
		}
		return m.joinChannel(arg)

	case "/dm":
		if arg == "" {
			m.addSystemMsg("usage: /dm <npub or hex pubkey>")
			return m, nil
		}
		return m.openDM(arg)

	case "/me":
		m.qrOverlay = renderQR("Your npub:", "nostr:"+m.keys.NPub)
		return m, nil

	case "/room":
		if m.isChannelSelected() && len(m.channels) > 0 {
			ch := m.channels[m.activeChannelIdx()]
			nevent, err := nip19.EncodeEvent(ch.ID, m.relays, "")
			if err != nil {
				m.addSystemMsg(fmt.Sprintf("encode error: %v", err))
				return m, nil
			}
			m.qrOverlay = renderQR("#"+ch.Name, "nostr:"+nevent)
			return m, nil
		}
		if m.isGroupSelected() && len(m.groups) > 0 {
			g := m.groups[m.activeGroupIdx()]
			naddr, err := nip19.EncodeEntity("", nostr.KindSimpleGroupMetadata, g.GroupID, []string{g.RelayURL})
			if err != nil {
				m.addSystemMsg(fmt.Sprintf("encode error: %v", err))
				return m, nil
			}
			m.qrOverlay = renderQR("~"+g.Name, "nostr:"+naddr)
			return m, nil
		}
		m.addSystemMsg("no active channel or group — switch to one first")
		return m, nil

	case "/delete":
		if !m.isGroupSelected() || len(m.groups) == 0 {
			m.addSystemMsg("/delete only works in a NIP-29 group")
			return m, nil
		}
		g := m.groups[m.activeGroupIdx()]
		gk := groupKey(g.RelayURL, g.GroupID)
		if arg != "" {
			// Delete by explicit event ID (admin use).
			// Remove from local messages.
			msgs := m.groupMsgs[gk]
			for i, cm := range msgs {
				if cm.EventID == arg {
					m.groupMsgs[gk] = append(msgs[:i], msgs[i+1:]...)
					break
				}
			}
			m.updateViewport()
			return m, deleteGroupEventCmd(m.pool, g.RelayURL, g.GroupID, arg, m.groupRecentIDs[gk], m.keys)
		}
		// No arg: delete the last own message.
		msgs := m.groupMsgs[gk]
		var targetID string
		var targetIdx int
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].IsMine && msgs[i].Author != "system" {
				targetID = msgs[i].EventID
				targetIdx = i
				break
			}
		}
		if targetID == "" {
			m.addSystemMsg("no own message to delete")
			return m, nil
		}
		m.groupMsgs[gk] = append(msgs[:targetIdx], msgs[targetIdx+1:]...)
		m.updateViewport()
		return m, deleteGroupEventCmd(m.pool, g.RelayURL, g.GroupID, targetID, m.groupRecentIDs[gk], m.keys)

	case "/name":
		if !m.isGroupSelected() || len(m.groups) == 0 {
			m.addSystemMsg("/name only works in a NIP-29 group")
			return m, nil
		}
		if arg == "" {
			m.addSystemMsg("usage: /name <new-name>")
			return m, nil
		}
		idx := m.activeGroupIdx()
		g := m.groups[idx]
		gk := groupKey(g.RelayURL, g.GroupID)
		m.groups[idx].Name = arg
		if err := UpdateSavedGroupName(m.cfgFlagPath, g.RelayURL, g.GroupID, arg); err != nil {
			log.Printf("/name: failed to save: %v", err)
		}
		m.updateViewport()
		return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"name": arg}, m.groupRecentIDs[gk], m.keys)

	case "/about":
		if !m.isGroupSelected() || len(m.groups) == 0 {
			m.addSystemMsg("/about only works in a NIP-29 group")
			return m, nil
		}
		if arg == "" {
			m.addSystemMsg("usage: /about <description>")
			return m, nil
		}
		g := m.groups[m.activeGroupIdx()]
		gk := groupKey(g.RelayURL, g.GroupID)
		return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"about": arg}, m.groupRecentIDs[gk], m.keys)

	case "/picture":
		if !m.isGroupSelected() || len(m.groups) == 0 {
			m.addSystemMsg("/picture only works in a NIP-29 group")
			return m, nil
		}
		if arg == "" {
			m.addSystemMsg("usage: /picture <url>")
			return m, nil
		}
		g := m.groups[m.activeGroupIdx()]
		gk := groupKey(g.RelayURL, g.GroupID)
		return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"picture": arg}, m.groupRecentIDs[gk], m.keys)

	case "/invite":
		if !m.isGroupSelected() || len(m.groups) == 0 {
			m.addSystemMsg("/invite only works in a NIP-29 group")
			return m, nil
		}
		g := m.groups[m.activeGroupIdx()]
		gk := groupKey(g.RelayURL, g.GroupID)
		return m, createGroupInviteCmd(m.pool, g.RelayURL, g.GroupID, m.groupRecentIDs[gk], m.keys)

	case "/leave":
		return m.leaveCurrentItem()

	case "/help":
		m.addSystemMsg("/create #name — create a NIP-28 channel")
		m.addSystemMsg("/create ~name wss://relay — create a NIP-29 group on a relay")
		m.addSystemMsg("/join #name — join a channel from your rooms file")
		m.addSystemMsg("/join <event-id> — join a channel by ID")
		m.addSystemMsg("/join naddr1... [code] — join a NIP-29 group (with optional invite code)")
		m.addSystemMsg("/join host'groupid [code] — join a NIP-29 group")
		m.addSystemMsg("/dm <npub> — open a DM conversation")
		m.addSystemMsg("/name <new-name> — edit group name")
		m.addSystemMsg("/about <text> — edit group description")
		m.addSystemMsg("/picture <url> — edit group picture")
		m.addSystemMsg("/invite — create an invite code for the current group")
		m.addSystemMsg("/delete — delete your last message in the current group")
		m.addSystemMsg("/delete <event-id> — delete a message by ID (admin)")
		m.addSystemMsg("/leave — leave the current channel, group, or DM")
		m.addSystemMsg("/me — show QR code of your npub")
		m.addSystemMsg("/room — show QR code of the current channel or group")
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

// joinGroup handles /join for NIP-29 groups (naddr or host'groupid format).
// An optional invite code can be appended after the address.
func (m *model) joinGroup(arg string) (tea.Model, tea.Cmd) {
	// Split off invite code if present: "naddr1... code123" or "host'group code123"
	parts := strings.Fields(arg)
	address := parts[0]
	inviteCode := ""
	if len(parts) > 1 {
		inviteCode = parts[1]
	}

	relayURL, groupID, err := parseGroupInput(address)
	if err != nil {
		m.addSystemMsg("invalid group address: " + err.Error())
		return m, nil
	}

	gk := groupKey(relayURL, groupID)

	// Check if already known
	for i, g := range m.groups {
		if g.RelayURL == relayURL && g.GroupID == groupID {
			m.activeItem = len(m.channels) + i
			m.updateViewport()
			return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
		}
	}

	// New group — send join request, then handle groupJoinedMsg
	return m, tea.Batch(
		joinGroupCmd(m.pool, relayURL, groupID, m.groupRecentIDs[gk], inviteCode, m.keys),
		fetchGroupMetaCmd(m.pool, relayURL, groupID),
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
			m.activeItem = len(m.channels) + len(m.groups) + i
			break
		}
	}
	m.updateViewport()
	return m, nil
}

// leaveCurrentItem removes the currently selected channel, group, or DM from the
// sidebar and deletes it from the persistence file.
func (m *model) leaveCurrentItem() (tea.Model, tea.Cmd) {
	total := m.sidebarTotal()
	if total == 0 {
		m.addSystemMsg("nothing to leave")
		return m, nil
	}

	var leaveCmds []tea.Cmd

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
	} else if m.isGroupSelected() && len(m.groups) > 0 {
		idx := m.activeGroupIdx()
		g := m.groups[idx]
		gk := groupKey(g.RelayURL, g.GroupID)

		// Cancel subscription if this is the active one.
		if gk == m.groupSubKey && m.groupCancel != nil {
			m.groupCancel()
			m.groupEvents = nil
			m.groupCancel = nil
			m.groupSubKey = ""
		}

		// Remove from groups list and message history.
		m.groups = append(m.groups[:idx], m.groups[idx+1:]...)
		delete(m.groupMsgs, gk)

		// Remove from groups file.
		if err := RemoveSavedGroup(m.cfgFlagPath, g.RelayURL, g.GroupID); err != nil {
			log.Printf("leaveCurrentItem: failed to remove group: %v", err)
		}

		// Send leave request.
		leaveCmds = append(leaveCmds, leaveGroupCmd(m.pool, g.RelayURL, g.GroupID, m.groupRecentIDs[gk], m.keys))

		// Clean up recent IDs tracking.
		delete(m.groupRecentIDs, gk)

		log.Printf("leaveCurrentItem: left group ~%s", g.Name)
	} else if m.isDMSelected() && len(m.dmPeers) > 0 {
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

	// Subscribe to the new active item if needed.
	if m.isChannelSelected() && len(m.channels) > 0 {
		leaveCmds = append(leaveCmds, subscribeChannelCmd(m.pool, m.relays, m.activeChannelID()))
	} else if m.isGroupSelected() && len(m.groups) > 0 {
		g := m.groups[m.activeGroupIdx()]
		leaveCmds = append(leaveCmds, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID))
	}

	if len(leaveCmds) > 0 {
		return m, tea.Batch(leaveCmds...)
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

// sidebarWidth returns the width needed for the sidebar based on the longest
// channel name, group name, or DM peer display name.
func (m *model) sidebarWidth() int {
	longest := 0
	for _, ch := range m.channels {
		if n := len(ch.Name); n > longest {
			longest = n
		}
	}
	for _, g := range m.groups {
		if n := len(g.Name); n > longest {
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
	} else if m.isGroupSelected() && len(m.groups) > 0 {
		gk := m.activeGroupKey()
		msgs = m.groupMsgs[gk]
	} else if m.isDMSelected() && len(m.dmPeers) > 0 {
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

	// GROUPS section
	items = append(items, sidebarSectionStyle.Render("GROUPS"))
	for i, g := range m.groups {
		name := "~" + g.Name
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

	// DMS section
	items = append(items, sidebarSectionStyle.Render("DMS"))
	for i, peer := range m.dmPeers {
		name := "@" + m.resolveAuthor(peer)
		if len(name) > sw-2 {
			name = name[:sw-2]
		}
		idx := len(m.channels) + len(m.groups) + i
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
	} else if m.isGroupSelected() && len(m.groups) > 0 {
		title = "~" + m.groups[m.activeGroupIdx()].Name
	} else if m.isDMSelected() && len(m.dmPeers) > 0 {
		title = "@" + m.resolveAuthor(m.dmPeers[m.activeDMPeerIdx()])
	}

	titleBar := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Padding(0, 1).Render(title)
	inputView := m.input.View()
	vp := m.viewport.View()

	inner := lipgloss.JoinVertical(lipgloss.Left, titleBar, vp, inputView)

	return lipgloss.NewStyle().Height(totalHeight).MaxHeight(totalHeight).Render(inner)
}

func (m *model) viewStatusBar() string {
	left := statusConnectedStyle.Render(fmt.Sprintf("● %d relays · %d rooms · %d groups", len(m.relays), len(m.channels), len(m.groups)))
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

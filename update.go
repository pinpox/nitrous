package main

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
)

const seenEventsTTL = 30 * time.Minute
const seenEventsCleanInterval = 5 * time.Minute

// isSeenEvent checks whether an event ID has already been seen.
func (m *model) isSeenEvent(eventID string) bool {
	_, ok := m.seenEvents[eventID]
	return ok
}

// markSeenEvent records an event ID and periodically evicts stale entries.
func (m *model) markSeenEvent(eventID string) {
	now := time.Now()
	m.seenEvents[eventID] = now
	if now.Sub(m.seenEventsClean) > seenEventsCleanInterval {
		for k, ts := range m.seenEvents {
			if now.Sub(ts) > seenEventsTTL {
				delete(m.seenEvents, k)
			}
		}
		m.seenEventsClean = now
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case channelCreatedMsg:
		return m.handleChannelCreated(msg)
	case channelMetaMsg:
		return m.handleChannelMeta(msg)
	case channelSubStartedMsg:
		return m.handleChannelSubStarted(msg)
	case dmSubStartedMsg:
		return m.handleDMSubStarted(msg)
	case channelEventMsg:
		return m.handleChannelEvent(msg)
	case dmEventMsg:
		return m.handleDMEvent(msg)
	case dmSubEndedMsg:
		return m.handleDMSubEnded(msg)
	case dmReconnectMsg:
		return m.handleDMReconnect(msg)
	case channelSubEndedMsg:
		return m.handleChannelSubEnded(msg)
	case channelReconnectMsg:
		return m.handleChannelReconnect(msg)
	case groupSubStartedMsg:
		return m.handleGroupSubStarted(msg)
	case groupEventMsg:
		return m.handleGroupEvent(msg)
	case groupSubEndedMsg:
		return m.handleGroupSubEnded(msg)
	case groupReconnectMsg:
		return m.handleGroupReconnect(msg)
	case groupMetaMsg:
		return m.handleGroupMeta(msg)
	case groupCreatedMsg:
		return m.handleGroupCreated(msg)
	case groupInviteCreatedMsg:
		return m.handleGroupInviteCreated(msg)
	case groupJoinedMsg:
		return m.handleGroupJoined(msg)
	case profileResolvedMsg:
		return m.handleProfileResolved(msg)
	case nip05ResolvedMsg:
		return m.handleNIP05Resolved(msg)
	case nostrErrMsg:
		return m.handleNostrErr(msg)
	case dmSendErrMsg:
		return m.handleDMSendErr(msg)
	case blossomUploadMsg:
		return m.handleBlossomUpload(msg)
	case blossomUploadErrMsg:
		return m.handleBlossomUploadErr(msg)
	case nip51ListsFetchedMsg:
		return m.handleNIP51ListsFetched(msg)
	case profilePublishedMsg:
		return m.handleProfilePublished(msg)
	case dmRelaysPublishedMsg:
		return m.handleDMRelaysPublished(msg)
	case nip51PublishResultMsg:
		return m.handleNIP51PublishResult(msg)
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	}
	return m.handleInputUpdate(msg)
}

func (m *model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	log.Printf("WindowSizeMsg: %dx%d", msg.Width, msg.Height)
	m.width = msg.Width
	m.height = msg.Height
	m.updateLayout()
	return m, tea.ClearScreen
}

func (m *model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.viewport.ScrollUp(3)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.viewport.ScrollDown(3)
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionPress && msg.X < m.sidebarWidth() {
			if idx, ok := m.sidebarItemAt(msg.Y); ok {
				m.activeItem = idx
				m.clearUnread()
				m.updateViewport()
			}
		}
		return m, nil
	}
	return m, nil
}

func (m *model) handleChannelCreated(msg channelCreatedMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelCreatedMsg: id=%s name=%q", msg.ID, msg.Name)
	idx := m.appendChannelItem(Channel{ID: msg.ID, Name: msg.Name})
	m.activeItem = idx
	m.updateViewport()
	return m, tea.Batch(
		subscribeChannelCmd(m.pool, m.relays, msg.ID),
		publishPublicChatsListCmd(m.pool, m.relays, m.allChannels(), m.keys),
	)
}

func (m *model) handleChannelMeta(msg channelMetaMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelMetaMsg: id=%s name=%q", msg.ID, msg.Name)
	m.updateChannelName(msg.ID, msg.Name)
	return m, nil
}

func (m *model) handleChannelSubStarted(msg channelSubStartedMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelSubStartedMsg: channel=%s", shortPK(msg.channelID))
	// Cancel any existing subscription for this channel (e.g. reconnect).
	m.cancelRoomSub(msg.channelID)
	sub := &roomSub{kind: SidebarChannel, roomID: msg.channelID, events: msg.events, cancel: msg.cancel}
	m.roomSubs[msg.channelID] = sub
	// Load log history if no messages are loaded yet.
	if len(m.msgs[msg.channelID]) == 0 {
		m.loadHistory("channel", msg.channelID)
	}
	return m, waitForRoomSub(sub, m.keys)
}

func (m *model) handleDMSubStarted(msg dmSubStartedMsg) (tea.Model, tea.Cmd) {
	log.Println("dmSubStartedMsg received")
	if m.dmCancel != nil {
		m.dmCancel()
	}
	m.dmEvents = msg.events
	m.dmCancel = msg.cancel
	return m, waitForDMEvent(m.dmEvents, m.keys)
}

func (m *model) handleChannelEvent(msg channelEventMsg) (tea.Model, tea.Cmd) {
	cm := ChatMessage(msg)
	log.Printf("channelEventMsg: author=%s channel=%s id=%s content=%q", cm.Author, cm.ChannelID, cm.EventID, cm.Content)
	sub := m.roomSubs[cm.ChannelID]
	if m.isSeenEvent(cm.EventID) {
		return m, waitForRoomSub(sub, m.keys)
	}
	m.markSeenEvent(cm.EventID)
	chID := cm.ChannelID
	m.msgs[chID] = appendMessage(m.msgs[chID], cm, m.cfg.MaxMessages)
	appendLogEntry(m.logDir, "channel", chID, cm, m.resolveAuthor(cm.PubKey))
	if chID == m.activeChannelID() {
		m.updateViewport()
	} else {
		m.unread[chID] = true
	}
	var batchCmds []tea.Cmd
	if profileCmd := m.maybeRequestProfile(cm.PubKey); profileCmd != nil {
		batchCmds = append(batchCmds, profileCmd)
	}
	batchCmds = append(batchCmds, waitForRoomSub(sub, m.keys))
	return m, tea.Batch(batchCmds...)
}

func (m *model) handleDMEvent(msg dmEventMsg) (tea.Model, tea.Cmd) {
	cm := ChatMessage(msg)
	log.Printf("dmEventMsg: author=%s id=%s mine=%v content=%q", cm.Author, cm.EventID, cm.IsMine, cm.Content)
	if m.isSeenEvent(cm.EventID) {
		if m.dmEvents != nil {
			return m, waitForDMEvent(m.dmEvents, m.keys)
		}
		return m, nil
	}
	m.markSeenEvent(cm.EventID)

	// Content-based dedup for our own DMs: the local echo from sendDM and
	// the relay echo from the subscription have different synthetic EventIDs,
	// so seenEvents can't catch the duplicate. Track by peer+content instead.
	if cm.IsMine {
		echoKey := cm.PubKey + ":" + cm.Content
		if _, ok := m.localDMEchoes[echoKey]; ok {
			log.Printf("dmEventMsg: skipping relay echo (already have local echo)")
			delete(m.localDMEchoes, echoKey)
			if m.dmEvents != nil {
				return m, waitForDMEvent(m.dmEvents, m.keys)
			}
			return m, nil
		}
		// Evict stale entries older than 5 minutes before adding a new one.
		const localDMEchoTTL = 5 * time.Minute
		now := time.Now()
		for k, ts := range m.localDMEchoes {
			if now.Sub(ts) > localDMEchoTTL {
				delete(m.localDMEchoes, k)
			}
		}
		m.localDMEchoes[echoKey] = now
	}

	peer := cm.PubKey
	m.msgs[peer] = appendMessage(m.msgs[peer], cm, m.cfg.MaxMessages)
	appendLogEntry(m.logDir, "dm", peer, cm, m.resolveAuthor(cm.PubKey))

	newPeer := false
	if !m.containsDMPeer(peer) {
		// Only auto-add unknown peers for genuinely new DMs, not replayed
		// history. Replayed messages from peers the user previously left
		// (removed from NIP-51 contacts) are stored but the peer is not
		// re-added to the sidebar.
		if cm.Timestamp <= m.dmSeenAtStart {
			log.Printf("dmEventMsg: skipping sidebar add for replayed msg from %s", shortPK(peer))
			if m.dmEvents != nil {
				return m, waitForDMEvent(m.dmEvents, m.keys)
			}
			return m, nil
		}
		newPeer = true
		m.appendDMItem(peer, m.resolveAuthor(peer))
	}
	if cm.Timestamp > m.lastDMSeen {
		m.lastDMSeen = cm.Timestamp
		if err := SaveLastDMSeen(m.cfgFlagPath, m.lastDMSeen); err != nil {
			log.Printf("dmEventMsg: failed to save last DM seen: %v", err)
		}
	}
	if m.isDMSelected() && peer == m.activeDMPeerPK() {
		m.updateViewport()
	} else if cm.Timestamp > m.dmSeenAtStart {
		m.unread[peer] = true
	}
	var batchCmds []tea.Cmd
	if profileCmd := m.maybeRequestProfile(peer); profileCmd != nil {
		batchCmds = append(batchCmds, profileCmd)
	}
	if newPeer {
		batchCmds = append(batchCmds, publishContactsListCmd(m.pool, m.relays, contactsFromModel(m.allDMPeers(), m.profiles), m.keys, m.kr))
	}
	if m.dmEvents != nil {
		batchCmds = append(batchCmds, waitForDMEvent(m.dmEvents, m.keys))
	}
	return m, tea.Batch(batchCmds...)
}

func (m *model) handleDMSubEnded(msg dmSubEndedMsg) (tea.Model, tea.Cmd) {
	log.Println("dmSubEndedMsg: DM subscription ended, scheduling reconnect")
	m.dmEvents = nil
	m.addSystemMsg("DM subscription lost, reconnecting...")
	return m, dmReconnectDelayCmd()
}

func (m *model) handleDMReconnect(msg dmReconnectMsg) (tea.Model, tea.Cmd) {
	log.Println("dmReconnectMsg: reconnecting DM subscription")
	return m, subscribeDMCmd(m.pool, m.relays, m.kr, m.lastDMSeen)
}

func (m *model) handleChannelSubEnded(msg channelSubEndedMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelSubEndedMsg: channel %s subscription ended", shortPK(msg.channelID))
	// Ignore stale messages from a previously canceled subscription.
	if _, ok := m.roomSubs[msg.channelID]; !ok {
		log.Printf("channelSubEndedMsg: ignoring stale message for %s", shortPK(msg.channelID))
		return m, nil
	}
	delete(m.roomSubs, msg.channelID)
	return m, channelReconnectDelayCmd(msg.channelID)
}

func (m *model) handleChannelReconnect(msg channelReconnectMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelReconnectMsg: reconnecting channel %s", shortPK(msg.channelID))
	// Don't resubscribe if we already have an active subscription.
	if _, ok := m.roomSubs[msg.channelID]; ok {
		log.Printf("channelReconnectMsg: already subscribed to %s, skipping", shortPK(msg.channelID))
		return m, nil
	}
	// Only reconnect if the channel is still in the sidebar.
	if m.findChannelIdx(msg.channelID) >= 0 {
		return m, subscribeChannelCmd(m.pool, m.relays, msg.channelID)
	}
	return m, nil
}

func (m *model) handleGroupSubStarted(msg groupSubStartedMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupSubStartedMsg: group=%s", msg.groupKey)
	// Cancel any existing subscription for this group (e.g. reconnect).
	m.cancelRoomSub(msg.groupKey)
	sub := &roomSub{kind: SidebarGroup, roomID: msg.groupKey, events: msg.events, cancel: msg.cancel}
	m.roomSubs[msg.groupKey] = sub
	if _, ok := m.groupRecentIDs[msg.groupKey]; !ok {
		m.groupRecentIDs[msg.groupKey] = nil
	}
	// Load log history if no messages are loaded yet.
	if len(m.msgs[msg.groupKey]) == 0 {
		m.loadHistory("group", msg.groupKey)
	}
	return m, waitForRoomSub(sub, m.keys)
}

func (m *model) handleGroupEvent(msg groupEventMsg) (tea.Model, tea.Cmd) {
	cm := ChatMessage(msg)
	log.Printf("groupEventMsg: author=%s group=%s id=%s", cm.Author, cm.GroupKey, cm.EventID)
	gk := cm.GroupKey
	sub := m.roomSubs[gk]
	if m.isSeenEvent(cm.EventID) {
		return m, waitForRoomSub(sub, m.keys)
	}
	m.markSeenEvent(cm.EventID)
	// Track recent event IDs for NIP-29 "previous" tags.
	ids := m.groupRecentIDs[gk]
	ids = append(ids, cm.EventID)
	if len(ids) > 50 {
		ids = ids[len(ids)-50:]
	}
	m.groupRecentIDs[gk] = ids
	m.msgs[gk] = appendMessage(m.msgs[gk], cm, m.cfg.MaxMessages)
	appendLogEntry(m.logDir, "group", gk, cm, m.resolveAuthor(cm.PubKey))
	if gk == m.activeGroupKey() {
		m.updateViewport()
	} else {
		m.unread[gk] = true
	}
	var batchCmds []tea.Cmd
	if profileCmd := m.maybeRequestProfile(cm.PubKey); profileCmd != nil {
		batchCmds = append(batchCmds, profileCmd)
	}
	batchCmds = append(batchCmds, waitForRoomSub(sub, m.keys))
	return m, tea.Batch(batchCmds...)
}

func (m *model) handleGroupSubEnded(msg groupSubEndedMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupSubEndedMsg: group %s subscription ended", msg.groupKey)
	// Ignore stale messages from a previously canceled subscription.
	if _, ok := m.roomSubs[msg.groupKey]; !ok {
		log.Printf("groupSubEndedMsg: ignoring stale message for %s", msg.groupKey)
		return m, nil
	}
	delete(m.roomSubs, msg.groupKey)
	return m, groupReconnectDelayCmd(msg.groupKey)
}

func (m *model) handleGroupReconnect(msg groupReconnectMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupReconnectMsg: reconnecting group %s", msg.groupKey)
	// Don't resubscribe if we already have an active subscription.
	if _, ok := m.roomSubs[msg.groupKey]; ok {
		log.Printf("groupReconnectMsg: already subscribed to %s, skipping", msg.groupKey)
		return m, nil
	}
	// Only reconnect if the group is still in the sidebar.
	relayURL, groupID := splitGroupKey(msg.groupKey)
	if m.findGroupIdx(relayURL, groupID) >= 0 {
		return m, subscribeGroupCmd(m.pool, relayURL, groupID)
	}
	return m, nil
}

func (m *model) handleGroupMeta(msg groupMetaMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupMetaMsg: relay=%s group=%s name=%q", msg.RelayURL, msg.GroupID, msg.Name)
	m.updateGroupName(msg.RelayURL, msg.GroupID, msg.Name)
	if msg.RelayPubKey != "" {
		m.updateGroupRelayPubKey(msg.RelayURL, msg.GroupID, msg.RelayPubKey)
	}
	m.updateViewport()
	var metaCmds []tea.Cmd
	metaCmds = append(metaCmds, publishSimpleGroupsListCmd(m.pool, m.relays, m.allGroups(), m.keys))
	// Only re-wait if this metadata came from the group subscription;
	// edit commands also return groupMetaMsg but must not spawn extra waiters.
	if msg.FromSub {
		gk := groupKey(msg.RelayURL, msg.GroupID)
		if sub, ok := m.roomSubs[gk]; ok {
			metaCmds = append(metaCmds, waitForRoomSub(sub, m.keys))
		}
	}
	return m, tea.Batch(metaCmds...)
}

func (m *model) handleGroupCreated(msg groupCreatedMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupCreatedMsg: relay=%s group=%s name=%q", msg.RelayURL, msg.GroupID, msg.Name)
	// Check if already in list (shouldn't happen, but be safe).
	if idx := m.findGroupIdx(msg.RelayURL, msg.GroupID); idx >= 0 {
		m.activeItem = idx
		m.updateViewport()
		g := m.sidebar[idx].(GroupItem).Group
		return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
	}
	idx := m.appendGroupItem(Group{RelayURL: msg.RelayURL, GroupID: msg.GroupID, Name: msg.Name})
	m.activeItem = idx
	m.addSystemMsg(fmt.Sprintf("created group ~%s on %s", msg.Name, msg.RelayURL))
	m.updateViewport()
	gk := groupKey(msg.RelayURL, msg.GroupID)
	return m, tea.Batch(
		subscribeGroupCmd(m.pool, msg.RelayURL, msg.GroupID),
		// Kind 9007 (create) doesn't set metadata on most relays;
		// publish a kind 9002 (edit metadata) to set the name.
		editGroupMetadataCmd(m.pool, msg.RelayURL, msg.GroupID, map[string]string{"name": msg.Name}, m.groupRecentIDs[gk], m.keys),
		// Default new groups to closed.
		editGroupMetadataCmd(m.pool, msg.RelayURL, msg.GroupID, map[string]string{"closed": ""}, m.groupRecentIDs[gk], m.keys),
		publishSimpleGroupsListCmd(m.pool, m.relays, m.allGroups(), m.keys),
	)
}

func (m *model) handleGroupInviteCreated(msg groupInviteCreatedMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupInviteCreatedMsg: relay=%s group=%s code=%s", msg.RelayURL, msg.GroupID, msg.Code)
	host := strings.TrimPrefix(msg.RelayURL, "wss://")
	m.addSystemMsg(fmt.Sprintf("invite code: %s  join with: /join %s'%s", msg.Code, host, msg.GroupID))
	return m, nil
}

func (m *model) handleGroupJoined(msg groupJoinedMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupJoinedMsg: relay=%s group=%s", msg.RelayURL, msg.GroupID)
	// Check if already in list
	if idx := m.findGroupIdx(msg.RelayURL, msg.GroupID); idx >= 0 {
		m.activeItem = idx
		m.updateViewport()
		g := m.sidebar[idx].(GroupItem).Group
		return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
	}
	name := msg.Name
	if name == "" {
		name = shortPK(msg.GroupID)
	}
	idx := m.appendGroupItem(Group{RelayURL: msg.RelayURL, GroupID: msg.GroupID, Name: name})
	m.activeItem = idx
	m.updateViewport()
	return m, tea.Batch(
		subscribeGroupCmd(m.pool, msg.RelayURL, msg.GroupID),
		fetchGroupMetaCmd(m.pool, msg.RelayURL, msg.GroupID),
		publishSimpleGroupsListCmd(m.pool, m.relays, m.allGroups(), m.keys),
	)
}

func (m *model) handleProfileResolved(msg profileResolvedMsg) (tea.Model, tea.Cmd) {
	log.Printf("profileResolvedMsg: %s -> %q", shortPK(msg.PubKey), msg.DisplayName)
	m.profiles[msg.PubKey] = msg.DisplayName
	delete(m.profilePending, msg.PubKey)
	if m.containsDMPeer(msg.PubKey) {
		m.updateDMItemName(msg.PubKey, msg.DisplayName)
		m.updateViewport()
		return m, publishContactsListCmd(m.pool, m.relays, contactsFromModel(m.allDMPeers(), m.profiles), m.keys, m.kr)
	}
	m.updateViewport()
	return m, nil
}

func (m *model) handleNIP05Resolved(msg nip05ResolvedMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.addSystemMsg(fmt.Sprintf("NIP-05 error: %v", msg.Err))
		return m, nil
	}
	m.addSystemMsg(fmt.Sprintf("resolved %s → %s", msg.Identifier, shortPK(msg.PubKey)))
	return m.openDM(msg.PubKey)
}

func (m *model) handleNostrErr(msg nostrErrMsg) (tea.Model, tea.Cmd) {
	log.Printf("nostrErrMsg: %s", msg.Error())
	m.addSystemMsg(msg.Error())
	return m, nil
}

func (m *model) handleDMSendErr(msg dmSendErrMsg) (tea.Model, tea.Cmd) {
	log.Printf("dmSendErrMsg: peer=%s err=%s", shortPK(msg.peerPK), msg.err)
	// Show the error in the DM conversation, not whatever room is active.
	errMsg := ChatMessage{
		Author:    "system",
		Content:   msg.err.Error(),
		Timestamp: nostr.Now(),
	}
	m.msgs[msg.peerPK] = appendMessage(m.msgs[msg.peerPK], errMsg, m.cfg.MaxMessages)
	if m.activeDMPeerPK() == msg.peerPK {
		m.updateViewport()
	}
	return m, nil
}

func (m *model) handleBlossomUpload(msg blossomUploadMsg) (tea.Model, tea.Cmd) {
	m.addSystemMsg(fmt.Sprintf("uploaded: %s", msg.URL))
	current := m.input.Value()
	if current != "" && !strings.HasSuffix(current, " ") {
		current += " "
	}
	m.input.SetValue(current + msg.URL)
	return m, nil
}

func (m *model) handleBlossomUploadErr(msg blossomUploadErrMsg) (tea.Model, tea.Cmd) {
	m.addSystemMsg("upload failed: " + msg.Error())
	return m, nil
}

func (m *model) handleNIP51ListsFetched(msg nip51ListsFetchedMsg) (tea.Model, tea.Cmd) {
	log.Printf("nip51ListsFetchedMsg: contacts=%d (ts=%d) channels=%d (ts=%d) groups=%d (ts=%d)",
		len(msg.contacts), msg.contactsTS, len(msg.channels), msg.channelsTS, len(msg.groups), msg.groupsTS)
	var fetchCmds []tea.Cmd

	// Contacts: if relay data is newer, replace in-memory state.
	if msg.contactsTS > m.contactsListTS && msg.contacts != nil {
		m.contactsListTS = msg.contactsTS
		m.replaceDMPeers(msg.contacts)
		for _, c := range msg.contacts {
			m.profiles[c.PubKey] = c.Name
		}
		// Fetch profiles for any new contacts.
		for _, c := range msg.contacts {
			if cmd := m.maybeRequestProfile(c.PubKey); cmd != nil {
				fetchCmds = append(fetchCmds, cmd)
			}
		}
	}

	// Channels: if relay data is newer, replace in-memory state and rewrite cache.
	if msg.channelsTS > m.channelsListTS && msg.channels != nil {
		m.channelsListTS = msg.channelsTS
		// Cancel subs for channels no longer in the list.
		newIDs := make(map[string]bool, len(msg.channels))
		for _, ch := range msg.channels {
			newIDs[ch.ID] = true
		}
		for _, ch := range m.allChannels() {
			if !newIDs[ch.ID] {
				m.cancelRoomSub(ch.ID)
			}
		}
		m.replaceChannels(msg.channels)
		// Subscribe to new channels and fetch metadata.
		for _, ch := range msg.channels {
			if _, ok := m.roomSubs[ch.ID]; !ok {
				fetchCmds = append(fetchCmds, subscribeChannelCmd(m.pool, m.relays, ch.ID))
			}
			fetchCmds = append(fetchCmds, fetchChannelMetaCmd(m.pool, m.relays, ch.ID))
		}
	}

	// Groups: if relay data is newer, replace in-memory state and rewrite cache.
	if msg.groupsTS > m.groupsListTS && msg.groups != nil {
		m.groupsListTS = msg.groupsTS
		// Cancel subs for groups no longer in the list.
		newGKs := make(map[string]bool, len(msg.groups))
		for _, sg := range msg.groups {
			newGKs[groupKey(sg.RelayURL, sg.GroupID)] = true
		}
		for _, g := range m.allGroups() {
			gk := groupKey(g.RelayURL, g.GroupID)
			if !newGKs[gk] {
				m.cancelRoomSub(gk)
			}
		}
		var groups []Group
		for _, sg := range msg.groups {
			groups = append(groups, Group{RelayURL: sg.RelayURL, GroupID: sg.GroupID, Name: sg.Name})
		}
		m.replaceGroups(groups)
		// Subscribe to new groups and fetch metadata.
		for _, sg := range msg.groups {
			gk := groupKey(sg.RelayURL, sg.GroupID)
			if _, ok := m.roomSubs[gk]; !ok {
				fetchCmds = append(fetchCmds, subscribeGroupCmd(m.pool, sg.RelayURL, sg.GroupID))
			}
			fetchCmds = append(fetchCmds, fetchGroupMetaCmd(m.pool, sg.RelayURL, sg.GroupID))
		}
	}

	// Clamp activeItem to valid range after list replacement.
	total := m.sidebarTotal()
	if total == 0 {
		m.activeItem = 0
	} else if m.activeItem >= total {
		m.activeItem = total - 1
	}
	m.updateViewport()
	if len(fetchCmds) > 0 {
		return m, tea.Batch(fetchCmds...)
	}
	return m, nil
}

func (m *model) handleProfilePublished(msg profilePublishedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		log.Printf("profilePublishedMsg: error: %v", msg.err)
	}
	return m, nil
}

func (m *model) handleDMRelaysPublished(msg dmRelaysPublishedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		log.Printf("dmRelaysPublishedMsg: error: %v", msg.err)
	}
	return m, nil
}

func (m *model) handleNIP51PublishResult(msg nip51PublishResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		log.Printf("nip51PublishResultMsg: kind %d error: %v", msg.listKind, msg.err)
	}
	return m, nil
}

func (m *model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Dismiss QR overlay on any key (except ctrl+c which still quits).
	if m.qrOverlay != "" {
		if msg.String() == "ctrl+c" {
			m.cancelAllRoomSubs()
			if m.dmCancel != nil {
				m.dmCancel()
			}
			return m, tea.Quit
		}
		m.qrOverlay = ""
		return m, nil
	}

	// Intercept bracketed paste: detect file paths for Blossom upload.
	if msg.Paste {
		text := strings.TrimSpace(string(msg.Runes))
		if isFilePath(text) {
			if len(m.cfg.BlossomServers) == 0 {
				m.addSystemMsg("blossom_servers not configured")
				return m, nil
			}
			m.addSystemMsg("uploading " + filepath.Base(text) + "...")
			return m, blossomUploadCmd(m.cfg.BlossomServers, text, m.keys)
		}
	}

	// Autocomplete key handling — intercept before textarea.
	if len(m.acSuggestions) > 0 {
		switch msg.String() {
		case "tab":
			m.acIndex = (m.acIndex + 1) % len(m.acSuggestions)
			return m, nil
		case "shift+tab":
			m.acIndex--
			if m.acIndex < 0 {
				m.acIndex = len(m.acSuggestions) - 1
			}
			return m, nil
		case "enter":
			m.acceptSuggestion()
			return m, nil
		case "esc":
			m.acSuggestions = nil
			m.acIndex = 0
			return m, nil
		}
	} else if msg.String() == "tab" {
		// Open autocomplete on first Tab press.
		m.updateSuggestions()
		if len(m.acSuggestions) > 0 {
			return m, nil
		}
	}

	// Input history navigation — only when cursor is at the
	// top (up) or bottom (down) line of the textarea.
	if msg.String() == "up" && m.input.Line() == 0 && len(m.inputHistory) > 0 {
		if m.historyIndex == -1 {
			// Entering history: save current input.
			m.historySaved = m.input.Value()
			m.historyIndex = len(m.inputHistory) - 1
		} else if m.historyIndex > 0 {
			m.historyIndex--
		}
		m.input.SetValue(m.inputHistory[m.historyIndex])
		m.syncInputHeight()
		return m, nil
	}
	if msg.String() == "down" && m.input.Line() == m.input.LineCount()-1 && m.historyIndex >= 0 {
		if m.historyIndex < len(m.inputHistory)-1 {
			m.historyIndex++
			m.input.SetValue(m.inputHistory[m.historyIndex])
		} else {
			// Past newest entry: restore saved input.
			m.historyIndex = -1
			m.input.SetValue(m.historySaved)
			m.historySaved = ""
		}
		m.syncInputHeight()
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		m.cancelAllRoomSubs()
		if m.dmCancel != nil {
			m.dmCancel()
		}
		return m, tea.Quit

	case "ctrl+up":
		total := m.sidebarTotal()
		if total > 1 {
			m.activeItem--
			if m.activeItem < 0 {
				m.activeItem = total - 1
			}
			m.clearUnread()
			m.updateViewport()
		}
		return m, nil

	case "ctrl+down":
		total := m.sidebarTotal()
		if total > 1 {
			m.activeItem++
			if m.activeItem >= total {
				m.activeItem = 0
			}
			m.clearUnread()
			m.updateViewport()
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
		m.inputHistory = append(m.inputHistory, text)
		m.historyIndex = -1
		m.historySaved = ""
		m.input.Reset()
		m.acSuggestions = nil
		m.acIndex = 0
		m.input.SetHeight(inputMinHeight)
		m.lastInputHeight = inputMinHeight
		m.updateLayout()

		// Slash commands
		if strings.HasPrefix(text, "/") {
			return m.handleCommand(text)
		}

		// Regular message
		if item := m.activeSidebarItem(); item != nil {
			switch it := item.(type) {
			case ChannelItem:
				return m, publishChannelMessage(m.pool, m.relays, it.Channel.ID, text, m.keys)
			case GroupItem:
				gk := groupKey(it.Group.RelayURL, it.Group.GroupID)
				return m, publishGroupMessage(m.pool, it.Group.RelayURL, it.Group.GroupID, text, m.groupRecentIDs[gk], m.keys)
			case DMItem:
				return m, sendDM(m.pool, m.relays, it.PubKey, text, m.keys, m.kr)
			}
		}
		return m, nil
	}

	return m.handleInputUpdate(msg)
}

func (m *model) handleInputUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

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

	// Re-filter suggestions as the user types (only when already open).
	if len(m.acSuggestions) > 0 {
		m.updateSuggestions()
	}

	// Shrink textarea when lines are removed (e.g. backspace joining lines).
	m.syncInputHeight()

	return m, tea.Batch(cmds...)
}

package main

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
	case blossomUploadMsg:
		return m.handleBlossomUpload(msg)
	case blossomUploadErrMsg:
		return m.handleBlossomUploadErr(msg)
	case nip51ListsFetchedMsg:
		return m.handleNIP51ListsFetched(msg)
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
				prev := m.activeItem
				m.activeItem = idx
				m.updateViewport()
				return m, m.subscribeIfNeeded(prev)
			}
		}
		return m, nil
	}
	return m, nil
}

func (m *model) handleChannelCreated(msg channelCreatedMsg) (tea.Model, tea.Cmd) {
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
	return m, tea.Batch(
		subscribeChannelCmd(m.pool, m.relays, msg.ID),
		publishPublicChatsListCmd(m.pool, m.relays, m.channels, m.keys),
	)
}

func (m *model) handleChannelMeta(msg channelMetaMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelMetaMsg: id=%s name=%q", msg.ID, msg.Name)
	for i, ch := range m.channels {
		if ch.ID == msg.ID {
			m.channels[i].Name = msg.Name
			break
		}
	}
	if err := UpdateRoomName(m.cfgFlagPath, msg.ID, msg.Name); err != nil {
		log.Printf("channelMetaMsg: failed to save room: %v", err)
		m.addSystemMsg("failed to save room: " + err.Error())
	}
	return m, publishPublicChatsListCmd(m.pool, m.relays, m.channels, m.keys)
}

func (m *model) handleChannelSubStarted(msg channelSubStartedMsg) (tea.Model, tea.Cmd) {
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
}

func (m *model) handleDMSubStarted(msg dmSubStartedMsg) (tea.Model, tea.Cmd) {
	log.Println("dmSubStartedMsg received")
	if m.dmCancel != nil {
		m.dmCancel()
	}
	m.dmEvents = msg.events
	m.dmCancel = msg.cancel
	m.addSystemMsg("DM subscription active")
	return m, waitForDMEvent(m.dmEvents, m.keys)
}

func (m *model) handleChannelEvent(msg channelEventMsg) (tea.Model, tea.Cmd) {
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
	} else {
		m.unread[chID] = true
	}
	var batchCmds []tea.Cmd
	if profileCmd := m.maybeRequestProfile(cm.PubKey); profileCmd != nil {
		batchCmds = append(batchCmds, profileCmd)
	}
	if m.channelEvents != nil {
		batchCmds = append(batchCmds, waitForChannelEvent(m.channelEvents, m.channelSubID, m.keys))
	}
	return m, tea.Batch(batchCmds...)
}

func (m *model) handleDMEvent(msg dmEventMsg) (tea.Model, tea.Cmd) {
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
	newPeer := false
	if !containsStr(m.dmPeers, peer) {
		newPeer = true
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
		batchCmds = append(batchCmds, publishContactsListCmd(m.pool, m.relays, contactsFromModel(m.dmPeers, m.profiles), m.keys, m.kr))
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
	log.Printf("channelSubEndedMsg: channel %s subscription ended, scheduling reconnect", shortPK(msg.channelID))
	m.channelEvents = nil
	if msg.channelID == m.activeChannelID() {
		m.addSystemMsg("channel subscription lost, reconnecting...")
	}
	return m, channelReconnectDelayCmd(msg.channelID)
}

func (m *model) handleChannelReconnect(msg channelReconnectMsg) (tea.Model, tea.Cmd) {
	log.Printf("channelReconnectMsg: reconnecting channel %s", shortPK(msg.channelID))
	if msg.channelID == m.activeChannelID() {
		return m, subscribeChannelCmd(m.pool, m.relays, msg.channelID)
	}
	return m, nil
}

func (m *model) handleGroupSubStarted(msg groupSubStartedMsg) (tea.Model, tea.Cmd) {
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
}

func (m *model) handleGroupEvent(msg groupEventMsg) (tea.Model, tea.Cmd) {
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
	} else {
		m.unread[gk] = true
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
}

func (m *model) handleGroupSubEnded(msg groupSubEndedMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupSubEndedMsg: group %s subscription ended, scheduling reconnect", msg.groupKey)
	m.groupEvents = nil
	if msg.groupKey == m.activeGroupKey() {
		m.addSystemMsg("group subscription lost, reconnecting...")
	}
	return m, groupReconnectDelayCmd(msg.groupKey)
}

func (m *model) handleGroupReconnect(msg groupReconnectMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupReconnectMsg: reconnecting group %s", msg.groupKey)
	if msg.groupKey == m.activeGroupKey() {
		if idx := m.activeGroupIdx(); idx >= 0 {
			g := m.groups[idx]
			return m, subscribeGroupCmd(m.pool, g.RelayURL, g.GroupID)
		}
	}
	return m, nil
}

func (m *model) handleGroupMeta(msg groupMetaMsg) (tea.Model, tea.Cmd) {
	log.Printf("groupMetaMsg: relay=%s group=%s name=%q", msg.RelayURL, msg.GroupID, msg.Name)
	for i, g := range m.groups {
		if g.RelayURL == msg.RelayURL && g.GroupID == msg.GroupID {
			m.groups[i].Name = msg.Name
			if msg.RelayPubKey != "" {
				m.groups[i].RelayPubKey = msg.RelayPubKey
			}
			break
		}
	}
	if err := UpdateSavedGroupName(m.cfgFlagPath, msg.RelayURL, msg.GroupID, msg.Name); err != nil {
		log.Printf("groupMetaMsg: failed to update group name: %v", err)
	}
	m.updateViewport()
	var metaCmds []tea.Cmd
	metaCmds = append(metaCmds, publishSimpleGroupsListCmd(m.pool, m.relays, m.groups, m.keys))
	// Only re-wait if this metadata came from the group subscription;
	// edit commands also return groupMetaMsg but must not spawn extra waiters.
	if msg.FromSub && m.groupEvents != nil {
		subRelayURL, _ := splitGroupKey(m.groupSubKey)
		metaCmds = append(metaCmds, waitForGroupEvent(m.groupEvents, m.groupSubKey, subRelayURL, m.keys))
	}
	return m, tea.Batch(metaCmds...)
}

func (m *model) handleGroupCreated(msg groupCreatedMsg) (tea.Model, tea.Cmd) {
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
		// Default new groups to closed.
		editGroupMetadataCmd(m.pool, msg.RelayURL, msg.GroupID, map[string]string{"closed": ""}, m.groupRecentIDs[gk], m.keys),
		publishSimpleGroupsListCmd(m.pool, m.relays, m.groups, m.keys),
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
		publishSimpleGroupsListCmd(m.pool, m.relays, m.groups, m.keys),
	)
}

func (m *model) handleProfileResolved(msg profileResolvedMsg) (tea.Model, tea.Cmd) {
	log.Printf("profileResolvedMsg: %s -> %q", shortPK(msg.PubKey), msg.DisplayName)
	m.profiles[msg.PubKey] = msg.DisplayName
	delete(m.profilePending, msg.PubKey)
	if containsStr(m.dmPeers, msg.PubKey) {
		if err := UpdateContactName(m.cfgFlagPath, msg.PubKey, msg.DisplayName); err != nil {
			log.Printf("profileResolvedMsg: failed to update contact name: %v", err)
		}
		m.updateViewport()
		return m, publishContactsListCmd(m.pool, m.relays, contactsFromModel(m.dmPeers, m.profiles), m.keys, m.kr)
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

	// Contacts: if relay data is newer, replace in-memory state and rewrite cache.
	if msg.contactsTS > m.contactsListTS && msg.contacts != nil {
		m.contactsListTS = msg.contactsTS
		m.dmPeers = nil
		for _, c := range msg.contacts {
			if !containsStr(m.dmPeers, c.PubKey) {
				m.dmPeers = append(m.dmPeers, c.PubKey)
			}
			m.profiles[c.PubKey] = c.Name
		}
		if err := WriteContacts(m.cfgFlagPath, msg.contacts); err != nil {
			log.Printf("nip51ListsFetchedMsg: write contacts cache: %v", err)
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
		m.channels = msg.channels
		var rooms []Room
		for _, ch := range msg.channels {
			rooms = append(rooms, Room{Name: ch.Name, ID: ch.ID})
		}
		if err := WriteRooms(m.cfgFlagPath, rooms); err != nil {
			log.Printf("nip51ListsFetchedMsg: write rooms cache: %v", err)
		}
		// Fetch metadata for channels with placeholder names.
		for _, ch := range msg.channels {
			fetchCmds = append(fetchCmds, fetchChannelMetaCmd(m.pool, m.relays, ch.ID))
		}
	}

	// Groups: if relay data is newer, replace in-memory state and rewrite cache.
	if msg.groupsTS > m.groupsListTS && msg.groups != nil {
		m.groupsListTS = msg.groupsTS
		m.groups = nil
		var savedGroups []SavedGroup
		for _, sg := range msg.groups {
			m.groups = append(m.groups, Group{RelayURL: sg.RelayURL, GroupID: sg.GroupID, Name: sg.Name})
			savedGroups = append(savedGroups, sg)
		}
		if err := WriteSavedGroups(m.cfgFlagPath, savedGroups); err != nil {
			log.Printf("nip51ListsFetchedMsg: write groups cache: %v", err)
		}
		// Fetch metadata for groups.
		for _, sg := range msg.groups {
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

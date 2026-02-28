package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

func (m *model) handleCommand(text string) (tea.Model, tea.Cmd) {
	parts := strings.SplitN(text, " ", 2)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "/channel":
		return m.handleChannelCommand(arg)

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
			m.addSystemMsg("usage: /dm <npub, hex pubkey, or NIP-05 address>")
			return m, nil
		}
		return m.openDM(arg)

	case "/me":
		m.qrOverlay = renderQR("Your npub:", "nostr:"+m.keys.NPub)
		return m, nil

	case "/room":
		if item := m.activeSidebarItem(); item != nil {
			switch it := item.(type) {
			case ChannelItem:
				id, err := nostr.IDFromHex(it.Channel.ID)
				if err != nil {
					m.addSystemMsg(fmt.Sprintf("invalid channel ID: %v", err))
					return m, nil
				}
				nevent := nip19.EncodeNevent(id, m.relays, nostr.PubKey{})
				m.qrOverlay = renderQR("#"+it.Channel.Name, "nostr:"+nevent)
				return m, nil
			case GroupItem:
				naddr, err := m.groupNaddr(it.Group)
				if err != nil {
					m.addSystemMsg(fmt.Sprintf("encode error: %v", err))
					return m, nil
				}
				m.qrOverlay = renderQR("~"+it.Group.Name, "nostr:"+naddr)
				return m, nil
			}
		}
		m.addSystemMsg("no active channel or group — switch to one first")
		return m, nil

	case "/delete":
		if !m.isGroupSelected() {
			m.addSystemMsg("/delete only works in a NIP-29 group")
			return m, nil
		}
		gi, ok := m.activeSidebarItem().(GroupItem)
		if !ok {
			m.addSystemMsg("unexpected sidebar item type")
			return m, nil
		}
		g := gi.Group
		gk := groupKey(g.RelayURL, g.GroupID)
		if arg != "" {
			// Delete by explicit event ID (admin use).
			// Remove from local messages.
			msgs := m.msgs[gk]
			for i, cm := range msgs {
				if cm.EventID == arg {
					m.msgs[gk] = append(msgs[:i], msgs[i+1:]...)
					break
				}
			}
			m.updateViewport()
			return m, deleteGroupEventCmd(m.pool, g.RelayURL, g.GroupID, arg, m.groupRecentIDs[gk], m.keys)
		}
		// No arg: delete the last own message.
		msgs := m.msgs[gk]
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
		m.msgs[gk] = append(msgs[:targetIdx], msgs[targetIdx+1:]...)
		m.updateViewport()
		return m, deleteGroupEventCmd(m.pool, g.RelayURL, g.GroupID, targetID, m.groupRecentIDs[gk], m.keys)

	case "/group":
		return m.handleGroupCommand(arg)

	case "/invite":
		if !m.isGroupSelected() {
			m.addSystemMsg("/invite requires a group to be selected")
			return m, nil
		}
		if arg == "" {
			m.addSystemMsg("usage: /invite <contact-name or npub or hex>")
			return m, nil
		}
		return m.inviteToGroup(arg)

	case "/leave":
		return m.leaveCurrentItem()

	case "/help":
		m.addSystemMsg("/channel create #name — create a NIP-28 channel")
		m.addSystemMsg("/join #name — join a channel from your rooms file")
		m.addSystemMsg("/join <event-id> — join a channel by ID")
		m.addSystemMsg("/join naddr1... [code] — join a NIP-29 group (with optional invite code)")
		m.addSystemMsg("/join host'groupid [code] — join a NIP-29 group")
		m.addSystemMsg("/dm <npub|user@domain> — open a DM conversation")
		m.addSystemMsg("/group create <name> <relay> — create a closed NIP-29 group")
		m.addSystemMsg("/group set open|closed — set group open or closed")
		m.addSystemMsg("/group user add <pubkey> — add a user to the group")
		m.addSystemMsg("/group name <new-name> — edit group name")
		m.addSystemMsg("/group about <text> — edit group description")
		m.addSystemMsg("/group picture <url> — edit group picture")
		m.addSystemMsg("/invite <name> — add a contact to the group and DM them the link")
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

// handleChannelCommand handles /channel subcommands.
func (m *model) handleChannelCommand(arg string) (tea.Model, tea.Cmd) {
	if arg == "" {
		m.addSystemMsg("usage: /channel create #name")
		return m, nil
	}

	parts := strings.SplitN(arg, " ", 2)
	sub := strings.ToLower(parts[0])
	subArg := ""
	if len(parts) > 1 {
		subArg = strings.TrimSpace(parts[1])
	}

	switch sub {
	case "create":
		if subArg == "" || !strings.HasPrefix(subArg, "#") {
			m.addSystemMsg("usage: /channel create #name")
			return m, nil
		}
		name := strings.TrimPrefix(subArg, "#")
		log.Printf("handleCommand: /channel create #%s", name)
		return m, createChannelCmd(m.pool, m.relays, name, m.keys)
	default:
		m.addSystemMsg("unknown subcommand: /channel " + sub)
		return m, nil
	}
}

func (m *model) handleGroupCommand(arg string) (tea.Model, tea.Cmd) {
	if arg == "" {
		m.addSystemMsg("usage: /group create <name> <relay> | set open|closed | user add <pubkey> | name <new-name> | about <text> | picture <url>")
		return m, nil
	}

	parts := strings.SplitN(arg, " ", 2)
	sub := strings.ToLower(parts[0])
	subArg := ""
	if len(parts) > 1 {
		subArg = strings.TrimSpace(parts[1])
	}

	switch sub {
	case "create":
		// /group create <name> [wss://relay]
		createParts := strings.Fields(subArg)
		if len(createParts) == 0 {
			m.addSystemMsg("usage: /group create <name> [wss://relay]")
			return m, nil
		}
		name := createParts[0]
		relayURL := m.cfg.GroupRelay
		if len(createParts) >= 2 && (strings.HasPrefix(createParts[1], "wss://") || strings.HasPrefix(createParts[1], "ws://")) {
			relayURL = createParts[1]
		}
		if relayURL == "" {
			m.addSystemMsg("no relay specified and group_relay not set in config")
			return m, nil
		}
		return m, createGroupCmd(m.pool, relayURL, name, m.keys)

	case "set":
		// /group set open | /group set closed
		if !m.isGroupSelected() {
			m.addSystemMsg("/group set requires a group to be selected")
			return m, nil
		}
		gi := m.activeSidebarItem().(GroupItem)
		g := gi.Group
		gk := groupKey(g.RelayURL, g.GroupID)
		switch strings.ToLower(subArg) {
		case "open":
			return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"open": ""}, m.groupRecentIDs[gk], m.keys)
		case "closed":
			return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"closed": ""}, m.groupRecentIDs[gk], m.keys)
		default:
			m.addSystemMsg("usage: /group set open|closed")
			return m, nil
		}

	case "user":
		// /group user add <pubkey>
		if !m.isGroupSelected() {
			m.addSystemMsg("/group user requires a group to be selected")
			return m, nil
		}
		userParts := strings.SplitN(subArg, " ", 2)
		if len(userParts) < 2 || strings.ToLower(userParts[0]) != "add" {
			m.addSystemMsg("usage: /group user add <npub-or-hex>")
			return m, nil
		}
		pk := strings.TrimSpace(userParts[1])
		if strings.HasPrefix(pk, "npub") {
			prefix, decoded, err := nip19.Decode(pk)
			if err != nil || prefix != "npub" {
				m.addSystemMsg("invalid npub")
				return m, nil
			}
			pk = decoded.(nostr.PubKey).Hex()
		}
		gi := m.activeSidebarItem().(GroupItem)
		g := gi.Group
		gk := groupKey(g.RelayURL, g.GroupID)
		m.addSystemMsg(fmt.Sprintf("adding user %s to ~%s", shortPK(pk), g.Name))
		return m, putUserCmd(m.pool, g.RelayURL, g.GroupID, pk, m.groupRecentIDs[gk], m.keys)

	case "name":
		// /group name <new-name>
		if !m.isGroupSelected() {
			m.addSystemMsg("/group name requires a group to be selected")
			return m, nil
		}
		if subArg == "" {
			m.addSystemMsg("usage: /group name <new-name>")
			return m, nil
		}
		gi := m.activeSidebarItem().(GroupItem)
		g := gi.Group
		gk := groupKey(g.RelayURL, g.GroupID)
		m.updateGroupName(g.RelayURL, g.GroupID, subArg)
		m.updateViewport()
		return m, tea.Batch(
			editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"name": subArg}, m.groupRecentIDs[gk], m.keys),
			publishSimpleGroupsListCmd(m.pool, m.relays, m.allGroups(), m.keys),
		)

	case "about":
		// /group about <text>
		if !m.isGroupSelected() {
			m.addSystemMsg("/group about requires a group to be selected")
			return m, nil
		}
		if subArg == "" {
			m.addSystemMsg("usage: /group about <description>")
			return m, nil
		}
		gi := m.activeSidebarItem().(GroupItem)
		g := gi.Group
		gk := groupKey(g.RelayURL, g.GroupID)
		return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"about": subArg}, m.groupRecentIDs[gk], m.keys)

	case "picture":
		// /group picture <url>
		if !m.isGroupSelected() {
			m.addSystemMsg("/group picture requires a group to be selected")
			return m, nil
		}
		if subArg == "" {
			m.addSystemMsg("usage: /group picture <url>")
			return m, nil
		}
		gi := m.activeSidebarItem().(GroupItem)
		g := gi.Group
		gk := groupKey(g.RelayURL, g.GroupID)
		return m, editGroupMetadataCmd(m.pool, g.RelayURL, g.GroupID, map[string]string{"picture": subArg}, m.groupRecentIDs[gk], m.keys)

	default:
		m.addSystemMsg("unknown group subcommand: " + sub)
		m.addSystemMsg("usage: /group create|set|user|name|about|picture")
		return m, nil
	}
}

// inviteToGroup resolves a contact name, npub, or hex pubkey, adds them to
// the current group via kind 9000, and sends a DM with the group naddr.
func (m *model) inviteToGroup(input string) (tea.Model, tea.Cmd) {
	gi := m.activeSidebarItem().(GroupItem)
	g := gi.Group
	gk := groupKey(g.RelayURL, g.GroupID)

	// Resolve input to a pubkey: try contact name first, then npub, then raw hex.
	pk := ""
	if strings.HasPrefix(input, "npub") {
		prefix, decoded, err := nip19.Decode(input)
		if err != nil || prefix != "npub" {
			m.addSystemMsg("invalid npub")
			return m, nil
		}
		pk = decoded.(nostr.PubKey).Hex()
	} else if len(input) == 64 {
		if _, err := hex.DecodeString(input); err != nil {
			m.addSystemMsg("invalid hex pubkey")
			return m, nil
		}
		pk = input
	} else {
		// Look up by display name in profiles (case-insensitive).
		var matches []string
		for pubkey, name := range m.profiles {
			if strings.EqualFold(name, input) {
				matches = append(matches, pubkey)
			}
		}
		switch len(matches) {
		case 0:
			m.addSystemMsg(fmt.Sprintf("unknown contact: %s (use npub or hex pubkey)", input))
			return m, nil
		case 1:
			pk = matches[0]
		default:
			m.addSystemMsg(fmt.Sprintf("ambiguous name %q matches %d contacts — use npub or hex pubkey instead", input, len(matches)))
			return m, nil
		}
	}

	naddr, err := m.groupNaddr(g)
	if err != nil {
		m.addSystemMsg(fmt.Sprintf("invite: failed to encode naddr: %v", err))
		return m, nil
	}

	displayName := m.resolveAuthor(pk)
	m.addSystemMsg(fmt.Sprintf("inviting %s to ~%s", displayName, g.Name))

	return m, tea.Batch(
		putUserCmd(m.pool, g.RelayURL, g.GroupID, pk, m.groupRecentIDs[gk], m.keys),
		inviteDMCmd(m.pool, m.relays, g.Name, naddr, pk, m.keys, m.kr),
	)
}

// joinChannel handles /join. #name looks up the rooms file, a raw hex ID
// joins directly and appends to the rooms file.
func (m *model) joinChannel(arg string) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(arg, "#") {
		// Lookup by name
		name := strings.TrimPrefix(arg, "#")
		for i, it := range m.sidebar {
			if ci, ok := it.(ChannelItem); ok && strings.EqualFold(ci.Channel.Name, name) {
				log.Printf("joinChannel: found %q -> %s", name, ci.Channel.ID)
				m.activeItem = i
				m.updateViewport()
				return m, subscribeChannelCmd(m.pool, m.relays, ci.Channel.ID)
			}
		}
		m.addSystemMsg("unknown room: " + name + " (add it to your rooms file)")
		return m, nil
	}

	// Raw hex event ID — check if already known
	id := arg
	if idx := m.findChannelIdx(id); idx >= 0 {
		ci := m.sidebar[idx].(ChannelItem)
		log.Printf("joinChannel: already have %s as %q", id, ci.Channel.Name)
		m.activeItem = idx
		m.updateViewport()
		return m, subscribeChannelCmd(m.pool, m.relays, ci.Channel.ID)
	}

	// New room — add with placeholder, fetch metadata to get the real name
	placeholder := id
	if len(placeholder) > 8 {
		placeholder = placeholder[:8]
	}
	idx := m.appendChannelItem(Channel{Name: placeholder, ID: id})
	m.activeItem = idx
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
	if idx := m.findGroupIdx(relayURL, groupID); idx >= 0 {
		m.activeItem = idx
		m.updateViewport()
		gi := m.sidebar[idx].(GroupItem)
		return m, subscribeGroupCmd(m.pool, gi.Group.RelayURL, gi.Group.GroupID)
	}

	// New group — send join request, then handle groupJoinedMsg
	return m, tea.Batch(
		joinGroupCmd(m.pool, relayURL, groupID, m.groupRecentIDs[gk], inviteCode, m.keys),
		fetchGroupMetaCmd(m.pool, relayURL, groupID),
	)
}

// openDM switches to a DM conversation, adding the peer if new.
func (m *model) openDM(input string) (tea.Model, tea.Cmd) {
	// NIP-05 identifier: resolve asynchronously.
	if strings.Contains(input, "@") {
		m.addSystemMsg(fmt.Sprintf("resolving %s …", input))
		return m, resolveNIP05Cmd(input)
	}

	pk := input
	if strings.HasPrefix(input, "npub") {
		prefix, decoded, err := nip19.Decode(input)
		if err != nil || prefix != "npub" {
			m.addSystemMsg("invalid npub")
			return m, nil
		}
		pk = decoded.(nostr.PubKey).Hex()
	}

	newPeer := false
	if !m.containsDMPeer(pk) {
		newPeer = true
		m.appendDMItem(pk, m.resolveAuthor(pk))
	}

	if idx := m.findDMPeerIdx(pk); idx >= 0 {
		m.activeItem = idx
	}
	// Load log history if no messages are loaded yet.
	if len(m.msgs[pk]) == 0 {
		m.loadHistory("dm", pk)
	}
	m.updateViewport()
	if newPeer {
		return m, publishContactsListCmd(m.pool, m.relays, contactsFromModel(m.allDMPeers(), m.profiles), m.keys, m.kr)
	}
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

	item := m.activeSidebarItem()
	if item == nil {
		m.addSystemMsg("nothing to leave")
		return m, nil
	}

	var leaveCmds []tea.Cmd

	switch it := item.(type) {
	case ChannelItem:
		ch := it.Channel

		// Cancel subscription for this channel.
		m.cancelRoomSub(ch.ID)

		// Remove from sidebar and message history.
		m.removeSidebarItem(m.activeItem)
		delete(m.msgs, ch.ID)

		leaveCmds = append(leaveCmds, publishPublicChatsListCmd(m.pool, m.relays, m.allChannels(), m.keys))
		log.Printf("leaveCurrentItem: left channel #%s", ch.Name)

	case GroupItem:
		g := it.Group
		gk := groupKey(g.RelayURL, g.GroupID)

		// Cancel subscription for this group.
		m.cancelRoomSub(gk)

		// Remove from sidebar and message history.
		m.removeSidebarItem(m.activeItem)
		delete(m.msgs, gk)

		// Send leave request.
		leaveCmds = append(leaveCmds, leaveGroupCmd(m.pool, g.RelayURL, g.GroupID, m.groupRecentIDs[gk], m.keys))

		// Clean up recent IDs tracking.
		delete(m.groupRecentIDs, gk)

		leaveCmds = append(leaveCmds, publishSimpleGroupsListCmd(m.pool, m.relays, m.allGroups(), m.keys))
		log.Printf("leaveCurrentItem: left group ~%s", g.Name)

	case DMItem:
		peer := it.PubKey

		// Remove from sidebar and message history.
		m.removeSidebarItem(m.activeItem)
		delete(m.msgs, peer)

		leaveCmds = append(leaveCmds, publishContactsListCmd(m.pool, m.relays, contactsFromModel(m.allDMPeers(), m.profiles), m.keys, m.kr))
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
	if newItem := m.activeSidebarItem(); newItem != nil {
		switch it := newItem.(type) {
		case ChannelItem:
			leaveCmds = append(leaveCmds, subscribeChannelCmd(m.pool, m.relays, it.Channel.ID))
		case GroupItem:
			leaveCmds = append(leaveCmds, subscribeGroupCmd(m.pool, it.Group.RelayURL, it.Group.GroupID))
		}
	}

	if len(leaveCmds) > 0 {
		return m, tea.Batch(leaveCmds...)
	}
	return m, nil
}

// groupNaddr encodes a NIP-19 naddr for a group, using the relay's pubkey if known.
func (m *model) groupNaddr(g Group) (string, error) {
	author := g.RelayPubKey
	if author == "" {
		author = m.keys.PK.Hex()
	}
	pk, err := nostr.PubKeyFromHex(author)
	if err != nil {
		return "", fmt.Errorf("invalid pubkey: %w", err)
	}
	return nip19.EncodeNaddr(pk, nostr.KindSimpleGroupMetadata, g.GroupID, []string{g.RelayURL}), nil
}

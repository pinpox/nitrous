package main

// SidebarKind identifies the type of a sidebar item.
type SidebarKind int

const (
	SidebarChannel SidebarKind = iota
	SidebarGroup
	SidebarDM
)

// SidebarItem is the unified interface for sidebar entries (channels, groups, DMs).
type SidebarItem interface {
	Kind() SidebarKind
	ItemID() string      // map key for msgs/unread/subscriptions
	DisplayName() string // human-readable name
	Prefix() string      // "#", "~", "@"
}

// ChannelItem wraps a Channel for the sidebar.
type ChannelItem struct {
	Channel Channel
}

func (c ChannelItem) Kind() SidebarKind  { return SidebarChannel }
func (c ChannelItem) ItemID() string     { return c.Channel.ID }
func (c ChannelItem) DisplayName() string { return c.Channel.Name }
func (c ChannelItem) Prefix() string     { return "#" }

// GroupItem wraps a Group for the sidebar.
type GroupItem struct {
	Group Group
}

func (g GroupItem) Kind() SidebarKind  { return SidebarGroup }
func (g GroupItem) ItemID() string     { return groupKey(g.Group.RelayURL, g.Group.GroupID) }
func (g GroupItem) DisplayName() string { return g.Group.Name }
func (g GroupItem) Prefix() string     { return "~" }

// DMItem wraps a DM peer for the sidebar.
type DMItem struct {
	PubKey string
	Name   string // resolved display name
}

func (d DMItem) Kind() SidebarKind  { return SidebarDM }
func (d DMItem) ItemID() string     { return d.PubKey }
func (d DMItem) DisplayName() string { return d.Name }
func (d DMItem) Prefix() string     { return "@" }

// --- Section counts ---

// channelCount returns the number of channel items in the sidebar.
func (m *model) channelCount() int {
	n := 0
	for _, it := range m.sidebar {
		if it.Kind() == SidebarChannel {
			n++
		}
	}
	return n
}

// groupCount returns the number of group items in the sidebar.
func (m *model) groupCount() int {
	n := 0
	for _, it := range m.sidebar {
		if it.Kind() == SidebarGroup {
			n++
		}
	}
	return n
}

// dmCount returns the number of DM items in the sidebar.
func (m *model) dmCount() int {
	n := 0
	for _, it := range m.sidebar {
		if it.Kind() == SidebarDM {
			n++
		}
	}
	return n
}

// --- Section boundaries (for insertion) ---

// channelEndIdx returns the index of the first non-channel item (= channelCount).
func (m *model) channelEndIdx() int {
	for i, it := range m.sidebar {
		if it.Kind() != SidebarChannel {
			return i
		}
	}
	return len(m.sidebar)
}

// groupEndIdx returns the index of the first item that is neither channel nor group.
func (m *model) groupEndIdx() int {
	for i, it := range m.sidebar {
		if it.Kind() != SidebarChannel && it.Kind() != SidebarGroup {
			return i
		}
	}
	return len(m.sidebar)
}

// --- Insert at end of section ---

// appendChannelItem inserts a ChannelItem at the end of the channel section.
// Returns the sidebar index where it was inserted.
func (m *model) appendChannelItem(ch Channel) int {
	idx := m.channelEndIdx()
	m.sidebar = append(m.sidebar, nil)
	copy(m.sidebar[idx+1:], m.sidebar[idx:])
	m.sidebar[idx] = ChannelItem{Channel: ch}
	return idx
}

// appendGroupItem inserts a GroupItem at the end of the group section.
// Returns the sidebar index where it was inserted.
func (m *model) appendGroupItem(g Group) int {
	idx := m.groupEndIdx()
	m.sidebar = append(m.sidebar, nil)
	copy(m.sidebar[idx+1:], m.sidebar[idx:])
	m.sidebar[idx] = GroupItem{Group: g}
	return idx
}

// appendDMItem appends a DMItem at the end of the sidebar.
// Returns the sidebar index where it was inserted.
func (m *model) appendDMItem(pubkey, name string) int {
	m.sidebar = append(m.sidebar, DMItem{PubKey: pubkey, Name: name})
	return len(m.sidebar) - 1
}

// --- Find by key ---

// findChannelIdx finds a channel by ID and returns its sidebar index, or -1.
func (m *model) findChannelIdx(id string) int {
	for i, it := range m.sidebar {
		if ci, ok := it.(ChannelItem); ok && ci.Channel.ID == id {
			return i
		}
	}
	return -1
}

// findGroupIdx finds a group by relay URL and group ID. Returns sidebar index or -1.
func (m *model) findGroupIdx(relayURL, groupID string) int {
	for i, it := range m.sidebar {
		if gi, ok := it.(GroupItem); ok && gi.Group.RelayURL == relayURL && gi.Group.GroupID == groupID {
			return i
		}
	}
	return -1
}

// findDMPeerIdx finds a DM peer by pubkey. Returns sidebar index or -1.
func (m *model) findDMPeerIdx(pubkey string) int {
	for i, it := range m.sidebar {
		if di, ok := it.(DMItem); ok && di.PubKey == pubkey {
			return i
		}
	}
	return -1
}

// --- Remove ---

// removeSidebarItem removes the item at index i from the sidebar.
func (m *model) removeSidebarItem(i int) {
	if i < 0 || i >= len(m.sidebar) {
		return
	}
	m.sidebar = append(m.sidebar[:i], m.sidebar[i+1:]...)
}

// --- Extract sublists ---

// allChannels collects all Channel values from the sidebar.
func (m *model) allChannels() []Channel {
	var out []Channel
	for _, it := range m.sidebar {
		if ci, ok := it.(ChannelItem); ok {
			out = append(out, ci.Channel)
		}
	}
	return out
}

// allGroups collects all Group values from the sidebar.
func (m *model) allGroups() []Group {
	var out []Group
	for _, it := range m.sidebar {
		if gi, ok := it.(GroupItem); ok {
			out = append(out, gi.Group)
		}
	}
	return out
}

// allDMPeers collects all DM peer pubkeys from the sidebar.
func (m *model) allDMPeers() []string {
	var out []string
	for _, it := range m.sidebar {
		if di, ok := it.(DMItem); ok {
			out = append(out, di.PubKey)
		}
	}
	return out
}

// --- Convenience ---

// containsDMPeer returns true if the sidebar contains a DM peer with the given pubkey.
func (m *model) containsDMPeer(pubkey string) bool {
	return m.findDMPeerIdx(pubkey) >= 0
}

// updateDMItemName updates the display name of a DM peer in the sidebar.
func (m *model) updateDMItemName(pubkey, name string) {
	for i, it := range m.sidebar {
		if di, ok := it.(DMItem); ok && di.PubKey == pubkey {
			di.Name = name
			m.sidebar[i] = di
			return
		}
	}
}

// --- Bulk replace (for NIP-51 sync) ---

// replaceChannels replaces all channel items in the sidebar with the given channels.
func (m *model) replaceChannels(channels []Channel) {
	// Remove existing channels (iterate backwards to keep indices stable).
	for i := len(m.sidebar) - 1; i >= 0; i-- {
		if m.sidebar[i].Kind() == SidebarChannel {
			m.sidebar = append(m.sidebar[:i], m.sidebar[i+1:]...)
		}
	}
	// Insert new channels at the beginning (channels come first).
	items := make([]SidebarItem, len(channels))
	for i, ch := range channels {
		items[i] = ChannelItem{Channel: ch}
	}
	m.sidebar = append(items, m.sidebar...)
}

// replaceGroups replaces all group items in the sidebar with the given groups.
func (m *model) replaceGroups(groups []Group) {
	// Remove existing groups (iterate backwards).
	for i := len(m.sidebar) - 1; i >= 0; i-- {
		if m.sidebar[i].Kind() == SidebarGroup {
			m.sidebar = append(m.sidebar[:i], m.sidebar[i+1:]...)
		}
	}
	// Insert new groups after channels.
	insertAt := m.channelEndIdx()
	items := make([]SidebarItem, len(groups))
	for i, g := range groups {
		items[i] = GroupItem{Group: g}
	}
	m.sidebar = append(m.sidebar[:insertAt], append(items, m.sidebar[insertAt:]...)...)
}

// replaceDMPeers replaces all DM items in the sidebar with the given contacts.
func (m *model) replaceDMPeers(contacts []Contact) {
	// Remove existing DM items (iterate backwards).
	for i := len(m.sidebar) - 1; i >= 0; i-- {
		if m.sidebar[i].Kind() == SidebarDM {
			m.sidebar = append(m.sidebar[:i], m.sidebar[i+1:]...)
		}
	}
	// Append new DMs at the end.
	for _, c := range contacts {
		m.sidebar = append(m.sidebar, DMItem{PubKey: c.PubKey, Name: c.Name})
	}
}

// updateChannelName updates the name of a channel in the sidebar by ID.
func (m *model) updateChannelName(id, name string) {
	for i, it := range m.sidebar {
		if ci, ok := it.(ChannelItem); ok && ci.Channel.ID == id {
			ci.Channel.Name = name
			m.sidebar[i] = ci
			return
		}
	}
}

// updateGroupName updates the name of a group in the sidebar.
func (m *model) updateGroupName(relayURL, groupID, name string) {
	for i, it := range m.sidebar {
		if gi, ok := it.(GroupItem); ok && gi.Group.RelayURL == relayURL && gi.Group.GroupID == groupID {
			gi.Group.Name = name
			m.sidebar[i] = gi
			return
		}
	}
}

// updateGroupRelayPubKey updates the relay pubkey of a group in the sidebar.
func (m *model) updateGroupRelayPubKey(relayURL, groupID, relayPubKey string) {
	for i, it := range m.sidebar {
		if gi, ok := it.(GroupItem); ok && gi.Group.RelayURL == relayURL && gi.Group.GroupID == groupID {
			gi.Group.RelayPubKey = relayPubKey
			m.sidebar[i] = gi
			return
		}
	}
}

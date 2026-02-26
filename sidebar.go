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

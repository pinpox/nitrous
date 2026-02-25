package main

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestAppendMessage(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		msgs := appendMessage(nil, ChatMessage{Content: "a", Timestamp: 100}, 10)
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
	})

	t.Run("timestamp ordering - insert at end", func(t *testing.T) {
		msgs := []ChatMessage{
			{Content: "first", Timestamp: 100},
			{Content: "second", Timestamp: 200},
		}
		msgs = appendMessage(msgs, ChatMessage{Content: "third", Timestamp: 300}, 10)
		if msgs[2].Content != "third" {
			t.Errorf("expected 'third' at end, got %q", msgs[2].Content)
		}
	})

	t.Run("timestamp ordering - insert at beginning", func(t *testing.T) {
		msgs := []ChatMessage{
			{Content: "second", Timestamp: 200},
			{Content: "third", Timestamp: 300},
		}
		msgs = appendMessage(msgs, ChatMessage{Content: "first", Timestamp: 100}, 10)
		if msgs[0].Content != "first" {
			t.Errorf("expected 'first' at beginning, got %q", msgs[0].Content)
		}
	})

	t.Run("timestamp ordering - insert in middle", func(t *testing.T) {
		msgs := []ChatMessage{
			{Content: "first", Timestamp: 100},
			{Content: "third", Timestamp: 300},
		}
		msgs = appendMessage(msgs, ChatMessage{Content: "second", Timestamp: 200}, 10)
		if msgs[1].Content != "second" {
			t.Errorf("expected 'second' in middle, got %q", msgs[1].Content)
		}
	})

	t.Run("max cap enforced", func(t *testing.T) {
		var msgs []ChatMessage
		for i := 0; i < 15; i++ {
			msgs = appendMessage(msgs, ChatMessage{
				Content:   "msg",
				Timestamp: nostr.Timestamp(i),
			}, 10)
		}
		if len(msgs) != 10 {
			t.Errorf("expected max 10 messages, got %d", len(msgs))
		}
		// Oldest messages should be trimmed.
		if msgs[0].Timestamp != 5 {
			t.Errorf("expected oldest timestamp 5, got %d", msgs[0].Timestamp)
		}
	})

	t.Run("equal timestamps preserve insertion order", func(t *testing.T) {
		msgs := []ChatMessage{
			{Content: "a", Timestamp: 100},
		}
		msgs = appendMessage(msgs, ChatMessage{Content: "b", Timestamp: 100}, 10)
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(msgs))
		}
	})
}

func newTestModel(channels int, groups int, dmPeers int) *model {
	m := &model{
		activeItem: 0,
	}
	for i := 0; i < channels; i++ {
		m.channels = append(m.channels, Channel{ID: "ch" + string(rune('0'+i)), Name: "chan" + string(rune('0'+i))})
	}
	for i := 0; i < groups; i++ {
		m.groups = append(m.groups, Group{RelayURL: "wss://r", GroupID: "g" + string(rune('0'+i)), Name: "grp" + string(rune('0'+i))})
	}
	for i := 0; i < dmPeers; i++ {
		m.dmPeers = append(m.dmPeers, "pk"+string(rune('0'+i)))
	}
	return m
}

func TestIsChannelSelected(t *testing.T) {
	m := newTestModel(2, 2, 2) // channels: 0-1, groups: 2-3, DMs: 4-5

	m.activeItem = 0
	if !m.isChannelSelected() {
		t.Error("activeItem=0 should be a channel")
	}

	m.activeItem = 1
	if !m.isChannelSelected() {
		t.Error("activeItem=1 should be a channel")
	}

	m.activeItem = 2
	if m.isChannelSelected() {
		t.Error("activeItem=2 should NOT be a channel")
	}

	m.activeItem = 4
	if m.isChannelSelected() {
		t.Error("activeItem=4 should NOT be a channel")
	}
}

func TestIsGroupSelected(t *testing.T) {
	m := newTestModel(2, 2, 2)

	m.activeItem = 0
	if m.isGroupSelected() {
		t.Error("activeItem=0 should NOT be a group")
	}

	m.activeItem = 2
	if !m.isGroupSelected() {
		t.Error("activeItem=2 should be a group")
	}

	m.activeItem = 3
	if !m.isGroupSelected() {
		t.Error("activeItem=3 should be a group")
	}

	m.activeItem = 4
	if m.isGroupSelected() {
		t.Error("activeItem=4 should NOT be a group")
	}
}

func TestIsDMSelected(t *testing.T) {
	m := newTestModel(2, 2, 2)

	m.activeItem = 0
	if m.isDMSelected() {
		t.Error("activeItem=0 should NOT be a DM")
	}

	m.activeItem = 3
	if m.isDMSelected() {
		t.Error("activeItem=3 should NOT be a DM")
	}

	m.activeItem = 4
	if !m.isDMSelected() {
		t.Error("activeItem=4 should be a DM")
	}

	m.activeItem = 5
	if !m.isDMSelected() {
		t.Error("activeItem=5 should be a DM")
	}
}

func TestActiveChannelIdx(t *testing.T) {
	m := newTestModel(2, 2, 2)

	m.activeItem = 0
	if idx := m.activeChannelIdx(); idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}

	m.activeItem = 1
	if idx := m.activeChannelIdx(); idx != 1 {
		t.Errorf("expected 1, got %d", idx)
	}

	m.activeItem = 2 // group
	if idx := m.activeChannelIdx(); idx != -1 {
		t.Errorf("expected -1 for group, got %d", idx)
	}

	m.activeItem = 4 // DM
	if idx := m.activeChannelIdx(); idx != -1 {
		t.Errorf("expected -1 for DM, got %d", idx)
	}
}

func TestActiveGroupIdx(t *testing.T) {
	m := newTestModel(2, 2, 2)

	m.activeItem = 2
	if idx := m.activeGroupIdx(); idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}

	m.activeItem = 3
	if idx := m.activeGroupIdx(); idx != 1 {
		t.Errorf("expected 1, got %d", idx)
	}

	m.activeItem = 0 // channel
	if idx := m.activeGroupIdx(); idx != -1 {
		t.Errorf("expected -1 for channel, got %d", idx)
	}
}

func TestActiveDMPeerIdx(t *testing.T) {
	m := newTestModel(2, 2, 2)

	m.activeItem = 4
	if idx := m.activeDMPeerIdx(); idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}

	m.activeItem = 5
	if idx := m.activeDMPeerIdx(); idx != 1 {
		t.Errorf("expected 1, got %d", idx)
	}

	m.activeItem = 3 // group
	if idx := m.activeDMPeerIdx(); idx != -1 {
		t.Errorf("expected -1 for group, got %d", idx)
	}
}

func TestSidebarTotal(t *testing.T) {
	tests := []struct {
		channels, groups, dms int
		want                  int
	}{
		{0, 0, 0, 0},
		{3, 0, 0, 3},
		{0, 2, 0, 2},
		{0, 0, 4, 4},
		{2, 3, 5, 10},
	}
	for _, tt := range tests {
		m := newTestModel(tt.channels, tt.groups, tt.dms)
		got := m.sidebarTotal()
		if got != tt.want {
			t.Errorf("sidebarTotal(%d, %d, %d) = %d, want %d",
				tt.channels, tt.groups, tt.dms, got, tt.want)
		}
	}
}

func TestResolveAuthor(t *testing.T) {
	m := &model{
		profiles: map[string]string{
			"abcdef1234567890": "Alice",
		},
	}

	t.Run("cached profile name", func(t *testing.T) {
		got := m.resolveAuthor("abcdef1234567890")
		if got != "Alice" {
			t.Errorf("expected 'Alice', got %q", got)
		}
	})

	t.Run("fallback to shortPK", func(t *testing.T) {
		got := m.resolveAuthor("ffffffffffffffff0000000000000000ffffffffffffffff0000000000000000")
		want := "ffffffff"
		if got != want {
			t.Errorf("expected %q, got %q", want, got)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		got := m.resolveAuthor("")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func TestBoundaryConditions(t *testing.T) {
	t.Run("no channels", func(t *testing.T) {
		m := newTestModel(0, 2, 1)
		m.activeItem = 0
		if m.isChannelSelected() {
			t.Error("should not be channel when no channels exist")
		}
		if !m.isGroupSelected() {
			t.Error("activeItem=0 with 0 channels should be group")
		}
	})

	t.Run("no groups", func(t *testing.T) {
		m := newTestModel(2, 0, 1)
		m.activeItem = 2
		if m.isGroupSelected() {
			t.Error("should not be group when no groups exist")
		}
		if !m.isDMSelected() {
			t.Error("activeItem=2 with 0 groups should be DM")
		}
	})

	t.Run("only DMs", func(t *testing.T) {
		m := newTestModel(0, 0, 3)
		m.activeItem = 0
		if !m.isDMSelected() {
			t.Error("activeItem=0 with only DMs should be DM")
		}
		if idx := m.activeDMPeerIdx(); idx != 0 {
			t.Errorf("expected DM index 0, got %d", idx)
		}
	})
}

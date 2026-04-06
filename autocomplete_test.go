package main

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"fiatjaf.com/nostr"
)

// testModel creates a minimal model for autocomplete testing.
func testModel() model {
	ta := textarea.New()
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	return model{
		profiles:       make(map[string]string),
		profilePending: make(map[string]bool),
		msgs:           make(map[string][]ChatMessage),
		unread:         make(map[string]bool),
		roomSubs:       make(map[string]*roomSub),
		groupRecentIDs: make(map[string][]string),
		localDMEchoes:  make(map[string]time.Time),
		seenEvents:     make(map[string]time.Time),
		input:          ta,
		viewport:       vp,
		keys:           Keys{PK: nostr.PubKey{}},
	}
}

func TestAutocompleteDMFromGroupAuthors(t *testing.T) {
	m := testModel()

	// Set up a group in the sidebar and make it active.
	gk := groupKey("wss://groups.example.com", "testgroup")
	m.appendGroupItem(Group{RelayURL: "wss://groups.example.com", GroupID: "testgroup", Name: "testgroup"})
	m.activeItem = 0

	// Add messages from group members.
	alicePK := "aaaa000000000000000000000000000000000000000000000000000000000001"
	bobPK := "bbbb000000000000000000000000000000000000000000000000000000000002"
	kenjiPK := "cccc000000000000000000000000000000000000000000000000000000000003"
	karlPK := "dddd000000000000000000000000000000000000000000000000000000000004"

	m.profiles[alicePK] = "alice"
	m.profiles[bobPK] = "bob"
	m.profiles[kenjiPK] = "kenji"
	m.profiles[karlPK] = "karl"

	m.msgs[gk] = []ChatMessage{
		{PubKey: alicePK, Author: shortPK(alicePK), Content: "hello"},
		{PubKey: bobPK, Author: shortPK(bobPK), Content: "hi"},
		{PubKey: kenjiPK, Author: shortPK(kenjiPK), Content: "hey"},
		{PubKey: karlPK, Author: shortPK(karlPK), Content: "hola"},
	}

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "dm k matches kenji and karl from group",
			input: "/dm k",
			want:  []string{"karl", "kenji"},
		},
		{
			name:  "dm ke matches only kenji",
			input: "/dm ke",
			want:  []string{"kenji"},
		},
		{
			name:  "dm shows all group members",
			input: "/dm ",
			want:  []string{"karl", "kenji", "bob", "alice"},
		},
		{
			name:  "invite k matches from group too",
			input: "/invite k",
			want:  []string{"karl", "kenji"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.input.SetValue(tt.input)
			m.acSuggestions = nil
			m.updateSuggestions()

			if len(m.acSuggestions) != len(tt.want) {
				t.Errorf("got %d suggestions %v, want %d %v", len(m.acSuggestions), m.acSuggestions, len(tt.want), tt.want)
				return
			}
			for i, s := range m.acSuggestions {
				if s != tt.want[i] {
					t.Errorf("suggestion[%d] = %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}

func TestAutocompleteDMDeduplicates(t *testing.T) {
	m := testModel()

	// alice is both a DM contact and a group member.
	alicePK := "aaaa000000000000000000000000000000000000000000000000000000000001"
	m.profiles[alicePK] = "alice"

	// Add alice as a DM peer in sidebar.
	m.appendDMItem(alicePK, "alice")

	// Add a group and make it active.
	gk := groupKey("wss://groups.example.com", "testgroup")
	m.appendGroupItem(Group{RelayURL: "wss://groups.example.com", GroupID: "testgroup", Name: "testgroup"})
	// Group is second item (after DM).
	m.activeItem = 1

	// Alice also has messages in the group.
	m.msgs[gk] = []ChatMessage{
		{PubKey: alicePK, Author: shortPK(alicePK), Content: "hello from group"},
	}

	m.input.SetValue("/dm a")
	m.updateSuggestions()

	count := 0
	for _, s := range m.acSuggestions {
		if s == "alice" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("alice appeared %d times in suggestions %v, want exactly 1", count, m.acSuggestions)
	}
}

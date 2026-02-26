package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
)

// testKeys generates a fresh key pair and keyer for tests.
func testKeysWithKeyer(t *testing.T) (Keys, nostr.Keyer) {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	kr, err := keyer.NewPlainKeySigner(sk)
	if err != nil {
		t.Fatalf("NewPlainKeySigner: %v", err)
	}
	return Keys{SK: sk, PK: pk, NPub: "npub1test"}, kr
}

func TestSelfEncryptDecryptRoundtrip(t *testing.T) {
	_, kr := testKeysWithKeyer(t)
	ctx := context.Background()

	plaintext := `[["p","abc123","","alice"]]`
	ciphertext, err := selfEncrypt(ctx, kr, plaintext)
	if err != nil {
		t.Fatalf("selfEncrypt: %v", err)
	}
	if ciphertext == plaintext {
		t.Fatal("ciphertext should differ from plaintext")
	}

	got, err := selfDecrypt(ctx, kr, ciphertext)
	if err != nil {
		t.Fatalf("selfDecrypt: %v", err)
	}
	if got != plaintext {
		t.Errorf("selfDecrypt = %q, want %q", got, plaintext)
	}
}

func TestSelfEncryptEmptyString(t *testing.T) {
	_, kr := testKeysWithKeyer(t)
	ctx := context.Background()

	// NIP-44 requires minimum 1 byte plaintext, so encrypt "[]" not ""
	ciphertext, err := selfEncrypt(ctx, kr, "[]")
	if err != nil {
		t.Fatalf("selfEncrypt: %v", err)
	}
	got, err := selfDecrypt(ctx, kr, ciphertext)
	if err != nil {
		t.Fatalf("selfDecrypt: %v", err)
	}
	if got != "[]" {
		t.Errorf("selfDecrypt = %q, want %q", got, "[]")
	}
}

func TestBuildParseContactsListRoundtrip(t *testing.T) {
	keys, kr := testKeysWithKeyer(t)
	ctx := context.Background()

	contacts := []Contact{
		{Name: "alice", PubKey: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"},
		{Name: "bob", PubKey: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"},
	}

	evt, err := buildContactsListEvent(ctx, contacts, keys, kr)
	if err != nil {
		t.Fatalf("buildContactsListEvent: %v", err)
	}

	// Verify event structure
	if evt.Kind != nostr.KindCategorizedPeopleList {
		t.Errorf("kind = %d, want %d", evt.Kind, nostr.KindCategorizedPeopleList)
	}
	if dTag := evt.Tags.GetFirst([]string{"d", "Chat-Friends"}); dTag == nil {
		t.Error("missing d-tag 'Chat-Friends'")
	}
	if evt.Content == "" {
		t.Error("content should be non-empty (encrypted)")
	}

	// Parse it back
	got, err := parseContactsListEvent(ctx, &evt, kr)
	if err != nil {
		t.Fatalf("parseContactsListEvent: %v", err)
	}
	if len(got) != len(contacts) {
		t.Fatalf("got %d contacts, want %d", len(got), len(contacts))
	}
	for i, c := range got {
		if c.PubKey != contacts[i].PubKey {
			t.Errorf("contact[%d].PubKey = %q, want %q", i, c.PubKey, contacts[i].PubKey)
		}
		if c.Name != contacts[i].Name {
			t.Errorf("contact[%d].Name = %q, want %q", i, c.Name, contacts[i].Name)
		}
	}
}

func TestBuildContactsListEvent_0xchatFormat(t *testing.T) {
	keys, kr := testKeysWithKeyer(t)
	ctx := context.Background()

	contacts := []Contact{
		{Name: "alice", PubKey: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"},
	}

	evt, err := buildContactsListEvent(ctx, contacts, keys, kr)
	if err != nil {
		t.Fatalf("buildContactsListEvent: %v", err)
	}

	// Decrypt and verify the JSON format matches 0xchat's [["p","pk","relay","petname"]]
	plaintext, err := selfDecrypt(ctx, kr, evt.Content)
	if err != nil {
		t.Fatalf("selfDecrypt: %v", err)
	}

	var tags [][]string
	if err := json.Unmarshal([]byte(plaintext), &tags); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	tag := tags[0]
	if len(tag) != 4 {
		t.Fatalf("expected tag length 4, got %d: %v", len(tag), tag)
	}
	if tag[0] != "p" {
		t.Errorf("tag[0] = %q, want 'p'", tag[0])
	}
	if tag[1] != contacts[0].PubKey {
		t.Errorf("tag[1] = %q, want %q", tag[1], contacts[0].PubKey)
	}
	if tag[2] != "" {
		t.Errorf("tag[2] = %q, want empty (relay hint)", tag[2])
	}
	if tag[3] != "alice" {
		t.Errorf("tag[3] = %q, want 'alice' (petname)", tag[3])
	}
}

func TestBuildParseContactsListEmpty(t *testing.T) {
	keys, kr := testKeysWithKeyer(t)
	ctx := context.Background()

	evt, err := buildContactsListEvent(ctx, nil, keys, kr)
	if err != nil {
		t.Fatalf("buildContactsListEvent: %v", err)
	}

	got, err := parseContactsListEvent(ctx, &evt, kr)
	if err != nil {
		t.Fatalf("parseContactsListEvent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d contacts, want 0", len(got))
	}
}

func TestParseContactsListEmptyContent(t *testing.T) {
	ctx := context.Background()
	_, kr := testKeysWithKeyer(t)
	evt := &nostr.Event{Content: ""}

	got, err := parseContactsListEvent(ctx, evt, kr)
	if err != nil {
		t.Fatalf("parseContactsListEvent: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestBuildParsePublicChatsListRoundtrip(t *testing.T) {
	keys, _ := testKeysWithKeyer(t)

	channels := []Channel{
		{ID: "event1111111111111111111111111111111111111111111111111111111111", Name: "general"},
		{ID: "event2222222222222222222222222222222222222222222222222222222222", Name: "random"},
	}

	evt, err := buildPublicChatsListEvent(channels, keys)
	if err != nil {
		t.Fatalf("buildPublicChatsListEvent: %v", err)
	}

	// Verify event structure
	if evt.Kind != nostr.KindPublicChatList {
		t.Errorf("kind = %d, want %d", evt.Kind, nostr.KindPublicChatList)
	}
	if evt.Content != "" {
		t.Errorf("content = %q, want empty", evt.Content)
	}

	// Check tags
	var eTags []string
	for _, tag := range evt.Tags {
		if len(tag) >= 2 && tag[0] == "e" {
			eTags = append(eTags, tag[1])
		}
	}
	if len(eTags) != 2 {
		t.Fatalf("got %d e-tags, want 2", len(eTags))
	}

	// Parse back
	got := parsePublicChatsListEvent(&evt)
	if len(got) != len(channels) {
		t.Fatalf("got %d channels, want %d", len(got), len(channels))
	}
	for i, ch := range got {
		if ch.ID != channels[i].ID {
			t.Errorf("channel[%d].ID = %q, want %q", i, ch.ID, channels[i].ID)
		}
	}
}

func TestBuildParsePublicChatsListEmpty(t *testing.T) {
	keys, _ := testKeysWithKeyer(t)

	evt, err := buildPublicChatsListEvent(nil, keys)
	if err != nil {
		t.Fatalf("buildPublicChatsListEvent: %v", err)
	}

	got := parsePublicChatsListEvent(&evt)
	if len(got) != 0 {
		t.Errorf("got %d channels, want 0", len(got))
	}
}

func TestBuildParseSimpleGroupsListRoundtrip(t *testing.T) {
	keys, _ := testKeysWithKeyer(t)

	groups := []Group{
		{RelayURL: "wss://groups.example.com", GroupID: "abc123", Name: "test-group"},
		{RelayURL: "wss://other.relay.com", GroupID: "def456", Name: "another"},
	}

	evt, err := buildSimpleGroupsListEvent(groups, keys)
	if err != nil {
		t.Fatalf("buildSimpleGroupsListEvent: %v", err)
	}

	// Verify event structure
	if evt.Kind != nostr.KindSimpleGroupList {
		t.Errorf("kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupList)
	}
	if evt.Content != "" {
		t.Errorf("content = %q, want empty", evt.Content)
	}

	// Parse back
	got := parseSimpleGroupsListEvent(&evt)
	if len(got) != len(groups) {
		t.Fatalf("got %d groups, want %d", len(got), len(groups))
	}
	for i, g := range got {
		if g.RelayURL != groups[i].RelayURL {
			t.Errorf("group[%d].RelayURL = %q, want %q", i, g.RelayURL, groups[i].RelayURL)
		}
		if g.GroupID != groups[i].GroupID {
			t.Errorf("group[%d].GroupID = %q, want %q", i, g.GroupID, groups[i].GroupID)
		}
		if g.Name != groups[i].Name {
			t.Errorf("group[%d].Name = %q, want %q", i, g.Name, groups[i].Name)
		}
	}
}

func TestBuildParseSimpleGroupsListEmpty(t *testing.T) {
	keys, _ := testKeysWithKeyer(t)

	evt, err := buildSimpleGroupsListEvent(nil, keys)
	if err != nil {
		t.Fatalf("buildSimpleGroupsListEvent: %v", err)
	}

	got := parseSimpleGroupsListEvent(&evt)
	if len(got) != 0 {
		t.Errorf("got %d groups, want 0", len(got))
	}
}

func TestSimpleGroupsListTagFormat(t *testing.T) {
	keys, _ := testKeysWithKeyer(t)

	groups := []Group{
		{RelayURL: "wss://groups.example.com", GroupID: "abc123", Name: "mygroup"},
	}

	evt, err := buildSimpleGroupsListEvent(groups, keys)
	if err != nil {
		t.Fatalf("buildSimpleGroupsListEvent: %v", err)
	}

	// Verify tag format: ["group", groupID, relayURL, name]
	found := false
	for _, tag := range evt.Tags {
		if len(tag) >= 3 && tag[0] == "group" {
			found = true
			if tag[1] != "abc123" {
				t.Errorf("tag[1] = %q, want 'abc123'", tag[1])
			}
			if tag[2] != "wss://groups.example.com" {
				t.Errorf("tag[2] = %q, want 'wss://groups.example.com'", tag[2])
			}
			if len(tag) >= 4 && tag[3] != "mygroup" {
				t.Errorf("tag[3] = %q, want 'mygroup'", tag[3])
			}
		}
	}
	if !found {
		t.Error("no 'group' tag found in event")
	}
}

func TestContactsFromModel(t *testing.T) {
	dmPeers := []string{"pk1", "pk2", "pk3"}
	profiles := map[string]string{
		"pk1": "alice",
		"pk3": "charlie",
	}

	got := contactsFromModel(dmPeers, profiles)
	if len(got) != 3 {
		t.Fatalf("got %d contacts, want 3", len(got))
	}

	// pk1 has profile name
	if got[0].Name != "alice" || got[0].PubKey != "pk1" {
		t.Errorf("contact[0] = %+v, want {alice, pk1}", got[0])
	}
	// pk2 has no profile, falls back to shortPK
	if got[1].Name != shortPK("pk2") || got[1].PubKey != "pk2" {
		t.Errorf("contact[1] = %+v, want {%s, pk2}", got[1], shortPK("pk2"))
	}
	// pk3 has profile name
	if got[2].Name != "charlie" || got[2].PubKey != "pk3" {
		t.Errorf("contact[2] = %+v, want {charlie, pk3}", got[2])
	}
}

func TestContactsFromModelEmpty(t *testing.T) {
	got := contactsFromModel(nil, nil)
	if len(got) != 0 {
		t.Errorf("got %d contacts, want 0", len(got))
	}
}

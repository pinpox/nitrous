package main

import (
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// testKeys generates a fresh keypair for testing.
func testKeys(t *testing.T) Keys {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	npub, err := nip19.EncodePublicKey(pk)
	if err != nil {
		t.Fatalf("EncodePublicKey: %v", err)
	}
	return Keys{SK: sk, PK: pk, NPub: npub}
}

// hasTag checks if an event has a tag with the given key and value.
func hasTag(evt nostr.Event, key, value string) bool {
	for _, tag := range evt.Tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

// hasTagKey checks if an event has a tag with the given key (any value).
func hasTagKey(evt nostr.Event, key string) bool {
	for _, tag := range evt.Tags {
		if len(tag) >= 1 && tag[0] == key {
			return true
		}
	}
	return false
}

func TestBuildCreateChannelEvent(t *testing.T) {
	keys := testKeys(t)
	evt, err := buildCreateChannelEvent("test-channel", keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != 40 {
		t.Errorf("Kind = %d, want 40", evt.Kind)
	}

	// Content should be valid JSON with name field.
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(evt.Content), &meta); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if meta.Name != "test-channel" {
		t.Errorf("content name = %q, want %q", meta.Name, "test-channel")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildChannelMessageEvent(t *testing.T) {
	keys := testKeys(t)
	channelID := "abc123def456abc123def456abc123def456abc123def456abc123def456abcd"
	content := "hello world"

	evt, err := buildChannelMessageEvent(channelID, content, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != 42 {
		t.Errorf("Kind = %d, want 42", evt.Kind)
	}
	if evt.Content != content {
		t.Errorf("Content = %q, want %q", evt.Content, content)
	}

	// Check for root tag: ["e", channelID, "", "root"]
	found := false
	for _, tag := range evt.Tags {
		if len(tag) >= 4 && tag[0] == "e" && tag[1] == channelID && tag[3] == "root" {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing [\"e\", channelID, \"\", \"root\"] tag")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildProfileEvent(t *testing.T) {
	keys := testKeys(t)
	profile := ProfileConfig{
		Name:        "alice",
		DisplayName: "Alice Wonderland",
		About:       "Down the rabbit hole",
		Picture:     "https://example.com/alice.png",
	}

	evt, err := buildProfileEvent(profile, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != 0 {
		t.Errorf("Kind = %d, want 0", evt.Kind)
	}

	// Content should be valid JSON with all profile fields.
	var meta map[string]string
	if err := json.Unmarshal([]byte(evt.Content), &meta); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if meta["name"] != "alice" {
		t.Errorf("name = %q, want %q", meta["name"], "alice")
	}
	if meta["display_name"] != "Alice Wonderland" {
		t.Errorf("display_name = %q, want %q", meta["display_name"], "Alice Wonderland")
	}
	if meta["about"] != "Down the rabbit hole" {
		t.Errorf("about = %q, want %q", meta["about"], "Down the rabbit hole")
	}
	if meta["picture"] != "https://example.com/alice.png" {
		t.Errorf("picture = %q, want %q", meta["picture"], "https://example.com/alice.png")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildProfileEventEmptyFields(t *testing.T) {
	keys := testKeys(t)
	profile := ProfileConfig{Name: "bob"}

	evt, err := buildProfileEvent(profile, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var meta map[string]string
	if err := json.Unmarshal([]byte(evt.Content), &meta); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if _, ok := meta["display_name"]; ok {
		t.Error("expected empty display_name to be omitted")
	}
	if _, ok := meta["about"]; ok {
		t.Error("expected empty about to be omitted")
	}
}

func TestBuildDMRelaysEvent(t *testing.T) {
	keys := testKeys(t)
	relays := []string{"wss://relay1.com", "wss://relay2.com", "wss://relay3.com"}

	evt, err := buildDMRelaysEvent(relays, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != 10050 {
		t.Errorf("Kind = %d, want 10050", evt.Kind)
	}

	// Check relay tags.
	relayCount := 0
	for _, tag := range evt.Tags {
		if len(tag) >= 2 && tag[0] == "relay" {
			relayCount++
		}
	}
	if relayCount != 3 {
		t.Errorf("expected 3 relay tags, got %d", relayCount)
	}

	if !hasTag(evt, "relay", "wss://relay1.com") {
		t.Error("missing relay tag for wss://relay1.com")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildGroupMessageEvent(t *testing.T) {
	keys := testKeys(t)
	groupID := "testgroup"
	content := "hello group"
	previousIDs := []string{"aaa111", "bbb222"}

	evt, err := buildGroupMessageEvent(groupID, content, previousIDs, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != nostr.KindSimpleGroupChatMessage {
		t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupChatMessage)
	}
	if evt.Content != content {
		t.Errorf("Content = %q, want %q", evt.Content, content)
	}
	if !hasTag(evt, "h", groupID) {
		t.Error("missing [\"h\", groupID] tag")
	}
	if !hasTagKey(evt, "previous") {
		t.Error("missing 'previous' tags")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildJoinGroupEvent(t *testing.T) {
	keys := testKeys(t)

	t.Run("without invite code", func(t *testing.T) {
		evt, err := buildJoinGroupEvent("grp1", nil, "", keys)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.Kind != nostr.KindSimpleGroupJoinRequest {
			t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupJoinRequest)
		}
		if !hasTag(evt, "h", "grp1") {
			t.Error("missing [\"h\", \"grp1\"] tag")
		}
		if hasTagKey(evt, "code") {
			t.Error("should not have 'code' tag without invite code")
		}
		if ok, err := evt.CheckSignature(); err != nil || !ok {
			t.Errorf("invalid signature: ok=%v err=%v", ok, err)
		}
	})

	t.Run("with invite code", func(t *testing.T) {
		evt, err := buildJoinGroupEvent("grp1", nil, "secret123", keys)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasTag(evt, "code", "secret123") {
			t.Error("missing [\"code\", \"secret123\"] tag")
		}
		if ok, err := evt.CheckSignature(); err != nil || !ok {
			t.Errorf("invalid signature: ok=%v err=%v", ok, err)
		}
	})
}

func TestBuildLeaveGroupEvent(t *testing.T) {
	keys := testKeys(t)
	evt, err := buildLeaveGroupEvent("grp1", []string{"prev1"}, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != nostr.KindSimpleGroupLeaveRequest {
		t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupLeaveRequest)
	}
	if !hasTag(evt, "h", "grp1") {
		t.Error("missing [\"h\", \"grp1\"] tag")
	}
	if !hasTagKey(evt, "previous") {
		t.Error("missing 'previous' tag")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildCreateGroupEvent(t *testing.T) {
	keys := testKeys(t)
	evt, err := buildCreateGroupEvent("abc12345", "My Group", keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != nostr.KindSimpleGroupCreateGroup {
		t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupCreateGroup)
	}
	if !hasTag(evt, "h", "abc12345") {
		t.Error("missing [\"h\", \"abc12345\"] tag")
	}
	if !hasTag(evt, "name", "My Group") {
		t.Error("missing [\"name\", \"My Group\"] tag")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildDeleteGroupEventEvent(t *testing.T) {
	keys := testKeys(t)
	eventID := "deadbeef12345678"
	evt, err := buildDeleteGroupEventEvent("grp1", eventID, []string{"prev1", "prev2"}, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != nostr.KindSimpleGroupDeleteEvent {
		t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupDeleteEvent)
	}
	if !hasTag(evt, "h", "grp1") {
		t.Error("missing [\"h\", \"grp1\"] tag")
	}
	if !hasTag(evt, "e", eventID) {
		t.Errorf("missing [\"e\", %q] tag", eventID)
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildPutUserEvent(t *testing.T) {
	keys := testKeys(t)
	userPK := "aaaa1111bbbb2222cccc3333dddd4444aaaa1111bbbb2222cccc3333dddd4444"
	evt, err := buildPutUserEvent("grp1", userPK, nil, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != nostr.KindSimpleGroupPutUser {
		t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupPutUser)
	}
	if !hasTag(evt, "h", "grp1") {
		t.Error("missing [\"h\", \"grp1\"] tag")
	}
	if !hasTag(evt, "p", userPK) {
		t.Errorf("missing [\"p\", %q] tag", userPK)
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildEditGroupMetadataEvent(t *testing.T) {
	keys := testKeys(t)

	t.Run("with name", func(t *testing.T) {
		fields := map[string]string{"name": "New Name"}
		evt, err := buildEditGroupMetadataEvent("grp1", fields, nil, keys)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if evt.Kind != nostr.KindSimpleGroupEditMetadata {
			t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupEditMetadata)
		}
		if !hasTag(evt, "h", "grp1") {
			t.Error("missing [\"h\", \"grp1\"] tag")
		}
		if !hasTag(evt, "name", "New Name") {
			t.Error("missing [\"name\", \"New Name\"] tag")
		}

		if ok, err := evt.CheckSignature(); err != nil || !ok {
			t.Errorf("invalid signature: ok=%v err=%v", ok, err)
		}
	})

	t.Run("with empty value (boolean tag)", func(t *testing.T) {
		fields := map[string]string{"closed": ""}
		evt, err := buildEditGroupMetadataEvent("grp1", fields, nil, keys)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have a single-element tag ["closed"]
		found := false
		for _, tag := range evt.Tags {
			if len(tag) == 1 && tag[0] == "closed" {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing single-element [\"closed\"] tag")
		}
	})
}

func TestBuildCreateGroupInviteEvent(t *testing.T) {
	keys := testKeys(t)
	evt, err := buildCreateGroupInviteEvent("grp1", []string{"prev1"}, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != nostr.KindSimpleGroupCreateInvite {
		t.Errorf("Kind = %d, want %d", evt.Kind, nostr.KindSimpleGroupCreateInvite)
	}
	if !hasTag(evt, "h", "grp1") {
		t.Error("missing [\"h\", \"grp1\"] tag")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

func TestBuildBlossomAuthEvent(t *testing.T) {
	keys := testKeys(t)
	hashHex := "abc123def456"

	evt, err := buildBlossomAuthEvent(hashHex, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Kind != 24242 {
		t.Errorf("Kind = %d, want 24242", evt.Kind)
	}

	if !hasTag(evt, "t", "upload") {
		t.Error("missing [\"t\", \"upload\"] tag")
	}
	if !hasTag(evt, "x", hashHex) {
		t.Errorf("missing [\"x\", %q] tag", hashHex)
	}
	if !hasTagKey(evt, "expiration") {
		t.Error("missing 'expiration' tag")
	}

	if ok, err := evt.CheckSignature(); err != nil || !ok {
		t.Errorf("invalid signature: ok=%v err=%v", ok, err)
	}
}

// TestEventBuildersPubKeyConsistency verifies all builders set the correct pubkey.
func TestEventBuildersPubKeyConsistency(t *testing.T) {
	keys := testKeys(t)

	builders := []struct {
		name  string
		build func() (nostr.Event, error)
	}{
		{"CreateChannel", func() (nostr.Event, error) { return buildCreateChannelEvent("ch", keys) }},
		{"ChannelMessage", func() (nostr.Event, error) { return buildChannelMessageEvent("ch", "hi", keys) }},
		{"Profile", func() (nostr.Event, error) {
			return buildProfileEvent(ProfileConfig{Name: "test"}, keys)
		}},
		{"DMRelays", func() (nostr.Event, error) { return buildDMRelaysEvent([]string{"wss://r"}, keys) }},
		{"GroupMessage", func() (nostr.Event, error) { return buildGroupMessageEvent("g", "hi", nil, keys) }},
		{"JoinGroup", func() (nostr.Event, error) { return buildJoinGroupEvent("g", nil, "", keys) }},
		{"LeaveGroup", func() (nostr.Event, error) { return buildLeaveGroupEvent("g", nil, keys) }},
		{"CreateGroup", func() (nostr.Event, error) { return buildCreateGroupEvent("gid", "name", keys) }},
		{"DeleteGroupEvent", func() (nostr.Event, error) { return buildDeleteGroupEventEvent("g", "e", nil, keys) }},
		{"PutUser", func() (nostr.Event, error) { return buildPutUserEvent("g", "pk", nil, keys) }},
		{"EditGroupMetadata", func() (nostr.Event, error) {
			return buildEditGroupMetadataEvent("g", map[string]string{"name": "n"}, nil, keys)
		}},
		{"CreateGroupInvite", func() (nostr.Event, error) { return buildCreateGroupInviteEvent("g", nil, keys) }},
		{"BlossomAuth", func() (nostr.Event, error) { return buildBlossomAuthEvent("hash", keys) }},
	}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			evt, err := b.build()
			if err != nil {
				t.Fatalf("build error: %v", err)
			}
			if evt.PubKey != keys.PK {
				t.Errorf("PubKey = %q, want %q", evt.PubKey, keys.PK)
			}
		})
	}
}

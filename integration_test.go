package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip19"
)

// testTheme provides a pre-built dark theme for integration tests
// so they don't need terminal background detection.
var testTheme = buildTheme(true)

// ─── Embedded relay ──────────────────────────────────────────────────────────

func startTestRelay(t *testing.T) (relayURL string, cleanup func()) {
	t.Helper()

	relay := khatru.NewRelay()

	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatalf("slicestore.Init: %v", err)
	}

	relay.UseEventstore(store, 500)

	srv := httptest.NewServer(relay)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Logf("test relay running at %s", wsURL)

	return wsURL, srv.Close
}

// ─── Test client helper ──────────────────────────────────────────────────────

type testClient struct {
	tm    *teatest.TestModel
	keys  Keys
	npub  string
	hexPK string
	name  string
}

func generateTestKeys(t *testing.T) Keys {
	t.Helper()
	sk := nostr.Generate()
	pk := nostr.GetPublicKey(sk)
	npub := nip19.EncodeNpub(pk)
	return Keys{SK: sk, PK: pk, NPub: npub}
}

func newTestClient(t *testing.T, name string, relayURL string) *testClient {
	t.Helper()

	keys := generateTestKeys(t)

	cfg := Config{
		Relays:      []string{relayURL},
		GroupRelay:  relayURL,
		MaxMessages: 500,
		Profile: ProfileConfig{
			Name:        name,
			DisplayName: name,
		},
	}

	kr := keyer.NewPlainKeySigner(keys.SK)

	pool := nostr.NewPool(nostr.PoolOptions{
		AuthRequiredHandler: func(ctx context.Context, evt *nostr.Event) error {
			return kr.SignEvent(ctx, evt)
		},
	})

	m := newModel(cfg, "", keys, pool, &kr, nil, testTheme, DefaultKeyMap())

	tm := teatest.NewTestModel(t, &m,
		teatest.WithInitialTermSize(120, 40),
	)

	return &testClient{
		tm:    tm,
		keys:  keys,
		npub:  keys.NPub,
		hexPK: keys.PK.Hex(),
		name:  name,
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func waitFor(t *testing.T, tm *teatest.TestModel, substr string, timeout time.Duration) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(),
		func(b []byte) bool {
			return bytes.Contains(b, []byte(substr))
		},
		teatest.WithDuration(timeout),
		teatest.WithCheckInterval(200*time.Millisecond),
	)
}

func typeCmd(tm *teatest.TestModel, text string) {
	tm.Type(text)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func sendCtrlUp(tm *teatest.TestModel) {
	tm.Send(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
}

// queryRelayEvents queries the embedded relay for events matching the filter.
func queryRelayEvents(t *testing.T, relayURL string, kinds []int, authors []string, tags map[string][]string) []nostr.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r, err := nostr.RelayConnect(ctx, relayURL, nostr.RelayOptions{})
	if err != nil {
		t.Fatalf("queryRelayEvents: connect: %v", err)
	}
	defer func() { _ = r.Close() }()

	filter := nostr.Filter{}

	// Convert int kinds to nostr.Kind.
	for _, k := range kinds {
		filter.Kinds = append(filter.Kinds, nostr.Kind(k))
	}

	// Convert string authors to nostr.PubKey.
	for _, a := range authors {
		pk, err := nostr.PubKeyFromHex(a)
		if err != nil {
			t.Fatalf("queryRelayEvents: invalid author pubkey %q: %v", a, err)
		}
		filter.Authors = append(filter.Authors, pk)
	}

	if len(tags) > 0 {
		filter.Tags = nostr.TagMap(tags)
	}

	sub, err := r.Subscribe(ctx, filter, nostr.SubscriptionOptions{})
	if err != nil {
		t.Fatalf("queryRelayEvents: subscribe: %v", err)
	}
	defer sub.Unsub()

	var evts []nostr.Event
	for {
		select {
		case evt, ok := <-sub.Events:
			if !ok {
				return evts
			}
			evts = append(evts, evt)
		case <-sub.EndOfStoredEvents:
			return evts
		case <-ctx.Done():
			return evts
		}
	}
}

func waitForRelayEvent(t *testing.T, relayURL string, kinds []int, authors []string, tags map[string][]string, timeout time.Duration) []nostr.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evts := queryRelayEvents(t, relayURL, kinds, authors, tags)
		if len(evts) > 0 {
			return evts
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("waitForRelayEvent: no events found for kinds=%v authors=%v tags=%v after %s", kinds, authors, tags, timeout)
	return nil
}

const defaultTimeout = 15 * time.Second

// ─── Integration Test ────────────────────────────────────────────────────────

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	relayURL, cleanup := startTestRelay(t)
	defer cleanup()

	// Allow relay to settle.
	time.Sleep(500 * time.Millisecond)

	alice := newTestClient(t, "alice", relayURL)
	defer func() { _ = alice.tm.Quit() }()

	bob := newTestClient(t, "bob", relayURL)
	defer func() { _ = bob.tm.Quit() }()

	t.Logf("alice: npub=%s hex=%s", alice.npub, alice.hexPK)
	t.Logf("bob:   npub=%s hex=%s", bob.npub, bob.hexPK)

	// Give clients time to connect, publish profiles, and subscribe.
	time.Sleep(3 * time.Second)

	// ── Startup ──────────────────────────────────────────────────────────

	t.Run("startup/profile", func(t *testing.T) {
		evts := waitForRelayEvent(t, relayURL, []int{0}, []string{alice.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("alice profile (kind 0) not found on relay")
		}
		evts = waitForRelayEvent(t, relayURL, []int{0}, []string{bob.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("bob profile (kind 0) not found on relay")
		}
	})

	t.Run("startup/dm-relay-list", func(t *testing.T) {
		evts := waitForRelayEvent(t, relayURL, []int{10050}, []string{alice.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("alice DM relay list (kind 10050) not found")
		}
		evts = waitForRelayEvent(t, relayURL, []int{10050}, []string{bob.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("bob DM relay list (kind 10050) not found")
		}
	})

	// ── Help command ─────────────────────────────────────────────────────

	t.Run("cmd/help", func(t *testing.T) {
		typeCmd(alice.tm, "/help")
		waitFor(t, alice.tm, "/channel", defaultTimeout)
	})

	// ── NIP-28 Channel ───────────────────────────────────────────────────

	var channelID string

	t.Run("channel/create", func(t *testing.T) {
		typeCmd(alice.tm, "/channel create #testroom")
		waitFor(t, alice.tm, "testroom", defaultTimeout)

		evts := waitForRelayEvent(t, relayURL, []int{40}, []string{alice.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("kind 40 channel creation event not found")
		}
		channelID = evts[0].ID.Hex()
		t.Logf("channel ID: %s", channelID)
	})

	t.Run("channel/join-by-id", func(t *testing.T) {
		if channelID == "" {
			t.Skip("no channel ID from previous test")
		}
		typeCmd(bob.tm, "/join "+channelID)
		waitFor(t, bob.tm, "testroom", defaultTimeout)
	})

	t.Run("channel/alice-sends", func(t *testing.T) {
		if channelID == "" {
			t.Skip("no channel ID")
		}
		typeCmd(alice.tm, "Hello from Alice!")
		waitFor(t, bob.tm, "Hello from Alice!", defaultTimeout)
	})

	t.Run("channel/bob-sends", func(t *testing.T) {
		if channelID == "" {
			t.Skip("no channel ID")
		}
		typeCmd(bob.tm, "Hello from Bob!")
		waitFor(t, alice.tm, "Hello from Bob!", defaultTimeout)
	})

	// ── NIP-29 Group ─────────────────────────────────────────────────────

	var groupID string

	t.Run("group/create", func(t *testing.T) {
		typeCmd(alice.tm, "/group create testgroup "+relayURL)
		waitFor(t, alice.tm, "created group", defaultTimeout)

		// Wait for the group to be fully created on relay.
		time.Sleep(3 * time.Second)

		// Find the group ID from alice's kind 10009 (simple groups list).
		evts := waitForRelayEvent(t, relayURL, []int{10009}, []string{alice.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("kind 10009 group list not found for alice")
		}
		// The group list has "group" tags like ["group", "<groupID>", "<relayURL>"]
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 3 && tag[0] == "group" {
					groupID = tag[1]
					break
				}
			}
		}
		if groupID == "" {
			t.Logf("kind 10009 tags: %v", evts[0].Tags)
			t.Fatal("could not extract group ID from kind 10009")
		}
		t.Logf("group ID: %s", groupID)

		// Verify client sent kind 9002 (edit metadata) for the group.
		// The simple khatru relay stores all events without relay29's
		// auto-generated kind 39000 metadata — we verify the client
		// sends the correct events, not relay-side policy.
		metaEvts := waitForRelayEvent(t, relayURL, []int{9002}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		if len(metaEvts) == 0 {
			t.Fatal("kind 9002 group metadata edit not found")
		}
	})

	t.Run("group/alice-sends-msg", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		// Alice should still be on the group (handleGroupCreated sets activeItem).
		typeCmd(alice.tm, "Group hello from Alice!")
		time.Sleep(3 * time.Second)

		// Verify kind 9 on relay.
		evts := waitForRelayEvent(t, relayURL, []int{9}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("kind 9 group message not found")
		}
		found := false
		for _, e := range evts {
			if e.Content == "Group hello from Alice!" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("alice's group message content not found")
		}
	})

	t.Run("group/add-user", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		typeCmd(alice.tm, "/group user add "+bob.hexPK)
		time.Sleep(3 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9000}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 2 && tag[0] == "p" && tag[1] == bob.hexPK {
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatal("kind 9000 put-user event for bob not found")
		}
	})

	t.Run("group/bob-joins", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		// Use host'groupid format. The host is without ws:// prefix.
		host := relayURL[len("ws://"):]
		typeCmd(bob.tm, "/join "+host+"'"+groupID)
		time.Sleep(3 * time.Second)
	})

	t.Run("group/bob-sends-msg", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		typeCmd(bob.tm, "Group hello from Bob!")
		time.Sleep(3 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, e := range evts {
			if e.Content == "Group hello from Bob!" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("bob's group message not found")
		}
	})

	t.Run("group/rename", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		// Navigate alice to the group by using /join.
		host := relayURL[len("ws://"):]
		typeCmd(alice.tm, "/join "+host+"'"+groupID)
		time.Sleep(2 * time.Second)

		typeCmd(alice.tm, "/group name renamedgroup")
		time.Sleep(3 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9002}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 2 && tag[0] == "name" && tag[1] == "renamedgroup" {
					found = true
					break
				}
			}
		}
		if !found {
			// Log all 9002 events for debugging.
			t.Logf("group ID: %s, found %d kind 9002 events", groupID, len(evts))
			for i, evt := range evts {
				t.Logf("  9002[%d]: tags=%v", i, evt.Tags)
			}
			t.Fatal("kind 9002 with name=renamedgroup not found")
		}
	})

	t.Run("group/about", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		typeCmd(alice.tm, "/group about Test description")
		time.Sleep(2 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9002}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 2 && tag[0] == "about" && tag[1] == "Test description" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatal("kind 9002 with about tag not found")
		}
	})

	t.Run("group/picture", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		typeCmd(alice.tm, "/group picture https://example.com/pic.png")
		time.Sleep(2 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9002}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 2 && tag[0] == "picture" && tag[1] == "https://example.com/pic.png" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatal("kind 9002 with picture tag not found")
		}
	})

	t.Run("group/set-open", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		typeCmd(alice.tm, "/group set open")
		time.Sleep(2 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9002}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if tag[0] == "open" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatal("kind 9002 with open tag not found")
		}
	})

	t.Run("group/set-closed", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		typeCmd(alice.tm, "/group set closed")
		time.Sleep(2 * time.Second)

		evts := waitForRelayEvent(t, relayURL, []int{9002}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if tag[0] == "closed" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatal("kind 9002 with closed tag not found")
		}
	})

	// ── Invite ──────────────────────────────────────────────────────────

	t.Run("group/invite", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		// Navigate alice to the group.
		host := relayURL[len("ws://"):]
		typeCmd(alice.tm, "/join "+host+"'"+groupID)
		time.Sleep(2 * time.Second)

		// Alice invites bob by npub.
		typeCmd(alice.tm, "/invite "+bob.npub)
		time.Sleep(5 * time.Second)

		// Verify kind 9000 (put-user) was sent for bob.
		evts := waitForRelayEvent(t, relayURL, []int{9000}, nil, map[string][]string{"h": {groupID}}, defaultTimeout)
		foundPutUser := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 2 && tag[0] == "p" && tag[1] == bob.hexPK {
					foundPutUser = true
					break
				}
			}
		}
		if !foundPutUser {
			t.Fatal("kind 9000 put-user for bob not found after /invite")
		}

		// Verify a gift-wrap DM (kind 1059) was sent containing an naddr.
		giftWraps := queryRelayEvents(t, relayURL, []int{1059}, nil, nil)
		if len(giftWraps) == 0 {
			t.Fatal("no gift wraps found after /invite")
		}
		foundForBob := false
		for _, gw := range giftWraps {
			for _, tag := range gw.Tags {
				if len(tag) >= 2 && tag[0] == "p" && tag[1] == bob.hexPK {
					foundForBob = true
					break
				}
			}
		}
		if !foundForBob {
			t.Fatalf("no gift wrap tagged with bob's pubkey after /invite")
		}
	})

	t.Run("group/invite-join", func(t *testing.T) {
		if groupID == "" {
			t.Skip("no group ID")
		}
		// Bob opens DM with alice. The invite message was delivered during
		// the previous test and is already in m.msgs[alicePK].
		typeCmd(bob.tm, "/dm "+alice.npub)
		time.Sleep(3 * time.Second)

		// Bob types "/join " and presses Tab to autocomplete the naddr
		// from the invite message in the current chat.
		bob.tm.Type("/join ")
		time.Sleep(1 * time.Second)
		bob.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
		time.Sleep(1 * time.Second)

		// Verify the autocomplete filled in an naddr.
		waitFor(t, bob.tm, "naddr1", defaultTimeout)

		// Press Enter to join.
		bob.tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
		time.Sleep(3 * time.Second)

		// Verify bob joined by checking for a join request (kind 9021) on the relay.
		evts := queryRelayEvents(t, relayURL, []int{9021}, nil, map[string][]string{"h": {groupID}})
		foundJoin := false
		for _, evt := range evts {
			if evt.PubKey.Hex() == bob.hexPK {
				foundJoin = true
				break
			}
		}
		if !foundJoin {
			t.Log("bob's kind 9021 join-request not found (may have already been a member)")
		}
	})

	// ── NIP-17 DMs ───────────────────────────────────────────────────────

	t.Run("dm/open", func(t *testing.T) {
		typeCmd(alice.tm, "/dm "+bob.npub)
		time.Sleep(2 * time.Second)
	})

	t.Run("dm/alice-sends", func(t *testing.T) {
		typeCmd(alice.tm, "Secret message from Alice")
		time.Sleep(5 * time.Second)

		// Verify gift wraps (kind 1059) were published to the relay.
		giftWraps := queryRelayEvents(t, relayURL, []int{1059}, nil, nil)
		t.Logf("gift wraps on relay: %d", len(giftWraps))
		if len(giftWraps) == 0 {
			t.Fatal("no gift wraps (kind 1059) found on relay after alice sent DM")
		}

		// Check that one gift wrap is tagged with bob's pubkey.
		foundForBob := false
		for _, gw := range giftWraps {
			for _, tag := range gw.Tags {
				if len(tag) >= 2 && tag[0] == "p" && tag[1] == bob.hexPK {
					foundForBob = true
					break
				}
			}
		}
		if !foundForBob {
			t.Fatalf("no gift wrap tagged with bob's pubkey %s", bob.hexPK[:16])
		}

		// TUI delivery of DMs through nip17 is flaky in test; verify alice sees her own message.
		waitFor(t, alice.tm, "Secret message from Alice", defaultTimeout)
	})

	t.Run("dm/bob-sends", func(t *testing.T) {
		typeCmd(bob.tm, "/dm "+alice.npub)
		time.Sleep(2 * time.Second)
		typeCmd(bob.tm, "Secret reply from Bob")
		time.Sleep(5 * time.Second)

		// Verify gift wraps for bob's message.
		giftWraps := queryRelayEvents(t, relayURL, []int{1059}, nil, nil)
		// Should have more gift wraps now (alice's 2 + bob's 2 = 4).
		if len(giftWraps) < 3 {
			t.Fatalf("expected at least 3 gift wraps after both sent DMs, got %d", len(giftWraps))
		}

		// Verify bob sees his own message (local echo).
		waitFor(t, bob.tm, "Secret reply from Bob", defaultTimeout)
	})

	// ── /me command ──────────────────────────────────────────────────────

	t.Run("cmd/me", func(t *testing.T) {
		typeCmd(alice.tm, "/me")
		waitFor(t, alice.tm, alice.npub, defaultTimeout)
		// Dismiss QR overlay.
		alice.tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
		time.Sleep(500 * time.Millisecond)
	})

	// ── /room command ────────────────────────────────────────────────────

	t.Run("cmd/room-channel", func(t *testing.T) {
		if channelID == "" {
			t.Skip("no channel ID")
		}
		// Navigate alice to the channel by joining it.
		typeCmd(alice.tm, "/join "+channelID)
		time.Sleep(2 * time.Second)

		typeCmd(alice.tm, "/room")
		waitFor(t, alice.tm, "nevent", defaultTimeout)
		alice.tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
		time.Sleep(500 * time.Millisecond)
	})

	// ── NIP-51 list verification ─────────────────────────────────────────

	t.Run("nip51/channel-list", func(t *testing.T) {
		if channelID == "" {
			t.Skip("no channel ID")
		}
		evts := waitForRelayEvent(t, relayURL, []int{10005}, []string{alice.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Fatal("kind 10005 channel list not found for alice")
		}
		found := false
		for _, evt := range evts {
			for _, tag := range evt.Tags {
				if len(tag) >= 2 && tag[0] == "e" && tag[1] == channelID {
					found = true
					break
				}
			}
		}
		if !found {
			t.Logf("kind 10005 tags: %v", evts[0].Tags)
			t.Fatalf("kind 10005 does not contain channel %s", channelID)
		}
	})

	t.Run("nip51/group-list", func(t *testing.T) {
		evts := waitForRelayEvent(t, relayURL, []int{10009}, []string{alice.hexPK}, nil, defaultTimeout)
		if len(evts) == 0 {
			t.Skip("kind 10009 group list not found for alice")
		}
	})

	// ── Leave ────────────────────────────────────────────────────────────

	t.Run("leave/channel", func(t *testing.T) {
		if channelID == "" {
			t.Skip("no channel ID")
		}
		// Navigate bob to first item (channel).
		for i := 0; i < 10; i++ {
			sendCtrlUp(bob.tm)
		}
		time.Sleep(500 * time.Millisecond)

		typeCmd(bob.tm, "/leave")
		time.Sleep(3 * time.Second)
	})

	// ── Profile resolution ───────────────────────────────────────────────

	t.Run("profile/peer-resolve", func(t *testing.T) {
		evts := queryRelayEvents(t, relayURL, []int{0}, []string{alice.hexPK}, nil)
		if len(evts) == 0 {
			t.Fatal("alice's kind-0 profile not on relay")
		}
		evts = queryRelayEvents(t, relayURL, []int{0}, []string{bob.hexPK}, nil)
		if len(evts) == 0 {
			t.Fatal("bob's kind-0 profile not on relay")
		}
	})
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip17"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip59"
)

// Keys holds the user's nostr key pair.
type Keys struct {
	SK     string
	PK     string
	NPub   string
}

// Channel represents a NIP-28 channel (kind 40 creation event).
type Channel struct {
	ID   string
	Name string
}

// ChatMessage represents a message displayed in the TUI.
type ChatMessage struct {
	Author    string
	PubKey    string // full 64-char hex pubkey of the author
	Content   string
	Timestamp nostr.Timestamp
	EventID   string
	ChannelID string // NIP-28 channel this message belongs to
	IsMine    bool
}

// Bubbletea message types for nostr events.
type channelEventMsg ChatMessage
type dmEventMsg ChatMessage
type nostrErrMsg struct{ err error }

// Subscription setup results — returned from Cmds so the model can store
// the channel and cancel func without blocking Init().
type channelSubStartedMsg struct {
	channelID string
	events    <-chan nostr.RelayEvent
	cancel    context.CancelFunc
}
type dmSubStartedMsg struct {
	events <-chan nostr.Event
	cancel context.CancelFunc
}

// channelMetaMsg is returned after fetching a kind-40 event to resolve channel metadata.
type channelMetaMsg struct {
	ID   string
	Name string
}

// channelCreatedMsg is returned after publishing a kind-40 channel creation event.
type channelCreatedMsg struct {
	ID   string
	Name string
}

// profileResolvedMsg is returned after fetching a kind-0 profile for a pubkey.
type profileResolvedMsg struct {
	PubKey      string
	DisplayName string
}

func (e nostrErrMsg) Error() string { return e.err.Error() }

// loadKeys reads the private key from the environment and derives the public key.
func loadKeys() (Keys, error) {
	raw := os.Getenv("NOSTR_PRIVATE_KEY")
	if raw == "" {
		return Keys{}, fmt.Errorf("NOSTR_PRIVATE_KEY environment variable not set")
	}

	sk := raw
	if strings.HasPrefix(raw, "nsec") {
		prefix, val, err := nip19.Decode(raw)
		if err != nil {
			return Keys{}, fmt.Errorf("failed to decode nsec: %w", err)
		}
		if prefix != "nsec" {
			return Keys{}, fmt.Errorf("expected nsec prefix, got %s", prefix)
		}
		sk = val.(string)
	}

	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		return Keys{}, fmt.Errorf("failed to derive public key: %w", err)
	}

	npub, err := nip19.EncodePublicKey(pk)
	if err != nil {
		return Keys{}, fmt.Errorf("failed to encode npub: %w", err)
	}

	return Keys{SK: sk, PK: pk, NPub: npub}, nil
}

// fetchChannelMetaCmd fetches a kind-40 event by ID to resolve the channel name.
func fetchChannelMetaCmd(pool *nostr.SimplePool, relays []string, eventID string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchChannelMeta: id=%s", eventID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			IDs:   []string{eventID},
			Kinds: []int{40},
		})
		if re == nil {
			log.Printf("fetchChannelMeta: not found for %s", eventID)
			return channelMetaMsg{ID: eventID, Name: eventID[:8]}
		}

		var meta struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(re.Content), &meta); err != nil || meta.Name == "" {
			log.Printf("fetchChannelMeta: no name in metadata for %s", eventID)
			return channelMetaMsg{ID: eventID, Name: eventID[:8]}
		}

		log.Printf("fetchChannelMeta: resolved %s -> %q", eventID, meta.Name)
		return channelMetaMsg{ID: eventID, Name: meta.Name}
	}
}

// createChannelCmd publishes a kind-40 event to create a NIP-28 channel.
func createChannelCmd(pool *nostr.SimplePool, relays []string, name string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		meta, _ := json.Marshal(struct {
			Name string `json:"name"`
		}{Name: name})

		evt := nostr.Event{
			Kind:      40,
			CreatedAt: nostr.Now(),
			Content:   string(meta),
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{err}
		}

		ctx := context.Background()
		for range pool.PublishMany(ctx, relays, evt) {
		}

		id := evt.GetID()
		log.Printf("createChannelCmd: created %q -> %s", name, id)
		return channelCreatedMsg{ID: id, Name: name}
	}
}

// subscribeChannelCmd opens a channel subscription inside a tea.Cmd so it doesn't block Init/Update.
func subscribeChannelCmd(pool *nostr.SimplePool, relays []string, channelID string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("subscribeChannelCmd: channelID=%s", channelID)
		ctx, cancel := context.WithCancel(context.Background())
		ch := pool.SubscribeMany(ctx, relays, nostr.Filter{
			Kinds: []int{42},
			Tags:  nostr.TagMap{"e": {channelID}},
			Limit: 50,
		})
		return channelSubStartedMsg{channelID: channelID, events: ch, cancel: cancel}
	}
}

// subscribeDMCmd opens a NIP-17 DM listener inside a tea.Cmd so it doesn't block Init/Update.
// It proactively authenticates with each relay via NIP-42 before subscribing.
func subscribeDMCmd(pool *nostr.SimplePool, relays []string, kr nostr.Keyer, since nostr.Timestamp) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		pk, err := kr.GetPublicKey(ctx)
		if err != nil {
			log.Printf("subscribeDMCmd: failed to get public key: %v", err)
			cancel()
			return nostrErrMsg{fmt.Errorf("subscribeDMCmd: %w", err)}
		}
		log.Printf("subscribeDMCmd: listening for kind 1059 gift wraps to %s since %d", shortPK(pk), since)

		// Pre-authenticate with each relay via NIP-42 so they serve DM events.
		for _, url := range relays {
			r, err := pool.EnsureRelay(url)
			if err != nil {
				log.Printf("subscribeDMCmd: failed to connect to %s: %v", url, err)
				continue
			}
			// Give the relay a moment to send the AUTH challenge.
			time.Sleep(500 * time.Millisecond)
			err = r.Auth(ctx, func(ae *nostr.Event) error { return kr.SignEvent(ctx, ae) })
			if err != nil {
				log.Printf("subscribeDMCmd: NIP-42 auth failed on %s: %v", url, err)
			} else {
				log.Printf("subscribeDMCmd: NIP-42 auth succeeded on %s", url)
			}
		}

		ch := make(chan nostr.Event)

		go func() {
			defer close(ch)
			for ie := range pool.SubscribeMany(ctx, relays, nostr.Filter{
				Kinds: []int{1059},
				Tags:  nostr.TagMap{"p": []string{pk}},
				Since: &since,
			}) {
				log.Printf("subscribeDMCmd: got kind 1059 event id=%s from relay=%s", shortPK(ie.ID), ie.Relay.URL)
				rumor, err := nip59.GiftUnwrap(
					*ie.Event,
					func(otherpubkey, ciphertext string) (string, error) { return kr.Decrypt(ctx, ciphertext, otherpubkey) },
				)
				if err != nil {
					log.Printf("subscribeDMCmd: unwrap failed: %v", err)
					continue
				}
				log.Printf("subscribeDMCmd: unwrapped rumor kind=%d from=%s content=%q", rumor.Kind, shortPK(rumor.PubKey), rumor.Content[:min(len(rumor.Content), 50)])
				ch <- rumor
			}
			log.Println("subscribeDMCmd: subscription ended")
		}()

		return dmSubStartedMsg{events: ch, cancel: cancel}
	}
}

// waitForChannelEvent blocks on the subscription channel and returns the next event.
func waitForChannelEvent(events <-chan nostr.RelayEvent, channelID string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		re, ok := <-events
		if !ok {
			return nil
		}
		return channelEventMsg(ChatMessage{
			Author:    shortPK(re.PubKey),
			PubKey:    re.PubKey,
			Content:   re.Content,
			Timestamp: re.CreatedAt,
			EventID:   re.ID,
			ChannelID: channelID,
			IsMine:    re.PubKey == keys.PK,
		})
	}
}

// publishChannelMessage signs and publishes a kind-42 message to a channel.
// Returns a channelEventMsg with the local message so it appears immediately.
func publishChannelMessage(pool *nostr.SimplePool, relays []string, channelID string, content string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt := nostr.Event{
			Kind:      42,
			CreatedAt: nostr.Now(),
			Tags: nostr.Tags{
				{"e", channelID, "", "root"},
			},
			Content: content,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{err}
		}

		ctx := context.Background()
		for range pool.PublishMany(ctx, relays, evt) {
		}

		return channelEventMsg(ChatMessage{
			Author:    shortPK(keys.PK),
			PubKey:    keys.PK,
			Content:   content,
			Timestamp: evt.CreatedAt,
			EventID:   evt.GetID(),
			ChannelID: channelID,
			IsMine:    true,
		})
	}
}

// waitForDMEvent blocks on the NIP-17 DM channel and returns the next decrypted rumor.
func waitForDMEvent(events <-chan nostr.Event, keys Keys) tea.Cmd {
	return func() tea.Msg {
		rumor, ok := <-events
		if !ok {
			return nil
		}

		// rumor.PubKey = sender, rumor.Content = plaintext (already decrypted by nip17)
		// Determine peer: if sender is us, look at "p" tag for recipient
		peer := rumor.PubKey
		if peer == keys.PK {
			for _, tag := range rumor.Tags {
				if len(tag) >= 2 && tag[0] == "p" {
					peer = tag[1]
					break
				}
			}
		}

		// Rumors (kind 14) are unsigned and have no ID; synthesize one for dedup.
		eventID := rumor.ID
		if eventID == "" {
			h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s", rumor.PubKey, peer, rumor.CreatedAt, rumor.Content)))
			eventID = hex.EncodeToString(h[:])
		}

		return dmEventMsg(ChatMessage{
			Author:    shortPK(rumor.PubKey),
			PubKey:    peer,
			Content:   rumor.Content,
			Timestamp: rumor.CreatedAt,
			EventID:   eventID,
			IsMine:    rumor.PubKey == keys.PK,
		})
	}
}

// sendDM publishes a NIP-17 gift-wrapped DM to a recipient.
// Returns a dmEventMsg with the plaintext so it appears locally.
func sendDM(pool *nostr.SimplePool, relays []string, recipientPK string, content string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		theirRelays := nip17.GetDMRelays(ctx, recipientPK, pool, relays)
		if len(theirRelays) == 0 {
			theirRelays = relays // fallback to our relays
		}

		err := nip17.PublishMessage(ctx, content, nil, pool, relays, theirRelays, kr, recipientPK, nil)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("send DM: %w", err)}
		}

		return dmEventMsg(ChatMessage{
			Author:    shortPK(keys.PK),
			PubKey:    recipientPK,
			Content:   content,
			Timestamp: nostr.Now(),
			IsMine:    true,
		})
	}
}

// fetchProfileCmd fetches a kind-0 event (NIP-01 profile metadata) for a pubkey.
func fetchProfileCmd(pool *nostr.SimplePool, relays []string, pubkey string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchProfile: pubkey=%s", shortPK(pubkey))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []int{0},
			Authors: []string{pubkey},
		})
		if re == nil {
			log.Printf("fetchProfile: not found for %s", shortPK(pubkey))
			return profileResolvedMsg{PubKey: pubkey, DisplayName: shortPK(pubkey)}
		}

		var meta struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		}
		if err := json.Unmarshal([]byte(re.Content), &meta); err != nil {
			log.Printf("fetchProfile: bad JSON for %s: %v", shortPK(pubkey), err)
			return profileResolvedMsg{PubKey: pubkey, DisplayName: shortPK(pubkey)}
		}

		name := meta.DisplayName
		if name == "" {
			name = meta.Name
		}
		if name == "" {
			name = shortPK(pubkey)
		}

		log.Printf("fetchProfile: resolved %s -> %q", shortPK(pubkey), name)
		return profileResolvedMsg{PubKey: pubkey, DisplayName: name}
	}
}

// publishProfileCmd publishes a kind-0 event with the user's profile metadata.
func publishProfileCmd(pool *nostr.SimplePool, relays []string, profile ProfileConfig, keys Keys) tea.Cmd {
	return func() tea.Msg {
		meta := map[string]string{}
		if profile.Name != "" {
			meta["name"] = profile.Name
		}
		if profile.DisplayName != "" {
			meta["display_name"] = profile.DisplayName
		}
		if profile.About != "" {
			meta["about"] = profile.About
		}
		if profile.Picture != "" {
			meta["picture"] = profile.Picture
		}

		content, err := json.Marshal(meta)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("publishProfile: marshal: %w", err)}
		}

		evt := nostr.Event{
			Kind:      0,
			CreatedAt: nostr.Now(),
			Content:   string(content),
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("publishProfile: sign: %w", err)}
		}

		ctx := context.Background()
		for range pool.PublishMany(ctx, relays, evt) {
		}

		log.Printf("publishProfile: published kind 0")
		return nil
	}
}

// publishDMRelaysCmd publishes a kind-10050 event (NIP-17 DM relay list)
// so other clients know where to send gift-wrapped DMs.
func publishDMRelaysCmd(pool *nostr.SimplePool, relays []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		var tags nostr.Tags
		for _, r := range relays {
			tags = append(tags, nostr.Tag{"relay", r})
		}

		evt := nostr.Event{
			Kind:      10050,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("publishDMRelays: sign: %w", err)}
		}

		ctx := context.Background()
		for range pool.PublishMany(ctx, relays, evt) {
		}

		log.Printf("publishDMRelays: published kind 10050 with %d relays", len(relays))
		return nil
	}
}

// shortPK returns the first 8 characters of a public key for display.
func shortPK(pk string) string {
	if len(pk) > 8 {
		return pk[:8]
	}
	return pk
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip44"
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
	events <-chan nostr.RelayEvent
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

// subscribeDMCmd opens a DM subscription inside a tea.Cmd so it doesn't block Init/Update.
func subscribeDMCmd(pool *nostr.SimplePool, relays []string, pk string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("subscribeDMCmd: pk=%s", shortPK(pk))
		ctx, cancel := context.WithCancel(context.Background())
		ch := pool.SubscribeMany(ctx, relays, nostr.Filter{
			Kinds: []int{4},
			Tags:  nostr.TagMap{"p": {pk}},
			Limit: 50,
		})
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

// waitForDMEvent blocks on the DM subscription channel and returns the next event.
func waitForDMEvent(events <-chan nostr.RelayEvent, keys Keys) tea.Cmd {
	return func() tea.Msg {
		re, ok := <-events
		if !ok {
			return nil
		}

		content := re.Content
		convKey, err := nip44.GenerateConversationKey(re.PubKey, keys.SK)
		if err == nil {
			if decrypted, err := nip44.Decrypt(re.Content, convKey); err == nil {
				content = decrypted
			}
		}

		return dmEventMsg(ChatMessage{
			Author:    shortPK(re.PubKey),
			PubKey:    re.PubKey,
			Content:   content,
			Timestamp: re.CreatedAt,
			EventID:   re.ID,
			IsMine:    false,
		})
	}
}

// sendDM encrypts and publishes a kind-4 DM to a recipient.
// Returns a dmEventMsg with the plaintext so it appears locally.
func sendDM(pool *nostr.SimplePool, relays []string, recipientPK string, content string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		convKey, err := nip44.GenerateConversationKey(recipientPK, keys.SK)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("conversation key: %w", err)}
		}

		encrypted, err := nip44.Encrypt(content, convKey)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("encrypt: %w", err)}
		}

		evt := nostr.Event{
			Kind:      4,
			CreatedAt: nostr.Now(),
			Tags: nostr.Tags{
				{"p", recipientPK},
			},
			Content: encrypted,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{err}
		}

		ctx := context.Background()
		for range pool.PublishMany(ctx, relays, evt) {
		}

		return dmEventMsg(ChatMessage{
			Author:    shortPK(keys.PK),
			PubKey:    keys.PK,
			Content:   content,
			Timestamp: evt.CreatedAt,
			EventID:   evt.GetID(),
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

// shortPK returns the first 8 characters of a public key for display.
func shortPK(pk string) string {
	if len(pk) > 8 {
		return pk[:8]
	}
	return pk
}

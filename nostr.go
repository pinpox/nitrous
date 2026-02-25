package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip17"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip29"
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
	GroupKey  string // NIP-29 group key "relay_url\tgroup_id" (empty for channels/DMs)
	IsMine    bool
}

// Bubbletea message types for nostr events.
type channelEventMsg ChatMessage
type dmEventMsg ChatMessage
type nostrErrMsg struct{ err error }

// Subscription-ended messages — trigger reconnection.
type dmSubEndedMsg struct{}
type channelSubEndedMsg struct{ channelID string }

// Reconnection delay messages — dispatched after a brief pause.
type dmReconnectMsg struct{}
type channelReconnectMsg struct{ channelID string }

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

// loadKeys reads the private key from a file (if configured) or the
// NOSTR_PRIVATE_KEY environment variable, and derives the public key.
func loadKeys(cfg Config) (Keys, error) {
	var raw string
	if cfg.PrivateKeyFile != "" {
		path := cfg.PrivateKeyFile
		// Expand ~ to home directory.
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Keys{}, fmt.Errorf("failed to read private_key_file %q: %w", path, err)
		}
		raw = strings.TrimSpace(string(data))
	}
	if raw == "" {
		raw = os.Getenv("NOSTR_PRIVATE_KEY")
	}
	if raw == "" {
		return Keys{}, fmt.Errorf("no private key: set private_key_file in config or NOSTR_PRIVATE_KEY env var")
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
			return channelMetaMsg{ID: eventID, Name: shortPK(eventID)}
		}

		var meta struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(re.Content), &meta); err != nil || meta.Name == "" {
			log.Printf("fetchChannelMeta: no name in metadata for %s", eventID)
			return channelMetaMsg{ID: eventID, Name: shortPK(eventID)}
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

// dmReconnectDelayCmd waits briefly before signalling a DM reconnection.
func dmReconnectDelayCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return dmReconnectMsg{}
	}
}

// channelReconnectDelayCmd waits briefly before signalling a channel reconnection.
func channelReconnectDelayCmd(channelID string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return channelReconnectMsg{channelID: channelID}
	}
}

// subscribeDMCmd opens a NIP-17 DM listener inside a tea.Cmd so it doesn't block Init/Update.
// NIP-42 auth is handled by the pool's WithAuthHandler; we pre-connect to each relay
// and wait briefly so the AUTH handshake completes before subscribing.
func subscribeDMCmd(pool *nostr.SimplePool, relays []string, kr nostr.Keyer, since nostr.Timestamp) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		pk, err := kr.GetPublicKey(ctx)
		if err != nil {
			log.Printf("subscribeDMCmd: failed to get public key: %v", err)
			cancel()
			return nostrErrMsg{fmt.Errorf("subscribeDMCmd: %w", err)}
		}
		// NIP-59 gift wraps use randomized created_at timestamps (up to ±2 days)
		// to thwart time-analysis attacks. Subtract 3 days from the since filter
		// so we don't miss events whose outer timestamp is in the past.
		// The seenEvents dedup map handles any duplicates.
		adjustedSince := since - 259200 // 3 days
		if adjustedSince < 0 {
			adjustedSince = 0
		}
		log.Printf("subscribeDMCmd: listening for kind 1059 gift wraps to %s since %d (adjusted from %d)", shortPK(pk), adjustedSince, since)

		// Pre-authenticate with each relay via NIP-42 in the background.
		// Some relays send an AUTH challenge on connect; r.Auth() reads it and responds.
		// We don't wait for completion — the subscription starts immediately and the
		// pool's WithAuthHandler handles any late AUTH challenges reactively.
		for _, url := range relays {
			go func(url string) {
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("subscribeDMCmd: failed to connect to %s: %v", url, err)
					return
				}
				time.Sleep(500 * time.Millisecond)
				authCtx, authCancel := context.WithTimeout(ctx, 3*time.Second)
				err = r.Auth(authCtx, func(ae *nostr.Event) error { return kr.SignEvent(authCtx, ae) })
				authCancel()
				if err != nil {
					log.Printf("subscribeDMCmd: NIP-42 auth on %s returned: %v (may still succeed relay-side)", url, err)
				} else {
					log.Printf("subscribeDMCmd: NIP-42 auth succeeded on %s", url)
				}
			}(url)
		}

		ch := make(chan nostr.Event)

		go func() {
			defer close(ch)
			for ie := range pool.SubscribeMany(ctx, relays, nostr.Filter{
				Kinds: []int{1059},
				Tags:  nostr.TagMap{"p": []string{pk}},
				Since: &adjustedSince,
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
			return channelSubEndedMsg{channelID: channelID}
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
			return dmSubEndedMsg{}
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		theirRelays := nip17.GetDMRelays(ctx, recipientPK, pool, relays)
		if len(theirRelays) == 0 {
			theirRelays = relays // fallback to our relays
		}

		toUs, toThem, err := nip17.PrepareMessage(ctx, content, nil, kr, recipientPK, nil)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("send DM: prepare: %w", err)}
		}

		// Publish to all relays concurrently. A dead relay won't block the rest.
		var wg sync.WaitGroup
		var sentToUs, sentToThem atomic.Bool

		// "to us" copy → our relays
		for _, url := range relays {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("sendDM: connect %s: %v", url, err)
					return
				}
				if err := r.Publish(ctx, toUs); err != nil {
					log.Printf("sendDM: publish toUs to %s: %v", url, err)
					return
				}
				sentToUs.Store(true)
				log.Printf("sendDM: published toUs to %s", url)
			}(url)
		}

		// "to them" copy → their relays
		for _, url := range theirRelays {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("sendDM: connect %s: %v", url, err)
					return
				}
				if err := r.Publish(ctx, toThem); err != nil {
					log.Printf("sendDM: publish toThem to %s: %v", url, err)
					return
				}
				sentToThem.Store(true)
				log.Printf("sendDM: published toThem to %s", url)
			}(url)
		}

		wg.Wait()

		if !sentToUs.Load() && !sentToThem.Load() {
			return nostrErrMsg{fmt.Errorf("send DM: failed to publish to any relay")}
		}
		if !sentToThem.Load() {
			log.Printf("sendDM: warning: could not deliver to recipient's relays %v", theirRelays)
		}

		ts := nostr.Now()
		h := sha256.Sum256([]byte(fmt.Sprintf("local:%s:%s:%d:%s", keys.PK, recipientPK, ts, content)))
		return dmEventMsg(ChatMessage{
			Author:    shortPK(keys.PK),
			PubKey:    recipientPK,
			Content:   content,
			Timestamp: ts,
			EventID:   hex.EncodeToString(h[:]),
			IsMine:    true,
		})
	}
}

// getPeerRelays fetches the NIP-65 relay list (kind 10002) for a pubkey
// and returns the write relay URLs. Falls back to nil if not found.
func getPeerRelays(pool *nostr.SimplePool, relays []string, pubkey string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	re := pool.QuerySingle(ctx, relays, nostr.Filter{
		Kinds:   []int{10002},
		Authors: []string{pubkey},
	})
	if re == nil {
		return nil
	}

	var urls []string
	for _, tag := range re.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		log.Printf("getPeerRelays: %s tag: %v", shortPK(pubkey), tag)
		urls = append(urls, tag[1])
	}
	log.Printf("getPeerRelays: %s -> %v", shortPK(pubkey), urls)
	return urls
}

// fetchProfileCmd fetches a kind-0 event (NIP-01 profile metadata) for a pubkey.
// If not found on the user's relays, looks up the peer's NIP-65 relay list
// and tries their write relays.
func fetchProfileCmd(pool *nostr.SimplePool, relays []string, pubkey string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchProfile: pubkey=%s", shortPK(pubkey))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []int{0},
			Authors: []string{pubkey},
		})

		// If not found locally, check the peer's NIP-65 relay list for their write relays.
		if re == nil {
			peerRelays := getPeerRelays(pool, relays, pubkey)
			if len(peerRelays) > 0 {
				log.Printf("fetchProfile: not on local relays, trying %d peer relays for %s", len(peerRelays), shortPK(pubkey))
				re = pool.QuerySingle(ctx, peerRelays, nostr.Filter{
					Kinds:   []int{0},
					Authors: []string{pubkey},
				})
			}
		}

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

// --- NIP-29 Relay-Based Groups ---

// groupKey builds a map key from relay URL and group ID.
func groupKey(relayURL, groupID string) string {
	return relayURL + "\t" + groupID
}

// splitGroupKey extracts the relay URL and group ID from a group key.
func splitGroupKey(gk string) (relayURL, groupID string) {
	parts := strings.SplitN(gk, "\t", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", gk
}

// Bubbletea message types for NIP-29 group events.
type groupEventMsg ChatMessage
type groupSubStartedMsg struct {
	groupKey string
	events   <-chan nostr.RelayEvent
	cancel   context.CancelFunc
}
type groupSubEndedMsg struct{ groupKey string }
type groupReconnectMsg struct{ groupKey string }
type groupMetaMsg struct {
	RelayURL    string
	GroupID     string
	Name        string
	RelayPubKey string // pubkey of the relay (author of kind 39000)
	FromSub     bool   // true when received via the group subscription, false from edit commands
}
type groupJoinedMsg struct {
	RelayURL string
	GroupID  string
	Name     string
}

// subscribeGroupCmd opens a subscription on a single relay for a NIP-29 group.
// Subscribes to both kind 9 (chat messages) and kind 39000 (metadata) using
// separate filters since they use different tag keys ("h" vs "d").
func subscribeGroupCmd(pool *nostr.SimplePool, relayURL, groupID string) tea.Cmd {
	return func() tea.Msg {
		gk := groupKey(relayURL, groupID)
		log.Printf("subscribeGroupCmd: relay=%s group=%s", relayURL, groupID)
		ctx, cancel := context.WithCancel(context.Background())
		ch := pool.SubMany(ctx, []string{relayURL}, nostr.Filters{
			{
				Kinds: []int{nostr.KindSimpleGroupChatMessage},
				Tags:  nostr.TagMap{"h": {groupID}},
				Limit: 50,
			},
			{
				Kinds: []int{nostr.KindSimpleGroupMetadata},
				Tags:  nostr.TagMap{"d": {groupID}},
				Limit: 1,
			},
		})
		return groupSubStartedMsg{groupKey: gk, events: ch, cancel: cancel}
	}
}

// waitForGroupEvent blocks on the group subscription channel and returns the next event.
// Returns groupMetaMsg for kind 39000 metadata events and groupEventMsg for chat messages.
func waitForGroupEvent(events <-chan nostr.RelayEvent, gk string, relayURL string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		re, ok := <-events
		if !ok {
			return groupSubEndedMsg{groupKey: gk}
		}

		// Handle metadata events (kind 39000) — extract group name from tags.
		if re.Kind == nostr.KindSimpleGroupMetadata {
			groupID := ""
			name := ""
			for _, tag := range re.Tags {
				if len(tag) >= 2 {
					switch tag[0] {
					case "d":
						groupID = tag[1]
					case "name":
						name = tag[1]
					}
				}
			}
			if name != "" && groupID != "" {
				log.Printf("waitForGroupEvent: got metadata for group %s: name=%q", groupID, name)
				return groupMetaMsg{RelayURL: relayURL, GroupID: groupID, Name: name, RelayPubKey: re.PubKey, FromSub: true}
			}
			log.Printf("waitForGroupEvent: got metadata event but no usable name, skipping")
			// Re-wait for next event rather than surfacing an empty metadata msg.
			return waitForGroupEvent(events, gk, relayURL, keys)()
		}

		return groupEventMsg(ChatMessage{
			Author:    shortPK(re.PubKey),
			PubKey:    re.PubKey,
			Content:   re.Content,
			Timestamp: re.CreatedAt,
			EventID:   re.ID,
			GroupKey:  gk,
			IsMine:    re.PubKey == keys.PK,
		})
	}
}

// publishGroupMessage signs and publishes a kind-9 message to a NIP-29 group.
func publishGroupMessage(pool *nostr.SimplePool, relayURL, groupID, content string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		gk := groupKey(relayURL, groupID)
		tags := nostr.Tags{{"h", groupID}}
		tags = append(tags, pickPreviousTags(previousIDs)...)
		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupChatMessage,
			CreatedAt: nostr.Now(),
			Tags:      tags,
			Content:   content,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{err}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group publish: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("group publish: %w", err)}
		}

		return groupEventMsg(ChatMessage{
			Author:    shortPK(keys.PK),
			PubKey:    keys.PK,
			Content:   content,
			Timestamp: evt.CreatedAt,
			EventID:   evt.GetID(),
			GroupKey:  gk,
			IsMine:    true,
		})
	}
}

// joinGroupCmd publishes a kind-9021 join request for a NIP-29 group.
func joinGroupCmd(pool *nostr.SimplePool, relayURL, groupID string, previousIDs []string, inviteCode string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		tags := nostr.Tags{{"h", groupID}}
		if inviteCode != "" {
			tags = append(tags, nostr.Tag{"code", inviteCode})
		}
		tags = append(tags, pickPreviousTags(previousIDs)...)
		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupJoinRequest,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("group join: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group join: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("group join: publish: %w", err)}
		}

		log.Printf("joinGroupCmd: sent kind 9021 to %s for group %s", relayURL, groupID)
		return groupJoinedMsg{RelayURL: relayURL, GroupID: groupID}
	}
}

// leaveGroupCmd publishes a kind-9022 leave request for a NIP-29 group.
func leaveGroupCmd(pool *nostr.SimplePool, relayURL, groupID string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		tags := nostr.Tags{{"h", groupID}}
		tags = append(tags, pickPreviousTags(previousIDs)...)
		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupLeaveRequest,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("group leave: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group leave: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			log.Printf("leaveGroupCmd: publish failed (may already be left): %v", err)
		}
		log.Printf("leaveGroupCmd: sent kind 9022 to %s for group %s", relayURL, groupID)
		return nil
	}
}

// fetchGroupMetaCmd fetches a kind-39000 event to resolve the group name.
func fetchGroupMetaCmd(pool *nostr.SimplePool, relayURL, groupID string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchGroupMeta: relay=%s group=%s", relayURL, groupID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		re := pool.QuerySingle(ctx, []string{relayURL}, nostr.Filter{
			Kinds: []int{nostr.KindSimpleGroupMetadata},
			Tags:  nostr.TagMap{"d": {groupID}},
		})
		if re == nil {
			log.Printf("fetchGroupMeta: not found for %s on %s", groupID, relayURL)
			return nil
		}

		g, err := nip29.NewGroupFromMetadataEvent(relayURL, re.Event)
		if err != nil {
			log.Printf("fetchGroupMeta: merge error: %v", err)
			return nil
		}
		name := g.Name
		if name == "" {
			// Metadata event exists but has no name field; don't overwrite.
			return nil
		}
		log.Printf("fetchGroupMeta: resolved %s -> %q", groupID, name)
		return groupMetaMsg{RelayURL: relayURL, GroupID: groupID, Name: name, RelayPubKey: re.PubKey}
	}
}

// groupReconnectDelayCmd waits briefly before signalling a group reconnection.
func groupReconnectDelayCmd(gk string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return groupReconnectMsg{groupKey: gk}
	}
}

// parseGroupInput parses a NIP-29 group address from user input.
// Accepts "naddr1..." or "host'groupid" format.
// Returns relayURL, groupID, or an error.
func parseGroupInput(input string) (string, string, error) {
	if strings.HasPrefix(input, "naddr") {
		prefix, data, err := nip19.Decode(input)
		if err != nil {
			return "", "", fmt.Errorf("invalid naddr: %w", err)
		}
		if prefix != "naddr" {
			return "", "", fmt.Errorf("expected naddr, got %s", prefix)
		}
		ep := data.(nostr.EntityPointer)
		if len(ep.Relays) == 0 {
			return "", "", fmt.Errorf("naddr has no relay")
		}
		return ep.Relays[0], ep.Identifier, nil
	}

	// Try host'groupid format
	ga, err := nip29.ParseGroupAddress(input)
	if err != nil {
		return "", "", fmt.Errorf("invalid group address: %w", err)
	}
	return ga.Relay, ga.ID, nil
}

// pickPreviousTags selects up to 3 random IDs from the recent event list
// and returns NIP-29 "previous" tags (first 8 chars of each ID).
func pickPreviousTags(ids []string) nostr.Tags {
	if len(ids) == 0 {
		return nil
	}
	n := 3
	if len(ids) < n {
		n = len(ids)
	}
	// Fisher-Yates partial shuffle to pick n random entries.
	picked := make([]string, len(ids))
	copy(picked, ids)
	for i := len(picked) - 1; i > 0 && i >= len(picked)-n; i-- {
		jBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			continue
		}
		j := int(jBig.Int64())
		picked[i], picked[j] = picked[j], picked[i]
	}
	var tags nostr.Tags
	for _, id := range picked[len(picked)-n:] {
		ref := id
		if len(ref) > 8 {
			ref = ref[:8]
		}
		tags = append(tags, nostr.Tag{"previous", ref})
	}
	return tags
}

// groupCreatedMsg is returned after publishing a kind 9007 group creation event.
type groupCreatedMsg struct {
	RelayURL string
	GroupID  string
	Name     string
}

// groupInviteCreatedMsg is returned after publishing a kind 9009 invite event.
type groupInviteCreatedMsg struct {
	RelayURL string
	GroupID  string
	Code     string
}

// createGroupCmd publishes a kind 9007 event to create a NIP-29 group on a relay.
func createGroupCmd(pool *nostr.SimplePool, relayURL, name string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		// Generate random 8-hex-char group ID.
		idBytes := make([]byte, 4)
		if _, err := rand.Read(idBytes); err != nil {
			return nostrErrMsg{fmt.Errorf("create group: random: %w", err)}
		}
		groupID := hex.EncodeToString(idBytes)

		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupCreateGroup,
			CreatedAt: nostr.Now(),
			Tags:      nostr.Tags{{"h", groupID}, {"name", name}},
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("create group: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("create group: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("create group: publish: %w", err)}
		}

		log.Printf("createGroupCmd: created ~%s (%s) on %s", name, groupID, relayURL)
		return groupCreatedMsg{RelayURL: relayURL, GroupID: groupID, Name: name}
	}
}

// deleteGroupEventCmd publishes a kind 9005 event to delete an event from a NIP-29 group.
func deleteGroupEventCmd(pool *nostr.SimplePool, relayURL, groupID, eventID string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		tags := nostr.Tags{{"h", groupID}, {"e", eventID}}
		tags = append(tags, pickPreviousTags(previousIDs)...)

		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupDeleteEvent,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("delete event: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("delete event: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("delete event: publish: %w", err)}
		}

		log.Printf("deleteGroupEventCmd: deleted %s from group %s on %s", eventID, groupID, relayURL)
		return nil
	}
}

// createGroupInviteCmd publishes a kind 9009 event to create an invite for a NIP-29 group.
func createGroupInviteCmd(pool *nostr.SimplePool, relayURL, groupID string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		tags := nostr.Tags{{"h", groupID}}
		tags = append(tags, pickPreviousTags(previousIDs)...)

		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupCreateInvite,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("create invite: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("create invite: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("create invite: publish: %w", err)}
		}

		// The invite code is typically returned as the event content by the relay.
		code := evt.Content
		if code == "" {
			code = shortPK(evt.GetID())
		}

		log.Printf("createGroupInviteCmd: invite for group %s on %s: %s", groupID, relayURL, code)
		return groupInviteCreatedMsg{RelayURL: relayURL, GroupID: groupID, Code: code}
	}
}

// inviteDMCmd fetches the group's kind 39000 metadata to get the relay pubkey,
// encodes a proper naddr, and sends a DM with the invite link.
func inviteDMCmd(pool *nostr.SimplePool, relays []string, relayURL, groupID, groupName, recipientPK string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		// Fetch the kind 39000 event to get the relay's pubkey for the naddr.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		author := keys.PK // fallback
		re := pool.QuerySingle(ctx, []string{relayURL}, nostr.Filter{
			Kinds: []int{nostr.KindSimpleGroupMetadata},
			Tags:  nostr.TagMap{"d": {groupID}},
		})
		if re != nil && re.PubKey != "" {
			author = re.PubKey
		}

		naddr, err := nip19.EncodeEntity(author, nostr.KindSimpleGroupMetadata, groupID, []string{relayURL})
		if err != nil {
			return nostrErrMsg{fmt.Errorf("invite: encode naddr: %w", err)}
		}

		// Strip wss:// prefix for the host'groupid format.
		host := strings.TrimPrefix(relayURL, "wss://")
		dmText := fmt.Sprintf("You've been invited to ~%s\n\nnostr:%s\n\n%s'%s", groupName, naddr, host, groupID)
		// Reuse sendDM logic inline — call the returned Cmd directly.
		return sendDM(pool, relays, recipientPK, dmText, keys, kr)()
	}
}

// putUserCmd publishes a kind 9000 event to add a user to a NIP-29 group.
func putUserCmd(pool *nostr.SimplePool, relayURL, groupID, pubkey string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		tags := nostr.Tags{{"h", groupID}, {"p", pubkey}}
		tags = append(tags, pickPreviousTags(previousIDs)...)

		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupPutUser,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("put user: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("put user: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("put user: publish: %w", err)}
		}

		log.Printf("putUserCmd: added %s to group %s on %s", shortPK(pubkey), groupID, relayURL)
		return nil
	}
}

// editGroupMetadataCmd publishes a kind 9002 event to edit group metadata.
func editGroupMetadataCmd(pool *nostr.SimplePool, relayURL, groupID string, fields map[string]string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		tags := nostr.Tags{{"h", groupID}}
		for k, v := range fields {
			if v == "" {
				tags = append(tags, nostr.Tag{k})
			} else {
				tags = append(tags, nostr.Tag{k, v})
			}
		}
		tags = append(tags, pickPreviousTags(previousIDs)...)

		evt := nostr.Event{
			Kind:      nostr.KindSimpleGroupEditMetadata,
			CreatedAt: nostr.Now(),
			Tags:      tags,
		}
		if err := evt.Sign(keys.SK); err != nil {
			return nostrErrMsg{fmt.Errorf("edit metadata: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("edit metadata: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("edit metadata: publish: %w", err)}
		}

		// Return the new name for the UI to update.
		name := fields["name"]
		if name == "" {
			name = groupID
		}
		log.Printf("editGroupMetadataCmd: updated metadata for group %s on %s", groupID, relayURL)
		return groupMetaMsg{RelayURL: relayURL, GroupID: groupID, Name: name}
	}
}

// nip05ResolvedMsg is sent when a NIP-05 identifier lookup completes.
type nip05ResolvedMsg struct {
	Identifier string // original input e.g. "alice@example.com"
	PubKey     string // resolved hex pubkey, empty on failure
	Err        error
}

// resolveNIP05Cmd resolves a NIP-05 internet identifier to a hex pubkey.
func resolveNIP05Cmd(identifier string) tea.Cmd {
	return func() tea.Msg {
		parts := strings.SplitN(identifier, "@", 2)
		if len(parts) != 2 {
			return nip05ResolvedMsg{Identifier: identifier, Err: fmt.Errorf("invalid NIP-05 identifier: %s", identifier)}
		}
		name, domain := parts[0], parts[1]

		url := fmt.Sprintf("https://%s/.well-known/nostr.json?name=%s", domain, name)
		log.Printf("resolveNIP05: fetching %s", url)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nip05ResolvedMsg{Identifier: identifier, Err: err}
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nip05ResolvedMsg{Identifier: identifier, Err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return nip05ResolvedMsg{Identifier: identifier, Err: fmt.Errorf("HTTP %d from %s", resp.StatusCode, domain)}
		}

		var result struct {
			Names map[string]string `json:"names"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nip05ResolvedMsg{Identifier: identifier, Err: fmt.Errorf("bad JSON from %s: %v", domain, err)}
		}

		pk, ok := result.Names[name]
		if !ok {
			return nip05ResolvedMsg{Identifier: identifier, Err: fmt.Errorf("name %q not found on %s", name, domain)}
		}

		log.Printf("resolveNIP05: %s -> %s", identifier, shortPK(pk))
		return nip05ResolvedMsg{Identifier: identifier, PubKey: pk}
	}
}

// shortPK returns the first 8 characters of a public key for display.
func shortPK(pk string) string {
	if len(pk) > 8 {
		return pk[:8]
	}
	return pk
}

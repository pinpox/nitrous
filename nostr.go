package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	Content   string
	Timestamp nostr.Timestamp
	EventID   string
	IsMine    bool
}

// Bubbletea message types for nostr events.
type channelsLoadedMsg []Channel
type channelEventMsg ChatMessage
type dmEventMsg ChatMessage
type publishedMsg struct{}
type nostrErrMsg struct{ err error }

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

// fetchChannels loads the list of NIP-28 channels from relays.
func fetchChannels(pool *nostr.SimplePool, relays []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch := pool.SubManyEose(ctx, relays, nostr.Filters{
			{Kinds: []int{40}, Limit: 100},
		})

		var channels []Channel
		for re := range ch {
			var meta struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(re.Content), &meta); err != nil {
				continue
			}
			name := meta.Name
			if name == "" {
				name = re.ID[:8]
			}
			channels = append(channels, Channel{
				ID:   re.ID,
				Name: name,
			})
		}

		return channelsLoadedMsg(channels)
	}
}

// waitForChannelEvent blocks on the subscription channel and returns the next event.
func waitForChannelEvent(events <-chan nostr.RelayEvent, keys Keys) tea.Cmd {
	return func() tea.Msg {
		re, ok := <-events
		if !ok {
			return nil
		}
		return channelEventMsg(ChatMessage{
			Author:    shortPK(re.PubKey),
			Content:   re.Content,
			Timestamp: re.CreatedAt,
			EventID:   re.ID,
			IsMine:    re.PubKey == keys.PK,
		})
	}
}

// publishChannelMessage signs and publishes a kind-42 message to a channel.
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
			// drain results
		}
		return publishedMsg{}
	}
}

// createChannel publishes a kind-40 channel creation event.
func createChannel(pool *nostr.SimplePool, relays []string, name string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		meta, _ := json.Marshal(map[string]string{"name": name})
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

		// Return the new channel so we can add it to the list.
		return channelsLoadedMsg{
			{ID: evt.GetID(), Name: name},
		}
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
			Content:   content,
			Timestamp: re.CreatedAt,
			EventID:   re.ID,
			IsMine:    false,
		})
	}
}

// sendDM encrypts and publishes a kind-4 DM to a recipient.
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
		return publishedMsg{}
	}
}

// shortPK returns the first 8 characters of a public key for display.
func shortPK(pk string) string {
	if len(pk) > 8 {
		return pk[:8]
	}
	return pk
}

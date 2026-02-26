package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
)

// Channel represents a NIP-28 channel (kind 40 creation event).
type Channel struct {
	ID   string
	Name string
}

// Bubbletea message types for NIP-28 channel events.
type channelEventMsg ChatMessage

// Subscription-ended message — triggers reconnection.
type channelSubEndedMsg struct{ channelID string }

// Reconnection delay message — dispatched after a brief pause.
type channelReconnectMsg struct{ channelID string }

// Subscription setup result — returned from Cmds so the model can store
// the channel and cancel func without blocking Init().
type channelSubStartedMsg struct {
	channelID string
	events    <-chan nostr.RelayEvent
	cancel    context.CancelFunc
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

// fetchChannelMetaCmd fetches a kind-40 event by ID to resolve the channel name.
func fetchChannelMetaCmd(pool *nostr.Pool, relays []string, eventID string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchChannelMeta: id=%s", eventID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		id, err := nostr.IDFromHex(eventID)
		if err != nil {
			log.Printf("fetchChannelMeta: invalid event ID %s: %v", eventID, err)
			return channelMetaMsg{ID: eventID, Name: shortPK(eventID)}
		}

		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			IDs:   []nostr.ID{id},
			Kinds: []nostr.Kind{nostr.KindChannelCreation},
		}, nostr.SubscriptionOptions{})
		if re == nil {
			log.Printf("fetchChannelMeta: not found for %s", eventID)
			return channelMetaMsg{ID: eventID, Name: shortPK(eventID)}
		}

		name := parseChannelMeta(re.Content)
		if name == "" {
			log.Printf("fetchChannelMeta: no name in metadata for %s", eventID)
			return channelMetaMsg{ID: eventID, Name: shortPK(eventID)}
		}

		log.Printf("fetchChannelMeta: resolved %s -> %q", eventID, name)
		return channelMetaMsg{ID: eventID, Name: name}
	}
}

// buildCreateChannelEvent builds a kind-40 event to create a NIP-28 channel.
func buildCreateChannelEvent(name string, keys Keys) (nostr.Event, error) {
	meta, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return nostr.Event{}, fmt.Errorf("marshal channel meta: %w", err)
	}

	evt := nostr.Event{
		Kind:      nostr.KindChannelCreation,
		CreatedAt: nostr.Now(),
		Content:   string(meta),
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// createChannelCmd publishes a kind-40 event to create a NIP-28 channel.
func createChannelCmd(pool *nostr.Pool, relays []string, name string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		log.Printf("createChannelCmd: starting for %q", name)
		evt, err := buildCreateChannelEvent(name, keys)
		if err != nil {
			return nostrErrMsg{err}
		}

		// Fire and forget — don't block the UI waiting for slow relays.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
		}()

		id := evt.GetID()
		log.Printf("createChannelCmd: created %q -> %s", name, id.Hex())
		return channelCreatedMsg{ID: id.Hex(), Name: name}
	}
}

// subscribeChannelCmd opens a channel subscription inside a tea.Cmd so it doesn't block Init/Update.
func subscribeChannelCmd(pool *nostr.Pool, relays []string, channelID string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("subscribeChannelCmd: channelID=%s", channelID)
		ctx, cancel := context.WithCancel(context.Background())
		ch := pool.SubscribeMany(ctx, relays, nostr.Filter{
			Kinds: []nostr.Kind{nostr.KindChannelMessage},
			Tags:  nostr.TagMap{"e": {channelID}},
			Limit: 50,
		}, nostr.SubscriptionOptions{})
		return channelSubStartedMsg{channelID: channelID, events: ch, cancel: cancel}
	}
}

// channelReconnectDelayCmd waits briefly before signalling a channel reconnection.
func channelReconnectDelayCmd(channelID string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return channelReconnectMsg{channelID: channelID}
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
			Author:    shortPK(re.PubKey.Hex()),
			PubKey:    re.PubKey.Hex(),
			Content:   re.Content,
			Timestamp: re.CreatedAt,
			EventID:   re.ID.Hex(),
			ChannelID: channelID,
			IsMine:    re.PubKey == keys.PK,
		})
	}
}

// buildChannelMessageEvent builds a kind-42 message event for a NIP-28 channel.
func buildChannelMessageEvent(channelID, content string, keys Keys) (nostr.Event, error) {
	evt := nostr.Event{
		Kind:      nostr.KindChannelMessage,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", channelID, "", "root"},
		},
		Content: content,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// publishChannelMessage signs and publishes a kind-42 message to a channel.
// Returns a channelEventMsg with the local message so it appears immediately.
func publishChannelMessage(pool *nostr.Pool, relays []string, channelID string, content string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildChannelMessageEvent(channelID, content, keys)
		if err != nil {
			return nostrErrMsg{err}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
		}()

		return channelEventMsg(ChatMessage{
			Author:    shortPK(keys.PK.Hex()),
			PubKey:    keys.PK.Hex(),
			Content:   content,
			Timestamp: evt.CreatedAt,
			EventID:   evt.GetID().Hex(),
			ChannelID: channelID,
			IsMine:    true,
		})
	}
}

// parseChannelMeta extracts a channel name from a kind-40 channel JSON content string.
func parseChannelMeta(content string) string {
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(content), &meta); err != nil {
		return ""
	}
	return meta.Name
}

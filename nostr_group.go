package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip29"
)

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

// subscribeGroupCmd opens a subscription on a single relay for a NIP-29 group.
// Subscribes to both kind 9 (chat messages) and kind 39000 (metadata) using
// two separate subscriptions merged into one channel (the new library takes
// a single filter per SubscribeMany call).
func subscribeGroupCmd(pool *nostr.Pool, relayURL, groupID string) tea.Cmd {
	return func() tea.Msg {
		gk := groupKey(relayURL, groupID)
		log.Printf("subscribeGroupCmd: relay=%s group=%s", relayURL, groupID)
		ctx, cancel := context.WithCancel(context.Background())
		merged := make(chan nostr.RelayEvent)

		var wg sync.WaitGroup
		wg.Add(2)

		// Chat messages (kind 9)
		go func() {
			defer wg.Done()
			for re := range pool.SubscribeMany(ctx, []string{relayURL}, nostr.Filter{
				Kinds: []nostr.Kind{nostr.KindSimpleGroupChatMessage},
				Tags:  nostr.TagMap{"h": {groupID}},
				Limit: 50,
			}, nostr.SubscriptionOptions{}) {
				merged <- re
			}
		}()

		// Metadata (kind 39000)
		go func() {
			defer wg.Done()
			for re := range pool.SubscribeMany(ctx, []string{relayURL}, nostr.Filter{
				Kinds: []nostr.Kind{nostr.KindSimpleGroupMetadata},
				Tags:  nostr.TagMap{"d": {groupID}},
				Limit: 1,
			}, nostr.SubscriptionOptions{}) {
				merged <- re
			}
		}()

		go func() {
			wg.Wait()
			close(merged)
		}()

		return groupSubStartedMsg{groupKey: gk, events: merged, cancel: cancel}
	}
}

// waitForGroupEvent blocks on the group subscription channel and returns the next event.
// Returns groupMetaMsg for kind 39000 metadata events and groupEventMsg for chat messages.
func waitForGroupEvent(events <-chan nostr.RelayEvent, gk string, relayURL string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		for {
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
					return groupMetaMsg{RelayURL: relayURL, GroupID: groupID, Name: name, RelayPubKey: re.PubKey.Hex(), FromSub: true}
				}
				log.Printf("waitForGroupEvent: got metadata event but no usable name, skipping")
				continue
			}

			return groupEventMsg(ChatMessage{
				Author:    shortPK(re.PubKey.Hex()),
				PubKey:    re.PubKey.Hex(),
				Content:   re.Content,
				Timestamp: re.CreatedAt,
				EventID:   re.ID.Hex(),
				GroupKey:  gk,
				IsMine:    re.PubKey == keys.PK,
			})
		}
	}
}

// buildGroupMessageEvent builds a kind-9 message event for a NIP-29 group.
func buildGroupMessageEvent(groupID, content string, previousIDs []string, keys Keys) (nostr.Event, error) {
	tags := nostr.Tags{{"h", groupID}}
	tags = append(tags, pickPreviousTags(previousIDs)...)
	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupChatMessage,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   content,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// publishGroupMessage signs and publishes a kind-9 message to a NIP-29 group.
func publishGroupMessage(pool *nostr.Pool, relayURL, groupID, content string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		gk := groupKey(relayURL, groupID)
		evt, err := buildGroupMessageEvent(groupID, content, previousIDs, keys)
		if err != nil {
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
			Author:    shortPK(keys.PK.Hex()),
			PubKey:    keys.PK.Hex(),
			Content:   content,
			Timestamp: evt.CreatedAt,
			EventID:   evt.GetID().Hex(),
			GroupKey:  gk,
			IsMine:    true,
		})
	}
}

// buildJoinGroupEvent builds a kind-9021 join request event for a NIP-29 group.
func buildJoinGroupEvent(groupID string, previousIDs []string, inviteCode string, keys Keys) (nostr.Event, error) {
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
		return evt, err
	}
	return evt, nil
}

// joinGroupCmd publishes a kind-9021 join request for a NIP-29 group.
func joinGroupCmd(pool *nostr.Pool, relayURL, groupID string, previousIDs []string, inviteCode string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildJoinGroupEvent(groupID, previousIDs, inviteCode, keys)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group join: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group join: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			// "already a member" is not a real error — treat it as success.
			errStr := strings.ToLower(err.Error())
			if !strings.Contains(errStr, "already") {
				return nostrErrMsg{fmt.Errorf("group join: publish: %w", err)}
			}
			log.Printf("joinGroupCmd: %s (treating as success)", err)
		}

		log.Printf("joinGroupCmd: sent kind 9021 to %s for group %s", relayURL, groupID)
		return groupJoinedMsg{RelayURL: relayURL, GroupID: groupID}
	}
}

// buildLeaveGroupEvent builds a kind-9022 leave request event for a NIP-29 group.
func buildLeaveGroupEvent(groupID string, previousIDs []string, keys Keys) (nostr.Event, error) {
	tags := nostr.Tags{{"h", groupID}}
	tags = append(tags, pickPreviousTags(previousIDs)...)
	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupLeaveRequest,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// leaveGroupCmd publishes a kind-9022 leave request for a NIP-29 group.
func leaveGroupCmd(pool *nostr.Pool, relayURL, groupID string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildLeaveGroupEvent(groupID, previousIDs, keys)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group leave: sign: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := pool.EnsureRelay(relayURL)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("group leave: connect %s: %w", relayURL, err)}
		}
		if err := r.Publish(ctx, evt); err != nil {
			return nostrErrMsg{fmt.Errorf("group leave: publish to %s group %s: %w", relayURL, groupID, err)}
		}
		log.Printf("leaveGroupCmd: sent kind 9022 to %s for group %s", relayURL, groupID)
		return nil
	}
}

// fetchGroupMetaCmd fetches a kind-39000 event to resolve the group name.
func fetchGroupMetaCmd(pool *nostr.Pool, relayURL, groupID string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchGroupMeta: relay=%s group=%s", relayURL, groupID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		re := pool.QuerySingle(ctx, []string{relayURL}, nostr.Filter{
			Kinds: []nostr.Kind{nostr.KindSimpleGroupMetadata},
			Tags:  nostr.TagMap{"d": {groupID}},
		}, nostr.SubscriptionOptions{})
		if re == nil {
			log.Printf("fetchGroupMeta: not found for %s on %s", groupID, relayURL)
			return nil
		}

		g, err := nip29.NewGroupFromMetadataEvent(relayURL, &re.Event)
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
		return groupMetaMsg{RelayURL: relayURL, GroupID: groupID, Name: name, RelayPubKey: re.PubKey.Hex()}
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
		ep, ok := data.(nostr.EntityPointer)
		if !ok {
			return "", "", fmt.Errorf("naddr data is not an EntityPointer")
		}
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

// buildCreateGroupEvent builds a kind-9007 event to create a NIP-29 group.
// The groupID must be provided by the caller (typically a random 8-hex-char string).
func buildCreateGroupEvent(groupID, name string, keys Keys) (nostr.Event, error) {
	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupCreateGroup,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"h", groupID}, {"name", name}},
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// createGroupCmd publishes a kind 9007 event to create a NIP-29 group on a relay.
func createGroupCmd(pool *nostr.Pool, relayURL, name string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		// Generate random 8-hex-char group ID.
		idBytes := make([]byte, 4)
		if _, err := rand.Read(idBytes); err != nil {
			return nostrErrMsg{fmt.Errorf("create group: random: %w", err)}
		}
		groupID := hex.EncodeToString(idBytes)

		evt, err := buildCreateGroupEvent(groupID, name, keys)
		if err != nil {
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

// buildDeleteGroupEventEvent builds a kind-9005 event to delete an event from a NIP-29 group.
func buildDeleteGroupEventEvent(groupID, eventID string, previousIDs []string, keys Keys) (nostr.Event, error) {
	tags := nostr.Tags{{"h", groupID}, {"e", eventID}}
	tags = append(tags, pickPreviousTags(previousIDs)...)

	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupDeleteEvent,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// deleteGroupEventCmd publishes a kind 9005 event to delete an event from a NIP-29 group.
func deleteGroupEventCmd(pool *nostr.Pool, relayURL, groupID, eventID string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildDeleteGroupEventEvent(groupID, eventID, previousIDs, keys)
		if err != nil {
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

// buildCreateGroupInviteEvent builds a kind-9009 invite event for a NIP-29 group.
func buildCreateGroupInviteEvent(groupID string, previousIDs []string, keys Keys) (nostr.Event, error) {
	tags := nostr.Tags{{"h", groupID}}
	tags = append(tags, pickPreviousTags(previousIDs)...)

	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupCreateInvite,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// createGroupInviteCmd publishes a kind 9009 event to create an invite for a NIP-29 group.
//nolint:unused // will be wired to /group invite command
func createGroupInviteCmd(pool *nostr.Pool, relayURL, groupID string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildCreateGroupInviteEvent(groupID, previousIDs, keys)
		if err != nil {
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

		// Use the event ID as the invite code; r.Publish does not return
		// relay-generated content, and buildCreateGroupInviteEvent leaves
		// Content empty, so the event ID is the only stable identifier.
		code := evt.GetID().Hex()

		log.Printf("createGroupInviteCmd: invite for group %s on %s: %s", groupID, relayURL, code)
		return groupInviteCreatedMsg{RelayURL: relayURL, GroupID: groupID, Code: code}
	}
}

// inviteDMCmd sends a DM with a group invite in host'groupid format.
func inviteDMCmd(pool *nostr.Pool, relays []string, relayURL, groupID, groupName, recipientPK string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		host := strings.TrimPrefix(strings.TrimPrefix(relayURL, "wss://"), "ws://")
		dmText := fmt.Sprintf("You've been invited to ~%s\n\n%s'%s", groupName, host, groupID)
		// Reuse sendDM logic inline — call the returned Cmd directly.
		return sendDM(pool, relays, recipientPK, dmText, keys, kr)()
	}
}

// buildPutUserEvent builds a kind-9000 event to add a user to a NIP-29 group.
func buildPutUserEvent(groupID, pubkey string, previousIDs []string, keys Keys) (nostr.Event, error) {
	tags := nostr.Tags{{"h", groupID}, {"p", pubkey}}
	tags = append(tags, pickPreviousTags(previousIDs)...)

	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupPutUser,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// putUserCmd publishes a kind 9000 event to add a user to a NIP-29 group.
func putUserCmd(pool *nostr.Pool, relayURL, groupID, pubkey string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildPutUserEvent(groupID, pubkey, previousIDs, keys)
		if err != nil {
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

// buildEditGroupMetadataEvent builds a kind-9002 event to edit group metadata.
func buildEditGroupMetadataEvent(groupID string, fields map[string]string, previousIDs []string, keys Keys) (nostr.Event, error) {
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
		return evt, err
	}
	return evt, nil
}

// editGroupMetadataCmd publishes a kind 9002 event to edit group metadata.
func editGroupMetadataCmd(pool *nostr.Pool, relayURL, groupID string, fields map[string]string, previousIDs []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildEditGroupMetadataEvent(groupID, fields, previousIDs, keys)
		if err != nil {
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

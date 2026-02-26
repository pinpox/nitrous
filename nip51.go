package main

import (
	"context"
	"encoding/json"
	"fmt"

	"fiatjaf.com/nostr"
)

// selfEncrypt encrypts plaintext to ourselves using NIP-44 via the Keyer interface.
func selfEncrypt(ctx context.Context, kr nostr.Keyer, plaintext string) (string, error) {
	pk, err := kr.GetPublicKey(ctx)
	if err != nil {
		return "", fmt.Errorf("selfEncrypt: get pubkey: %w", err)
	}
	return kr.Encrypt(ctx, plaintext, pk)
}

// selfDecrypt decrypts ciphertext that was encrypted to ourselves.
func selfDecrypt(ctx context.Context, kr nostr.Keyer, ciphertext string) (string, error) {
	pk, err := kr.GetPublicKey(ctx)
	if err != nil {
		return "", fmt.Errorf("selfDecrypt: get pubkey: %w", err)
	}
	return kr.Decrypt(ctx, ciphertext, pk)
}

// buildContactsListEvent builds a kind 30000 (categorized people list) event
// with d-tag "Chat-Friends" and NIP-44 self-encrypted content containing
// the contact list in [["p","hexPubkey","relayHint","petname"], ...] format.
// This is compatible with 0xchat's contact list format.
func buildContactsListEvent(ctx context.Context, contacts []Contact, keys Keys, kr nostr.Keyer) (nostr.Event, error) {
	// Build the inner tag array: [["p","pk","","name"], ...]
	var inner nostr.Tags
	for _, c := range contacts {
		inner = append(inner, nostr.Tag{"p", c.PubKey, "", c.Name})
	}

	plaintext, err := json.Marshal(inner)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("buildContactsListEvent: marshal: %w", err)
	}

	ciphertext, err := selfEncrypt(ctx, kr, string(plaintext))
	if err != nil {
		return nostr.Event{}, fmt.Errorf("buildContactsListEvent: encrypt: %w", err)
	}

	evt := nostr.Event{
		Kind:      nostr.KindCategorizedPeopleList, // 30000
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", "Chat-Friends"}},
		Content:   ciphertext,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, fmt.Errorf("buildContactsListEvent: sign: %w", err)
	}
	return evt, nil
}

// parseContactsListEvent decrypts and parses a kind 30000 "Chat-Friends" event
// into a slice of Contacts.
func parseContactsListEvent(ctx context.Context, evt *nostr.Event, kr nostr.Keyer) ([]Contact, error) {
	if evt.Content == "" {
		return nil, nil
	}

	plaintext, err := selfDecrypt(ctx, kr, evt.Content)
	if err != nil {
		return nil, fmt.Errorf("parseContactsListEvent: decrypt: %w", err)
	}

	var tags nostr.Tags
	if err := json.Unmarshal([]byte(plaintext), &tags); err != nil {
		return nil, fmt.Errorf("parseContactsListEvent: unmarshal: %w", err)
	}

	var contacts []Contact
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != "p" {
			continue
		}
		pk := tag[1]
		name := ""
		// tag[2] is relay hint (skip), tag[3] is petname
		if len(tag) >= 4 {
			name = tag[3]
		}
		if name == "" {
			name = shortPK(pk)
		}
		contacts = append(contacts, Contact{Name: name, PubKey: pk})
	}
	return contacts, nil
}

// buildPublicChatsListEvent builds a kind 10005 (public chat list) event
// with ["e", channelID] tags for each joined NIP-28 channel.
func buildPublicChatsListEvent(channels []Channel, keys Keys) (nostr.Event, error) {
	var tags nostr.Tags
	for _, ch := range channels {
		tags = append(tags, nostr.Tag{"e", ch.ID})
	}

	evt := nostr.Event{
		Kind:      nostr.KindPublicChatList, // 10005
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   "",
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, fmt.Errorf("buildPublicChatsListEvent: sign: %w", err)
	}
	return evt, nil
}

// parsePublicChatsListEvent extracts channels from a kind 10005 event.
// Channel names are not stored in the event; callers should resolve names
// via fetchChannelMetaCmd.
func parsePublicChatsListEvent(evt *nostr.Event) []Channel {
	var channels []Channel
	for _, tag := range evt.Tags {
		if len(tag) >= 2 && tag[0] == "e" {
			channels = append(channels, Channel{ID: tag[1], Name: shortPK(tag[1])})
		}
	}
	return channels
}

// buildSimpleGroupsListEvent builds a kind 10009 (simple group list) event
// with ["group", groupID, relayURL] tags for each joined NIP-29 group.
func buildSimpleGroupsListEvent(groups []Group, keys Keys) (nostr.Event, error) {
	var tags nostr.Tags
	for _, g := range groups {
		tag := nostr.Tag{"group", g.GroupID, g.RelayURL}
		if g.Name != "" {
			tag = append(tag, g.Name)
		}
		tags = append(tags, tag)
	}

	evt := nostr.Event{
		Kind:      nostr.KindSimpleGroupList, // 10009
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   "",
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, fmt.Errorf("buildSimpleGroupsListEvent: sign: %w", err)
	}
	return evt, nil
}

// parseSimpleGroupsListEvent extracts groups from a kind 10009 event.
func parseSimpleGroupsListEvent(evt *nostr.Event) []SavedGroup {
	var groups []SavedGroup
	for _, tag := range evt.Tags {
		if len(tag) < 3 || tag[0] != "group" {
			continue
		}
		groupID := tag[1]
		relayURL := tag[2]
		name := ""
		if len(tag) >= 4 {
			name = tag[3]
		}
		if name == "" {
			name = shortPK(groupID)
		}
		groups = append(groups, SavedGroup{Name: name, RelayURL: relayURL, GroupID: groupID})
	}
	return groups
}

// contactsFromModel converts in-memory DM peer list + profile cache into a
// []Contact suitable for building a kind 30000 event.
func contactsFromModel(dmPeers []string, profiles map[string]string) []Contact {
	var contacts []Contact
	for _, pk := range dmPeers {
		name := shortPK(pk)
		if n, ok := profiles[pk]; ok && n != "" {
			name = n
		}
		contacts = append(contacts, Contact{Name: name, PubKey: pk})
	}
	return contacts
}

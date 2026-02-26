package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip05"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip65"
)

// Keys holds the user's nostr key pair.
type Keys struct {
	SK   nostr.SecretKey
	PK   nostr.PubKey
	NPub string
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

// nostrErrMsg wraps a nostr operation error as a Bubbletea message.
type nostrErrMsg struct{ err error }

func (e nostrErrMsg) Error() string { return e.err.Error() }

// profileResolvedMsg is returned after fetching a kind-0 profile for a pubkey.
type profileResolvedMsg struct {
	PubKey      string
	DisplayName string
}

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

	var sk nostr.SecretKey
	if strings.HasPrefix(raw, "nsec") {
		prefix, val, err := nip19.Decode(raw)
		if err != nil {
			return Keys{}, fmt.Errorf("failed to decode nsec: %w", err)
		}
		if prefix != "nsec" {
			return Keys{}, fmt.Errorf("expected nsec prefix, got %s", prefix)
		}
		sk = val.(nostr.SecretKey)
	} else {
		var err error
		sk, err = nostr.SecretKeyFromHex(raw)
		if err != nil {
			return Keys{}, fmt.Errorf("failed to parse hex secret key: %w", err)
		}
	}

	pk := nostr.GetPublicKey(sk)
	npub := nip19.EncodeNpub(pk)

	return Keys{SK: sk, PK: pk, NPub: npub}, nil
}

// fetchProfileCmd fetches a kind-0 event (NIP-01 profile metadata) for a pubkey.
// If not found on the user's relays, looks up the peer's NIP-65 relay list
// and tries their write relays.
func fetchProfileCmd(pool *nostr.Pool, relays []string, pubkey string) tea.Cmd {
	return func() tea.Msg {
		log.Printf("fetchProfile: pubkey=%s", shortPK(pubkey))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		pk, err := nostr.PubKeyFromHex(pubkey)
		if err != nil {
			log.Printf("fetchProfile: invalid pubkey %s: %v", shortPK(pubkey), err)
			return profileResolvedMsg{PubKey: pubkey, DisplayName: shortPK(pubkey)}
		}

		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []nostr.Kind{nostr.KindProfileMetadata},
			Authors: []nostr.PubKey{pk},
		}, nostr.SubscriptionOptions{})

		// If not found locally, check the peer's NIP-65 relay list for their write relays.
		if re == nil {
			peerRelays := getPeerRelays(pool, relays, pk)
			if len(peerRelays) > 0 {
				log.Printf("fetchProfile: not on local relays, trying %d peer relays for %s", len(peerRelays), shortPK(pubkey))
				re = pool.QuerySingle(ctx, peerRelays, nostr.Filter{
					Kinds:   []nostr.Kind{nostr.KindProfileMetadata},
					Authors: []nostr.PubKey{pk},
				}, nostr.SubscriptionOptions{})
			}
		}

		if re == nil {
			log.Printf("fetchProfile: not found for %s", shortPK(pubkey))
			return profileResolvedMsg{PubKey: pubkey, DisplayName: shortPK(pubkey)}
		}

		name := parseProfileMeta(re.Content)
		if name == "" {
			name = shortPK(pubkey)
		}

		log.Printf("fetchProfile: resolved %s -> %q", shortPK(pubkey), name)
		return profileResolvedMsg{PubKey: pubkey, DisplayName: name}
	}
}

// buildProfileEvent builds a kind-0 event with the user's profile metadata.
func buildProfileEvent(profile ProfileConfig, keys Keys) (nostr.Event, error) {
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
		return nostr.Event{}, fmt.Errorf("marshal: %w", err)
	}

	evt := nostr.Event{
		Kind:      nostr.KindProfileMetadata,
		CreatedAt: nostr.Now(),
		Content:   string(content),
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, fmt.Errorf("sign: %w", err)
	}
	return evt, nil
}

// profilePublishedMsg is returned after publishing a kind-0 profile event.
type profilePublishedMsg struct {
	err error
}

// publishProfileCmd publishes a kind-0 event with the user's profile metadata.
func publishProfileCmd(pool *nostr.Pool, relays []string, profile ProfileConfig, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildProfileEvent(profile, keys)
		if err != nil {
			return profilePublishedMsg{err: fmt.Errorf("publishProfile: %w", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var successCount int
		for res := range pool.PublishMany(ctx, relays, evt) {
			if res.Error == nil {
				successCount++
			} else {
				log.Printf("publishProfile: relay error: %v", res.Error)
			}
		}
		if successCount == 0 {
			return profilePublishedMsg{err: fmt.Errorf("failed to publish to any relay")}
		}
		log.Printf("publishProfile: published kind 0 to %d relays", successCount)
		return profilePublishedMsg{}
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
		if !nip05.IsValidIdentifier(identifier) {
			return nip05ResolvedMsg{Identifier: identifier, Err: fmt.Errorf("invalid NIP-05 identifier: %s", identifier)}
		}

		log.Printf("resolveNIP05: resolving %s", identifier)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		pp, err := nip05.QueryIdentifier(ctx, identifier)
		if err != nil {
			return nip05ResolvedMsg{Identifier: identifier, Err: err}
		}

		pk := pp.PublicKey.Hex()
		log.Printf("resolveNIP05: %s -> %s", identifier, shortPK(pk))
		return nip05ResolvedMsg{Identifier: identifier, PubKey: pk}
	}
}

// parseProfileMeta extracts a display name from a kind-0 profile JSON content string.
// Prefers display_name, falls back to name, then returns empty string.
func parseProfileMeta(content string) string {
	var meta struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal([]byte(content), &meta); err != nil {
		return ""
	}
	if meta.DisplayName != "" {
		return meta.DisplayName
	}
	return meta.Name
}

// drainPublish drains the PublishMany result channel with context awareness,
// so a hanging relay doesn't block forever.
func drainPublish(ctx context.Context, ch <-chan nostr.PublishResult) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// shortPK returns the first 8 characters of a public key for display.
func shortPK(pk string) string {
	if len(pk) > 8 {
		return pk[:8]
	}
	return pk
}

// --- NIP-51 List Sync ---

// nip51ListsFetchedMsg is returned after querying relays for the user's NIP-51 lists.
type nip51ListsFetchedMsg struct {
	contacts   []Contact
	contactsTS nostr.Timestamp
	channels   []Channel
	channelsTS nostr.Timestamp
	groups     []SavedGroup
	groupsTS   nostr.Timestamp
}

// nip51PublishResultMsg is returned after publishing a NIP-51 list event.
type nip51PublishResultMsg struct {
	listKind nostr.Kind
	err      error
}

// fetchNIP51ListsCmd queries relays for the user's kind 30000, 10005, and 10009 lists.
func fetchNIP51ListsCmd(pool *nostr.Pool, relays []string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var result nip51ListsFetchedMsg

		// Kind 30000 "Chat-Friends" (parameterized replaceable)
		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []nostr.Kind{nostr.KindCategorizedPeopleList},
			Authors: []nostr.PubKey{keys.PK},
			Tags:    nostr.TagMap{"d": {"Chat-Friends"}},
		}, nostr.SubscriptionOptions{})
		if re != nil {
			contacts, err := parseContactsListEvent(ctx, &re.Event, kr)
			if err != nil {
				log.Printf("fetchNIP51Lists: contacts parse error: %v", err)
			} else {
				result.contacts = contacts
				result.contactsTS = re.CreatedAt
				log.Printf("fetchNIP51Lists: got %d contacts (ts=%d)", len(contacts), re.CreatedAt)
			}
		}

		// Kind 10005 (public chat list, standard replaceable)
		re = pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []nostr.Kind{nostr.KindPublicChatList},
			Authors: []nostr.PubKey{keys.PK},
		}, nostr.SubscriptionOptions{})
		if re != nil {
			channels := parsePublicChatsListEvent(&re.Event)
			result.channels = channels
			result.channelsTS = re.CreatedAt
			log.Printf("fetchNIP51Lists: got %d channels (ts=%d)", len(channels), re.CreatedAt)
		}

		// Kind 10009 (simple group list, standard replaceable)
		re = pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []nostr.Kind{nostr.KindSimpleGroupList},
			Authors: []nostr.PubKey{keys.PK},
		}, nostr.SubscriptionOptions{})
		if re != nil {
			groups := parseSimpleGroupsListEvent(&re.Event)
			result.groups = groups
			result.groupsTS = re.CreatedAt
			log.Printf("fetchNIP51Lists: got %d groups (ts=%d)", len(groups), re.CreatedAt)
		}

		return result
	}
}

// publishContactsListCmd builds and publishes a kind 30000 "Chat-Friends" event.
func publishContactsListCmd(pool *nostr.Pool, relays []string, contacts []Contact, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		evt, err := buildContactsListEvent(ctx, contacts, keys, kr)
		if err != nil {
			cancel()
			return nip51PublishResultMsg{listKind: nostr.KindCategorizedPeopleList, err: err}
		}

		defer cancel()
		drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
		log.Printf("publishContactsList: published kind %d with %d contacts", nostr.KindCategorizedPeopleList, len(contacts))
		return nip51PublishResultMsg{listKind: nostr.KindCategorizedPeopleList}
	}
}

// publishPublicChatsListCmd builds and publishes a kind 10005 event.
func publishPublicChatsListCmd(pool *nostr.Pool, relays []string, channels []Channel, keys Keys) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		evt, err := buildPublicChatsListEvent(channels, keys)
		if err != nil {
			cancel()
			return nip51PublishResultMsg{listKind: nostr.KindPublicChatList, err: err}
		}

		defer cancel()
		drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
		log.Printf("publishPublicChatsList: published kind %d with %d channels", nostr.KindPublicChatList, len(channels))
		return nip51PublishResultMsg{listKind: nostr.KindPublicChatList}
	}
}

// publishSimpleGroupsListCmd builds and publishes a kind 10009 event.
func publishSimpleGroupsListCmd(pool *nostr.Pool, relays []string, groups []Group, keys Keys) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		evt, err := buildSimpleGroupsListEvent(groups, keys)
		if err != nil {
			cancel()
			return nip51PublishResultMsg{listKind: nostr.KindSimpleGroupList, err: err}
		}

		defer cancel()
		drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
		log.Printf("publishSimpleGroupsList: published kind %d with %d groups", nostr.KindSimpleGroupList, len(groups))
		return nip51PublishResultMsg{listKind: nostr.KindSimpleGroupList}
	}
}

// getPeerRelays fetches the NIP-65 relay list (kind 10002) for a pubkey
// and returns the write relay URLs. Falls back to nil if not found.
func getPeerRelays(pool *nostr.Pool, relays []string, pubkey nostr.PubKey) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	re := pool.QuerySingle(ctx, relays, nostr.Filter{
		Kinds:   []nostr.Kind{nostr.KindRelayListMetadata},
		Authors: []nostr.PubKey{pubkey},
	}, nostr.SubscriptionOptions{})
	if re == nil {
		return nil
	}

	_, writeRelays := nip65.ParseRelayList(re.Event)
	log.Printf("getPeerRelays: %s -> %v", shortPK(pubkey.Hex()), writeRelays)
	return writeRelays
}

// containsStr is replaced by slices.Contains but kept as an alias for readability.
func containsStr(sl []string, s string) bool {
	return slices.Contains(sl, s)
}

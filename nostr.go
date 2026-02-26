package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Keys holds the user's nostr key pair.
type Keys struct {
	SK     string
	PK     string
	NPub   string
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
		Kind:      0,
		CreatedAt: nostr.Now(),
		Content:   string(content),
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, fmt.Errorf("sign: %w", err)
	}
	return evt, nil
}

// publishProfileCmd publishes a kind-0 event with the user's profile metadata.
func publishProfileCmd(pool *nostr.SimplePool, relays []string, profile ProfileConfig, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildProfileEvent(profile, keys)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("publishProfile: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
			log.Printf("publishProfile: published kind 0")
		}()
		return nil
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
	listKind int
	err      error
}

// fetchNIP51ListsCmd queries relays for the user's kind 30000, 10005, and 10009 lists.
func fetchNIP51ListsCmd(pool *nostr.SimplePool, relays []string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var result nip51ListsFetchedMsg

		// Kind 30000 "Chat-Friends" (parameterized replaceable)
		re := pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []int{nostr.KindCategorizedPeopleList},
			Authors: []string{keys.PK},
			Tags:    nostr.TagMap{"d": {"Chat-Friends"}},
		})
		if re != nil {
			contacts, err := parseContactsListEvent(ctx, re.Event, kr)
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
			Kinds:   []int{nostr.KindPublicChatList},
			Authors: []string{keys.PK},
		})
		if re != nil {
			channels := parsePublicChatsListEvent(re.Event)
			result.channels = channels
			result.channelsTS = re.CreatedAt
			log.Printf("fetchNIP51Lists: got %d channels (ts=%d)", len(channels), re.CreatedAt)
		}

		// Kind 10009 (simple group list, standard replaceable)
		re = pool.QuerySingle(ctx, relays, nostr.Filter{
			Kinds:   []int{nostr.KindSimpleGroupList},
			Authors: []string{keys.PK},
		})
		if re != nil {
			groups := parseSimpleGroupsListEvent(re.Event)
			result.groups = groups
			result.groupsTS = re.CreatedAt
			log.Printf("fetchNIP51Lists: got %d groups (ts=%d)", len(groups), re.CreatedAt)
		}

		return result
	}
}

// publishContactsListCmd builds and publishes a kind 30000 "Chat-Friends" event.
func publishContactsListCmd(pool *nostr.SimplePool, relays []string, contacts []Contact, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		evt, err := buildContactsListEvent(ctx, contacts, keys, kr)
		if err != nil {
			cancel()
			return nip51PublishResultMsg{listKind: nostr.KindCategorizedPeopleList, err: err}
		}

		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
			log.Printf("publishContactsList: published kind %d with %d contacts", nostr.KindCategorizedPeopleList, len(contacts))
		}()
		return nip51PublishResultMsg{listKind: nostr.KindCategorizedPeopleList}
	}
}

// publishPublicChatsListCmd builds and publishes a kind 10005 event.
func publishPublicChatsListCmd(pool *nostr.SimplePool, relays []string, channels []Channel, keys Keys) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		evt, err := buildPublicChatsListEvent(channels, keys)
		if err != nil {
			cancel()
			return nip51PublishResultMsg{listKind: nostr.KindPublicChatList, err: err}
		}

		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
			log.Printf("publishPublicChatsList: published kind %d with %d channels", nostr.KindPublicChatList, len(channels))
		}()
		return nip51PublishResultMsg{listKind: nostr.KindPublicChatList}
	}
}

// publishSimpleGroupsListCmd builds and publishes a kind 10009 event.
func publishSimpleGroupsListCmd(pool *nostr.SimplePool, relays []string, groups []Group, keys Keys) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		evt, err := buildSimpleGroupsListEvent(groups, keys)
		if err != nil {
			cancel()
			return nip51PublishResultMsg{listKind: nostr.KindSimpleGroupList, err: err}
		}

		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
			log.Printf("publishSimpleGroupsList: published kind %d with %d groups", nostr.KindSimpleGroupList, len(groups))
		}()
		return nip51PublishResultMsg{listKind: nostr.KindSimpleGroupList}
	}
}

package main

import (
	"fmt"
	"log"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/gen2brain/beeep"
)

// notifyBody resolves nostr:npub/nprofile references to @displayname for
// human-readable notification text.
func notifyBody(content string, profiles map[string]string) string {
	resolved, mentions := renderMentions(content, profiles)
	return styleMentions(resolved, mentions, lipgloss.NewStyle())
}

// notifyCmd returns a tea.Cmd that sends desktop and/or bell notifications
// based on config. Returns nil if both backends are disabled.
func notifyCmd(cfg Config, title, body string) tea.Cmd {
	desktop := cfg.DesktopNotificationsEnabled()
	bell := cfg.BellNotificationsEnabled()

	if !desktop && !bell {
		return nil
	}

	// Truncate body to 100 chars.
	if len(body) > 100 {
		body = body[:97] + "..."
	}

	var cmds []tea.Cmd

	if desktop {
		t, b := title, body // capture for closure
		cmds = append(cmds, func() tea.Msg {
			if err := beeep.Notify(t, b, ""); err != nil {
				log.Printf("desktop notification failed: %v", err)
			}
			return nil
		})
	}

	if bell {
		cmds = append(cmds, func() tea.Msg {
			fmt.Print("\a")
			return nil
		})
	}

	return tea.Batch(cmds...)
}

// mentionsUser checks whether a message mentions the given user pubkey.
// It checks:
// 1. Event p tags — if any p tag value matches userPK
// 2. Content — if content contains nostr:npub1... or nostr:nprofile1... resolving to userPK
func mentionsUser(content string, tags nostr.Tags, userPK string) bool {
	// Check p tags first (primary mechanism).
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == userPK {
			return true
		}
	}

	// Fallback: check content for NIP-27 references.
	for _, match := range nostrMentionPattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		bech32str := match[1]
		prefix, value, err := nip19.Decode(bech32str)
		if err != nil {
			continue
		}

		var hexPK string
		switch prefix {
		case "npub":
			pk, ok := value.(nostr.PubKey)
			if !ok {
				continue
			}
			hexPK = pk.Hex()
		case "nprofile":
			pp, ok := value.(nostr.ProfilePointer)
			if !ok {
				continue
			}
			hexPK = pp.PublicKey.Hex()
		default:
			continue
		}

		if hexPK == userPK {
			return true
		}
	}

	return false
}

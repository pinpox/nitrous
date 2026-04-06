package main

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

// mentionPattern matches @username at word boundaries. The @ must be at the
// start of the string or preceded by whitespace (avoids matching emails like
// user@example.com). Usernames may contain word chars, hyphens, and dots.
var mentionPattern = regexp.MustCompile(`(?:^|(?:\s))@([\w.-]+)`)

// nostrMentionPattern matches nostr:npub1... and nostr:nprofile1... references in content.
var nostrMentionPattern = regexp.MustCompile(`nostr:((?:npub1|nprofile1)[a-z0-9]+)`)

// resolveMentions replaces @username patterns in content with nostr:npub1...
// references using the profiles map (pubkey hex → display name). Returns the
// rewritten content and a deduplicated list of mentioned hex pubkeys.
func resolveMentions(content string, profiles map[string]string) (string, []string) {
	// Build reverse map: lowercase display name → hex pubkey.
	nameToKey := make(map[string]string, len(profiles))
	for pk, name := range profiles {
		lower := strings.ToLower(name)
		if _, exists := nameToKey[lower]; !exists {
			nameToKey[lower] = pk
		}
	}

	seen := make(map[string]bool)
	var mentionedPKs []string

	result := mentionPattern.ReplaceAllStringFunc(content, func(match string) string {
		// Preserve leading whitespace if present.
		prefix := ""
		trimmed := match
		if strings.HasPrefix(match, " ") || strings.HasPrefix(match, "\t") || strings.HasPrefix(match, "\n") {
			prefix = match[:1]
			trimmed = match[1:]
		}

		username := strings.TrimPrefix(trimmed, "@")
		hexPK, ok := nameToKey[strings.ToLower(username)]
		if !ok {
			return match // unknown user, leave as-is
		}

		pk, err := nostr.PubKeyFromHex(hexPK)
		if err != nil {
			return match
		}

		npub := nip19.EncodeNpub(pk)
		if !seen[hexPK] {
			seen[hexPK] = true
			mentionedPKs = append(mentionedPKs, hexPK)
		}

		return prefix + "nostr:" + npub
	})

	return result, mentionedPKs
}

// mentionPlaceholderPattern matches the NUL-delimited markers inserted by
// renderMentions for resolved mentions.
var mentionPlaceholderPattern = regexp.MustCompile("\x00M([0-9]+)\x00")

// renderMentions replaces nostr:npub1... and nostr:nprofile1... references in
// content with @displayname for readability. Falls back to @npub1<8chars>...
// if the pubkey is not in the profiles map.
//
// Resolved mentions are emitted as NUL-delimited placeholders (\x00M<n>\x00)
// rather than the literal @displayname; the second return value is the
// placeholder-index → @displayname table. Callers MUST pass the result
// through styleMentions to obtain human-readable text. The placeholder layer
// exists so styling can be applied after markdown rendering without substring
// collisions (e.g. @al matching the prefix of @alice) or ANSI corruption.
func renderMentions(content string, profiles map[string]string) (string, []string) {
	seen := make(map[string]int)
	var resolved []string

	out := nostrMentionPattern.ReplaceAllStringFunc(content, func(match string) string {
		// Extract the bech32 part after "nostr:".
		bech32str := strings.TrimPrefix(match, "nostr:")

		prefix, value, err := nip19.Decode(bech32str)
		if err != nil {
			return match
		}

		var hexPK string
		switch prefix {
		case "npub":
			pk, ok := value.(nostr.PubKey)
			if !ok {
				return match
			}
			hexPK = pk.Hex()
		case "nprofile":
			pp, ok := value.(nostr.ProfilePointer)
			if !ok {
				return match
			}
			hexPK = pp.PublicKey.Hex()
		default:
			return match
		}

		if name, found := profiles[hexPK]; found {
			mention := "@" + name
			idx, ok := seen[mention]
			if !ok {
				idx = len(resolved)
				seen[mention] = idx
				resolved = append(resolved, mention)
			}
			return "\x00M" + strconv.Itoa(idx) + "\x00"
		}

		// Truncated fallback: @npub1<8chars>... or @nprofile1<8chars>...
		if len(bech32str) > 13 {
			return "@" + bech32str[:13] + "..."
		}
		return "@" + bech32str
	})

	return out, resolved
}

// styleMentions replaces the NUL-delimited placeholders inserted by
// renderMentions with styled @displayname strings. Call this after markdown
// rendering. Passing an empty lipgloss.Style yields plain text (used by the
// notification path).
func styleMentions(content string, mentions []string, style lipgloss.Style) string {
	return mentionPlaceholderPattern.ReplaceAllStringFunc(content, func(match string) string {
		n, err := strconv.Atoi(match[2 : len(match)-1])
		if err != nil || n < 0 || n >= len(mentions) {
			return match
		}
		return style.Render(mentions[n])
	})
}

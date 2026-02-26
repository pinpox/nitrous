package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// updateSuggestions generates context-aware autocomplete suggestions based on
// the current input value.
func (m *model) updateSuggestions() {
	text := m.input.Value()

	// Check for @mention anywhere in the input.
	if suggestions := m.mentionSuggestions(text); len(suggestions) > 0 {
		if !slicesEqual(suggestions, m.acSuggestions) {
			m.acIndex = 0
		}
		m.acSuggestions = suggestions
		m.acMention = true
		return
	}

	m.acMention = false

	if !strings.HasPrefix(text, "/") {
		m.acSuggestions = nil
		m.acIndex = 0
		return
	}

	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		m.acSuggestions = nil
		m.acIndex = 0
		return
	}

	// If input ends with a space, the user is starting a new token.
	trailingSpace := len(text) > 0 && text[len(text)-1] == ' '

	var suggestions []string

	switch {
	case len(tokens) == 1 && !trailingSpace:
		// Partial top-level command: /he → /help
		commands := []string{"/channel", "/join", "/dm", "/me", "/room", "/delete", "/group", "/invite", "/leave", "/help"}
		prefix := strings.ToLower(tokens[0])
		for _, c := range commands {
			if strings.HasPrefix(c, prefix) && c != prefix {
				suggestions = append(suggestions, c)
			}
		}

	case strings.ToLower(tokens[0]) == "/channel":
		subcommands := []string{"create"}
		switch {
		case len(tokens) == 1 && trailingSpace:
			suggestions = subcommands
		case len(tokens) == 2 && !trailingSpace:
			prefix := strings.ToLower(tokens[1])
			for _, sc := range subcommands {
				if strings.HasPrefix(sc, prefix) && sc != prefix {
					suggestions = append(suggestions, sc)
				}
			}
		}

	case strings.ToLower(tokens[0]) == "/group":
		subcommands := []string{"create", "set", "user", "name", "about", "picture"}
		switch {
		case len(tokens) == 1 && trailingSpace:
			// "/group " → show all subcommands
			suggestions = subcommands
		case len(tokens) == 2 && !trailingSpace:
			// "/group na" → filter subcommands
			prefix := strings.ToLower(tokens[1])
			for _, sc := range subcommands {
				if strings.HasPrefix(sc, prefix) && sc != prefix {
					suggestions = append(suggestions, sc)
				}
			}
		case len(tokens) == 2 && trailingSpace:
			// "/group set " → show options for the subcommand
			sub := strings.ToLower(tokens[1])
			if sub == "set" {
				suggestions = []string{"open", "closed"}
			} else if sub == "user" {
				suggestions = []string{"add"}
			}
		case len(tokens) == 3 && !trailingSpace:
			sub := strings.ToLower(tokens[1])
			if sub == "set" {
				options := []string{"open", "closed"}
				prefix := strings.ToLower(tokens[2])
				for _, o := range options {
					if strings.HasPrefix(o, prefix) && o != prefix {
						suggestions = append(suggestions, o)
					}
				}
			} else if sub == "user" {
				options := []string{"add"}
				prefix := strings.ToLower(tokens[2])
				for _, o := range options {
					if strings.HasPrefix(o, prefix) && o != prefix {
						suggestions = append(suggestions, o)
					}
				}
			}
		}

	case strings.ToLower(tokens[0]) == "/join":
		// "/join <partial>" → filter channel names and invite links
		if (len(tokens) == 1 && trailingSpace) || (len(tokens) == 2 && !trailingSpace) {
			partial := ""
			if len(tokens) == 2 {
				partial = tokens[1]
			}
			for _, it := range m.sidebar {
				if ci, ok := it.(ChannelItem); ok {
					candidate := "#" + ci.Channel.Name
					if partial == "" || (strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(partial)) && !strings.EqualFold(candidate, partial)) {
						suggestions = append(suggestions, candidate)
					}
				}
			}
			// Scan current chat messages for invite links (host'groupid format).
			for _, addr := range m.extractInviteAddresses() {
				if partial == "" || (strings.HasPrefix(strings.ToLower(addr), strings.ToLower(partial)) && !strings.EqualFold(addr, partial)) {
					suggestions = append(suggestions, addr)
				}
			}
		}

	case strings.ToLower(tokens[0]) == "/dm" || strings.ToLower(tokens[0]) == "/invite":
		// "/dm <partial>" or "/invite <partial>" → filter contact display names
		if (len(tokens) == 1 && trailingSpace) || (len(tokens) == 2 && !trailingSpace) {
			partial := ""
			if len(tokens) == 2 {
				partial = strings.ToLower(tokens[1])
			}
			for _, it := range m.sidebar {
				if di, ok := it.(DMItem); ok {
					name := di.Name
					if partial == "" || (strings.HasPrefix(strings.ToLower(name), partial) && !strings.EqualFold(name, partial)) {
						suggestions = append(suggestions, name)
					}
				}
			}
		}
	}

	if len(suggestions) == 0 {
		m.acSuggestions = nil
		m.acIndex = 0
		return
	}

	// Reset index when the suggestion list changes.
	if !slicesEqual(suggestions, m.acSuggestions) {
		m.acIndex = 0
	}
	m.acSuggestions = suggestions
}

// acceptSuggestion replaces the partial token in input with the selected suggestion.
func (m *model) acceptSuggestion() {
	if len(m.acSuggestions) == 0 {
		return
	}
	if m.acIndex >= len(m.acSuggestions) {
		m.acIndex = 0
	}

	selected := m.acSuggestions[m.acIndex]
	text := m.input.Value()

	var newText string
	if m.acMention {
		// @mention: find the last @ and replace from there.
		atPos := strings.LastIndex(text, "@")
		if atPos >= 0 {
			newText = text[:atPos] + "@" + selected + " "
		} else {
			newText = "@" + selected + " "
		}
	} else {
		tokens := strings.Fields(text)
		if len(tokens) == 1 && strings.HasPrefix(selected, "/") {
			// Completing the command itself: replace entire text.
			newText = selected + " "
		} else {
			// Completing a subcommand or argument: replace from last space.
			lastSpace := strings.LastIndex(text, " ")
			if lastSpace >= 0 {
				newText = text[:lastSpace+1] + selected + " "
			} else {
				newText = selected + " "
			}
		}
	}

	m.input.SetValue(newText)
	m.acSuggestions = nil
	m.acIndex = 0
}

// viewAutocomplete renders suggestions as a horizontal row.
func (m *model) viewAutocomplete() string {
	maxWidth := m.viewport.Width

	// Pre-render all items so we know their widths.
	rendered := make([]string, len(m.acSuggestions))
	widths := make([]int, len(m.acSuggestions))
	for i, s := range m.acSuggestions {
		if i == m.acIndex {
			rendered[i] = acSelectedStyle.Render(s)
		} else {
			rendered[i] = acSuggestionStyle.Render(s)
		}
		widths[i] = lipgloss.Width(rendered[i])
	}

	// Find a window of items that fits within maxWidth, ensuring the
	// selected item is always visible.
	start := m.acIndex
	end := m.acIndex + 1
	used := widths[m.acIndex]

	// Expand right, then left, alternating to keep selection roughly centered.
	for {
		grew := false
		if end < len(m.acSuggestions) && used+widths[end] <= maxWidth {
			used += widths[end]
			end++
			grew = true
		}
		if start > 0 && used+widths[start-1] <= maxWidth {
			start--
			used += widths[start]
			grew = true
		}
		if !grew {
			break
		}
	}

	var parts []string
	if start > 0 {
		parts = append(parts, acSuggestionStyle.Render("◂"))
	}
	parts = append(parts, rendered[start:end]...)
	if end < len(m.acSuggestions) {
		parts = append(parts, acSuggestionStyle.Render("▸"))
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// mentionSuggestions returns @username suggestions if the last word in text
// starts with @. Returns nil if no @ mention is being typed.
func (m *model) mentionSuggestions(text string) []string {
	// Find the last word boundary.
	lastSpace := strings.LastIndex(text, " ")
	var word string
	if lastSpace >= 0 {
		word = text[lastSpace+1:]
	} else {
		word = text
	}

	if !strings.HasPrefix(word, "@") || len(word) < 1 {
		return nil
	}

	partial := strings.ToLower(strings.TrimPrefix(word, "@"))

	// Collect unique authors from current chat messages.
	authors := m.currentChatAuthors()

	var suggestions []string
	for _, pk := range authors {
		if pk == m.keys.PK.Hex() {
			continue // skip self
		}
		name := m.resolveAuthor(pk)
		if partial == "" || (strings.HasPrefix(strings.ToLower(name), partial) && !strings.EqualFold(name, partial)) {
			suggestions = append(suggestions, name)
		}
	}
	return suggestions
}

// currentChatAuthors returns deduplicated pubkeys of message authors in the
// current chat, ordered by most recent message first.
func (m *model) currentChatAuthors() []string {
	var msgs []ChatMessage
	if item := m.activeSidebarItem(); item != nil {
		msgs = m.msgs[item.ItemID()]
	} else {
		return nil
	}

	seen := make(map[string]bool)
	var authors []string
	for i := len(msgs) - 1; i >= 0; i-- {
		pk := msgs[i].PubKey
		if pk == "" || msgs[i].Author == "system" || seen[pk] {
			continue
		}
		seen[pk] = true
		authors = append(authors, pk)
	}
	return authors
}

// extractInviteAddresses scans messages in the current chat for group invite
// links in host'groupid format and returns them (most recent first, deduped).
func (m *model) extractInviteAddresses() []string {
	var msgs []ChatMessage
	if item := m.activeSidebarItem(); item != nil {
		msgs = m.msgs[item.ItemID()]
	} else {
		msgs = m.globalMsgs
	}

	seen := make(map[string]bool)
	var addrs []string
	// Walk newest first so the most recent invite appears first.
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, line := range strings.Split(msgs[i].Content, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			word := fields[0]
			// Match host'groupid pattern (e.g. groups.0xchat.com'2d0fe936).
			// Only match if it's the first word on a line and the host part looks like a domain.
			if parts := strings.SplitN(word, "'", 2); len(parts) == 2 && strings.Contains(parts[0], ".") && parts[1] != "" {
				if !seen[word] {
					seen[word] = true
					addrs = append(addrs, word)
				}
			}
		}
	}
	return addrs
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

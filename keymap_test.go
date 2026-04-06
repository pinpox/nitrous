package main

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func TestParseKeyMap(t *testing.T) {
	tomlData := []byte(`
prev_room = ["alt+k", "ctrl+up"]
next_room = ["alt+j"]
`)

	km, err := ParseKeyMap(tomlData)
	if err != nil {
		t.Fatalf("ParseKeyMap: %v", err)
	}

	// Multiple keys per action should all match.
	msg := tea.KeyPressMsg{Code: 'k', Mod: tea.ModAlt}
	if !key.Matches(msg, km.PrevRoom) {
		t.Error("PrevRoom should match alt+k after override")
	}
	msg = tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl}
	if !key.Matches(msg, km.PrevRoom) {
		t.Error("PrevRoom should also match ctrl+up (multiple keys)")
	}

	// Non-overridden bindings should keep defaults.
	msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	if !key.Matches(msg, km.Quit) {
		t.Error("Quit should still match ctrl+c (not overridden)")
	}
}

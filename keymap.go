package main

import (
	"fmt"
	"os"
	"path/filepath"

	"charm.land/bubbles/v2/key"
	"github.com/BurntSushi/toml"
)

// KeyMap holds the configurable keybindings. Only bindings that commonly
// conflict with terminal emulators or that users want to remap (e.g. vim-style
// navigation) are exposed here. Everything else (enter to send, tab for
// autocomplete, esc to dismiss, up/down in popups) stays hardcoded.
type KeyMap struct {
	Quit            key.Binding
	PrevRoom        key.Binding
	NextRoom        key.Binding
	ChannelSelector key.Binding
	ScrollUp        key.Binding
	ScrollDown      key.Binding
}

// DefaultKeyMap returns the default keybindings matching the original
// hardcoded behavior.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
		PrevRoom: key.NewBinding(
			key.WithKeys("ctrl+up"),
			key.WithHelp("ctrl+up", "previous room"),
		),
		NextRoom: key.NewBinding(
			key.WithKeys("ctrl+down"),
			key.WithHelp("ctrl+down", "next room"),
		),
		ChannelSelector: key.NewBinding(
			key.WithKeys("ctrl+k"),
			key.WithHelp("ctrl+k", "channel selector"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdown", "scroll down"),
		),
	}
}

// keymapTOML is the on-disk representation of user keybinding overrides.
type keymapTOML struct {
	Quit            []string `toml:"quit"`
	PrevRoom        []string `toml:"prev_room"`
	NextRoom        []string `toml:"next_room"`
	ChannelSelector []string `toml:"channel_selector"`
	ScrollUp        []string `toml:"scroll_up"`
	ScrollDown      []string `toml:"scroll_down"`
}

// keymapPath returns the path to the keybindings config file.
func keymapPath(cfgFlagPath string) string {
	if p := os.Getenv("NITROUS_KEYBINDINGS"); p != "" {
		return p
	}
	dir := filepath.Dir(configPath(cfgFlagPath))
	return filepath.Join(dir, "keybindings.toml")
}

// LoadKeyMap reads keybindings from the config file. Missing file returns
// defaults. Empty arrays for a binding are rejected as errors.
func LoadKeyMap(cfgFlagPath string) (KeyMap, error) {
	km := DefaultKeyMap()

	path := keymapPath(cfgFlagPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return km, nil
		}
		return km, fmt.Errorf("reading keybindings: %w", err)
	}

	return ParseKeyMap(data)
}

// ParseKeyMap parses keybinding overrides from TOML bytes, applying them on
// top of the defaults.
func ParseKeyMap(data []byte) (KeyMap, error) {
	km := DefaultKeyMap()

	var raw keymapTOML
	if err := toml.Unmarshal(data, &raw); err != nil {
		return km, fmt.Errorf("parsing keybindings: %w", err)
	}

	if err := applyOverride(&km.Quit, raw.Quit, "quit"); err != nil {
		return km, err
	}
	if err := applyOverride(&km.PrevRoom, raw.PrevRoom, "prev_room"); err != nil {
		return km, err
	}
	if err := applyOverride(&km.NextRoom, raw.NextRoom, "next_room"); err != nil {
		return km, err
	}
	if err := applyOverride(&km.ChannelSelector, raw.ChannelSelector, "channel_selector"); err != nil {
		return km, err
	}
	if err := applyOverride(&km.ScrollUp, raw.ScrollUp, "scroll_up"); err != nil {
		return km, err
	}
	if err := applyOverride(&km.ScrollDown, raw.ScrollDown, "scroll_down"); err != nil {
		return km, err
	}

	return km, nil
}

// applyOverride replaces a key.Binding's keys if the user provided a
// non-nil, non-empty override.
func applyOverride(binding *key.Binding, keys []string, name string) error {
	if keys == nil {
		return nil // not specified in config, keep default
	}
	if len(keys) == 0 {
		return fmt.Errorf("keybinding %q: empty key list", name)
	}
	help := binding.Help()
	*binding = key.NewBinding(
		key.WithKeys(keys...),
		key.WithHelp(keys[0], help.Desc),
	)
	return nil
}

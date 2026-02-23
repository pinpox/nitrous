package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type ProfileConfig struct {
	Name        string `toml:"name"`
	DisplayName string `toml:"display_name"`
	About       string `toml:"about"`
	Picture     string `toml:"picture"`
}

type Config struct {
	Relays      []string      `toml:"relays"`
	DisplayName string        `toml:"display_name"`
	MaxMessages int           `toml:"max_messages"`
	Bookmarks   []string      `toml:"bookmarks"`
	Profile     ProfileConfig `toml:"profile"`
}

// Room maps a human-readable name to a kind-40 event ID.
type Room struct {
	Name string
	ID   string
}

func defaultConfig() Config {
	return Config{
		Relays: []string{
			"wss://nostr.0cx.de",
		},
		DisplayName: "anon",
		MaxMessages: 500,
	}
}

func configPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if p := os.Getenv("NITROUS_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "nitrous", "config.toml")
}

func LoadConfig(flagPath string) (Config, error) {
	cfg := defaultConfig()

	path := configPath(flagPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}

	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 500
	}
	if len(cfg.Relays) == 0 {
		cfg.Relays = defaultConfig().Relays
	}

	// Backward compat: copy top-level display_name into profile if not set.
	if cfg.Profile.DisplayName == "" && cfg.DisplayName != "" {
		cfg.Profile.DisplayName = cfg.DisplayName
	}

	return cfg, nil
}

// roomsPath returns the path to the rooms file, in the same directory as the config.
func roomsPath(cfgFlagPath string) string {
	dir := filepath.Dir(configPath(cfgFlagPath))
	return filepath.Join(dir, "rooms")
}

// LoadRooms reads the rooms file. Each line is "name event_id".
// Returns an empty slice if the file doesn't exist.
func LoadRooms(cfgFlagPath string) ([]Room, error) {
	path := roomsPath(cfgFlagPath)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var rooms []Room
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		id := strings.TrimSpace(parts[1])
		if name != "" && id != "" {
			rooms = append(rooms, Room{Name: name, ID: id})
		}
	}
	return rooms, scanner.Err()
}

// AppendRoom adds a room to the rooms file. Creates the file and parent dirs if needed.
func AppendRoom(cfgFlagPath string, room Room) error {
	path := roomsPath(cfgFlagPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", room.Name, room.ID)
	return err
}

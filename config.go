package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"fiatjaf.com/nostr"
)

type ProfileConfig struct {
	Name        string `toml:"name"`
	DisplayName string `toml:"display_name"`
	About       string `toml:"about"`
	Picture     string `toml:"picture"`
}

type Config struct {
	Relays         []string      `toml:"relays"`
	GroupRelay     string        `toml:"group_relay"`
	BlossomServers []string      `toml:"blossom_servers"`
	PrivateKeyFile string        `toml:"private_key_file"`
	MaxMessages    int           `toml:"max_messages"`
	Logging        *bool         `toml:"logging"`        // nil = default (true)
	LogDir         string        `toml:"log_dir"`
	Profile        ProfileConfig `toml:"profile"`
}

// LoggingEnabled returns whether message logging is enabled.
func (c Config) LoggingEnabled() bool {
	if c.Logging == nil {
		return true // enabled by default
	}
	return *c.Logging
}

func defaultConfig() Config {
	return Config{
		Relays: []string{
			"wss://relay.damus.io",
			"wss://relay.nostr.band",
			"wss://nos.lol",
		},
		BlossomServers: []string{
			"https://blossom.nostr.build",
		},
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
	if cfg.Profile.Name == "" {
		cfg.Profile.Name = os.Getenv("USER")
	}
	if cfg.Profile.DisplayName == "" {
		cfg.Profile.DisplayName = os.Getenv("USER")
	}

	return cfg, nil
}

// lastDMSeenPath returns the path to the last_dm_seen timestamp file.
func lastDMSeenPath(cfgFlagPath string) string {
	dir := filepath.Dir(configPath(cfgFlagPath))
	return filepath.Join(dir, "last_dm_seen")
}

// LoadLastDMSeen reads the last-seen DM timestamp from disk.
// Returns 7 days ago if the file is missing or unreadable.
func LoadLastDMSeen(cfgFlagPath string) nostr.Timestamp {
	fallback := nostr.Timestamp(time.Now().Add(-7 * 24 * time.Hour).Unix())
	data, err := os.ReadFile(lastDMSeenPath(cfgFlagPath))
	if err != nil {
		return fallback
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return fallback
	}
	return nostr.Timestamp(v)
}

// SaveLastDMSeen writes the last-seen DM timestamp to disk.
func SaveLastDMSeen(cfgFlagPath string, ts nostr.Timestamp) error {
	path := lastDMSeenPath(cfgFlagPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(int64(ts), 10)+"\n"), 0644)
}

// Contact maps a display name to a hex pubkey.
type Contact struct {
	Name   string
	PubKey string
}

// SavedGroup maps a display name to a relay URL and group ID (NIP-29).
type SavedGroup struct {
	Name     string
	RelayURL string
	GroupID  string
}



package main

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Relays      []string `toml:"relays"`
	DisplayName string   `toml:"display_name"`
	MaxMessages int      `toml:"max_messages"`
	Bookmarks   []string `toml:"bookmarks"`
}

func defaultConfig() Config {
	return Config{
		Relays: []string{
			"wss://nostr.0cx.de",
			"wss://relay.damus.io",
			"wss://nos.lol",
		},
		DisplayName: "anon",
		MaxMessages: 500,
	}
}

func configPath() string {
	if p := os.Getenv("NITROUS_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "nitrous", "config.toml")
}

func LoadConfig() (Config, error) {
	cfg := defaultConfig()

	path := configPath()
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

	return cfg, nil
}

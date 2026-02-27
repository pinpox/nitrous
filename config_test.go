package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()

	if len(cfg.Relays) == 0 {
		t.Fatal("expected default relays, got empty")
	}
	if cfg.Relays[0] != "wss://relay.damus.io" {
		t.Errorf("first default relay = %q, want %q", cfg.Relays[0], "wss://relay.damus.io")
	}
	if cfg.MaxMessages != 500 {
		t.Errorf("MaxMessages = %d, want 500", cfg.MaxMessages)
	}
	if len(cfg.BlossomServers) == 0 {
		t.Fatal("expected default blossom servers, got empty")
	}
	if cfg.BlossomServers[0] != "https://blossom.nostr.build" {
		t.Errorf("first blossom server = %q, want %q", cfg.BlossomServers[0], "https://blossom.nostr.build")
	}
}

func TestConfigPath(t *testing.T) {
	t.Run("flag takes priority", func(t *testing.T) {
		got := configPath("/my/flag/path.toml")
		if got != "/my/flag/path.toml" {
			t.Errorf("configPath with flag = %q, want %q", got, "/my/flag/path.toml")
		}
	})

	t.Run("env var when no flag", func(t *testing.T) {
		t.Setenv("NITROUS_CONFIG", "/env/path.toml")
		got := configPath("")
		if got != "/env/path.toml" {
			t.Errorf("configPath with env = %q, want %q", got, "/env/path.toml")
		}
	})

	t.Run("default when no flag or env", func(t *testing.T) {
		t.Setenv("NITROUS_CONFIG", "")
		got := configPath("")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("os.UserHomeDir() failed: %v", err)
		}
		want := filepath.Join(home, ".config", "nitrous", "config.toml")
		if got != want {
			t.Errorf("configPath default = %q, want %q", got, want)
		}
	})
}

func TestLoadConfig(t *testing.T) {
	t.Run("missing file returns defaults", func(t *testing.T) {
		dir := t.TempDir()
		flagPath := filepath.Join(dir, "nonexistent.toml")
		cfg, err := LoadConfig(flagPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.MaxMessages != 500 {
			t.Errorf("MaxMessages = %d, want 500", cfg.MaxMessages)
		}
		if len(cfg.Relays) == 0 {
			t.Error("expected default relays")
		}
	})

	t.Run("valid TOML parses", func(t *testing.T) {
		dir := t.TempDir()
		cfgFile := filepath.Join(dir, "config.toml")
		content := `
relays = ["wss://custom.relay"]
max_messages = 100

[profile]
name = "testuser"
display_name = "Test User"
`
		if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Relays) != 1 || cfg.Relays[0] != "wss://custom.relay" {
			t.Errorf("relays = %v, want [wss://custom.relay]", cfg.Relays)
		}
		if cfg.MaxMessages != 100 {
			t.Errorf("MaxMessages = %d, want 100", cfg.MaxMessages)
		}
		if cfg.Profile.Name != "testuser" {
			t.Errorf("Profile.Name = %q, want %q", cfg.Profile.Name, "testuser")
		}
		if cfg.Profile.DisplayName != "Test User" {
			t.Errorf("Profile.DisplayName = %q, want %q", cfg.Profile.DisplayName, "Test User")
		}
	})

	t.Run("empty relays get defaults", func(t *testing.T) {
		dir := t.TempDir()
		cfgFile := filepath.Join(dir, "config.toml")
		content := `relays = []`
		if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defaults := defaultConfig()
		if len(cfg.Relays) != len(defaults.Relays) {
			t.Errorf("expected default relays when empty, got %d relays", len(cfg.Relays))
		}
	})

	t.Run("zero max_messages gets default", func(t *testing.T) {
		dir := t.TempDir()
		cfgFile := filepath.Join(dir, "config.toml")
		content := `max_messages = 0`
		if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.MaxMessages != 500 {
			t.Errorf("MaxMessages = %d, want 500 (default)", cfg.MaxMessages)
		}
	})
}

func TestLoadLastDMSeenAndSaveLastDMSeen(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Missing file returns ~7 days ago.
	ts := LoadLastDMSeen(cfgFile)
	sevenDaysAgo := nostr.Timestamp(time.Now().Add(-7 * 24 * time.Hour).Unix())
	// Allow 10 seconds of drift.
	diff := int64(ts) - int64(sevenDaysAgo)
	if diff < -10 || diff > 10 {
		t.Errorf("expected ~7 days ago, got diff of %d seconds", diff)
	}

	// Save and reload.
	want := nostr.Timestamp(1234567890)
	if err := SaveLastDMSeen(cfgFile, want); err != nil {
		t.Fatal(err)
	}
	got := LoadLastDMSeen(cfgFile)
	if got != want {
		t.Errorf("LoadLastDMSeen = %d, want %d", got, want)
	}
}

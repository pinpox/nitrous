package main

import (
	"fmt"
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
		home, _ := os.UserHomeDir()
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

func TestLoadRoomsAndAppendRoom(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	// Create config so roomsPath resolves correctly.
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Missing file returns nil.
	rooms, err := LoadRooms(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rooms != nil {
		t.Errorf("expected nil rooms for missing file, got %v", rooms)
	}

	// Append rooms.
	if err := AppendRoom(cfgFile, Room{Name: "general", ID: "aaa111"}); err != nil {
		t.Fatal(err)
	}
	if err := AppendRoom(cfgFile, Room{Name: "dev", ID: "bbb222"}); err != nil {
		t.Fatal(err)
	}

	rooms, err = LoadRooms(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 {
		t.Fatalf("expected 2 rooms, got %d", len(rooms))
	}
	if rooms[0].Name != "general" || rooms[0].ID != "aaa111" {
		t.Errorf("room[0] = %+v, want {general aaa111}", rooms[0])
	}
	if rooms[1].Name != "dev" || rooms[1].ID != "bbb222" {
		t.Errorf("room[1] = %+v, want {dev bbb222}", rooms[1])
	}
}

func TestRemoveRoom(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	_ = AppendRoom(cfgFile, Room{Name: "a", ID: "111"})
	_ = AppendRoom(cfgFile, Room{Name: "b", ID: "222"})
	_ = AppendRoom(cfgFile, Room{Name: "c", ID: "333"})

	if err := RemoveRoom(cfgFile, "222"); err != nil {
		t.Fatal(err)
	}

	rooms, _ := LoadRooms(cfgFile)
	if len(rooms) != 2 {
		t.Fatalf("expected 2 rooms after remove, got %d", len(rooms))
	}
	for _, r := range rooms {
		if r.ID == "222" {
			t.Error("room 222 should have been removed")
		}
	}

	// Removing nonexistent is a no-op.
	if err := RemoveRoom(cfgFile, "nonexistent"); err != nil {
		t.Fatalf("unexpected error removing nonexistent: %v", err)
	}
}

func TestLoadRoomsSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	roomsFile := filepath.Join(dir, "rooms")
	content := "# comment\n\ngeneral abc123\n  \n# another comment\ndev def456\n"
	if err := os.WriteFile(roomsFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	rooms, err := LoadRooms(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 {
		t.Fatalf("expected 2 rooms, got %d", len(rooms))
	}
}

func TestLoadContactsAndAppendContact(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Missing file returns nil.
	contacts, err := LoadContacts(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if contacts != nil {
		t.Errorf("expected nil, got %v", contacts)
	}

	// Append contacts.
	_ = AppendContact(cfgFile, Contact{Name: "alice", PubKey: "pk_alice"})
	_ = AppendContact(cfgFile, Contact{Name: "bob", PubKey: "pk_bob"})

	contacts, err = LoadContacts(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts, got %d", len(contacts))
	}

	// Dedup: appending same pubkey is a no-op.
	_ = AppendContact(cfgFile, Contact{Name: "alice2", PubKey: "pk_alice"})
	contacts, _ = LoadContacts(cfgFile)
	if len(contacts) != 2 {
		t.Errorf("expected 2 contacts after dedup, got %d", len(contacts))
	}
}

func TestRemoveContact(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	_ = AppendContact(cfgFile, Contact{Name: "alice", PubKey: "pk_a"})
	_ = AppendContact(cfgFile, Contact{Name: "bob", PubKey: "pk_b"})

	if err := RemoveContact(cfgFile, "pk_a"); err != nil {
		t.Fatal(err)
	}

	contacts, _ := LoadContacts(cfgFile)
	if len(contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(contacts))
	}
	if contacts[0].PubKey != "pk_b" {
		t.Errorf("expected pk_b, got %q", contacts[0].PubKey)
	}
}

func TestUpdateContactName(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	_ = AppendContact(cfgFile, Contact{Name: "alice", PubKey: "pk_a"})
	_ = AppendContact(cfgFile, Contact{Name: "bob", PubKey: "pk_b"})

	if err := UpdateContactName(cfgFile, "pk_a", "Alice_Updated"); err != nil {
		t.Fatal(err)
	}

	contacts, _ := LoadContacts(cfgFile)
	if contacts[0].Name != "Alice_Updated" {
		t.Errorf("expected Alice_Updated, got %q", contacts[0].Name)
	}
	if contacts[1].Name != "bob" {
		t.Errorf("expected bob unchanged, got %q", contacts[1].Name)
	}

	// No-op when name is already correct.
	if err := UpdateContactName(cfgFile, "pk_a", "Alice_Updated"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSavedGroupsAndAppendSavedGroup(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Missing file returns nil.
	groups, err := LoadSavedGroups(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if groups != nil {
		t.Errorf("expected nil, got %v", groups)
	}

	// Append groups.
	g1 := SavedGroup{Name: "chat", RelayURL: "wss://relay1.com", GroupID: "g1"}
	g2 := SavedGroup{Name: "dev", RelayURL: "wss://relay2.com", GroupID: "g2"}
	_ = AppendSavedGroup(cfgFile, g1)
	_ = AppendSavedGroup(cfgFile, g2)

	groups, _ = LoadSavedGroups(cfgFile)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	// Dedup: same relay+group is a no-op.
	_ = AppendSavedGroup(cfgFile, SavedGroup{Name: "chat2", RelayURL: "wss://relay1.com", GroupID: "g1"})
	groups, _ = LoadSavedGroups(cfgFile)
	if len(groups) != 2 {
		t.Errorf("expected 2 groups after dedup, got %d", len(groups))
	}
}

func TestRemoveSavedGroup(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	_ = AppendSavedGroup(cfgFile, SavedGroup{Name: "a", RelayURL: "r1", GroupID: "g1"})
	_ = AppendSavedGroup(cfgFile, SavedGroup{Name: "b", RelayURL: "r2", GroupID: "g2"})

	if err := RemoveSavedGroup(cfgFile, "r1", "g1"); err != nil {
		t.Fatal(err)
	}

	groups, _ := LoadSavedGroups(cfgFile)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].GroupID != "g2" {
		t.Errorf("expected g2, got %q", groups[0].GroupID)
	}
}

func TestUpdateSavedGroupName(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	_ = AppendSavedGroup(cfgFile, SavedGroup{Name: "old", RelayURL: "r1", GroupID: "g1"})

	if err := UpdateSavedGroupName(cfgFile, "r1", "g1", "new"); err != nil {
		t.Fatal(err)
	}

	groups, _ := LoadSavedGroups(cfgFile)
	if groups[0].Name != "new" {
		t.Errorf("expected name 'new', got %q", groups[0].Name)
	}
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

func TestLoadRoomsCRUDRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create several rooms.
	for i := 0; i < 5; i++ {
		_ = AppendRoom(cfgFile, Room{
			Name: fmt.Sprintf("room%d", i),
			ID:   fmt.Sprintf("id%d", i),
		})
	}

	// Remove middle one.
	_ = RemoveRoom(cfgFile, "id2")

	rooms, _ := LoadRooms(cfgFile)
	if len(rooms) != 4 {
		t.Fatalf("expected 4 rooms, got %d", len(rooms))
	}
	for _, r := range rooms {
		if r.ID == "id2" {
			t.Error("id2 should have been removed")
		}
	}
}

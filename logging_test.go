package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"plain text", "hello world"},
		{"with newline", "hello\nworld"},
		{"with multiple newlines", "a\nb\nc"},
		{"with literal backslash-n", `hello\nworld`},
		{"with backslash", `path\to\file`},
		{"with both", "line1\nline2\\nline3"},
		{"empty", ""},
		{"only newline", "\n"},
		{"only backslash", `\`},
		{"trailing newline", "hello\n"},
		{"double backslash", `\\`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			escaped := escapeContent(tt.input)
			// Escaped form must not contain actual newlines.
			if strings.Contains(escaped, "\n") {
				t.Errorf("escaped contains newline: %q", escaped)
			}
			got := unescapeContent(escaped)
			if got != tt.input {
				t.Errorf("round-trip failed:\n  input:     %q\n  escaped:   %q\n  unescaped: %q", tt.input, escaped, got)
			}
		})
	}
}

func TestLogFilePath(t *testing.T) {
	got := logFilePath("/tmp/logs", "channel", "abc123")
	if got != "/tmp/logs/channel_abc123.log" {
		t.Errorf("unexpected path: %s", got)
	}

	// Group keys contain tabs and colons — should be sanitized.
	got = logFilePath("/tmp/logs", "group", "ws://relay.example.com\tgroupid")
	if strings.ContainsAny(got, "\t:/") && !strings.HasPrefix(got, "/") {
		t.Errorf("path contains unsafe characters: %s", got)
	}
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()

	msgs := []ChatMessage{
		{Timestamp: nostr.Timestamp(1700000001), EventID: "aabbccdd11223344", PubKey: "1122334455667788", Author: "alice", Content: "hello world"},
		{Timestamp: nostr.Timestamp(1700000002), EventID: "eeff001122334455", PubKey: "aabbccddeeff0011", Author: "bob", Content: "hello\nworld"},
		{Timestamp: nostr.Timestamp(1700000003), EventID: "1111222233334444", PubKey: "5555666677778888", Author: "charlie", Content: `literal\nescaped`},
	}

	for _, msg := range msgs {
		appendLogEntry(dir, "channel", "testroom", msg, msg.Author)
	}

	// Verify file exists.
	path := logFilePath(dir, "channel", "testroom")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file not created: %v", err)
	}

	// Load and verify.
	loaded, err := loadLogHistory(dir, "channel", "testroom", 100)
	if err != nil {
		t.Fatalf("loadLogHistory: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(loaded))
	}

	for i, msg := range msgs {
		got := loaded[i]
		if got.Content != msg.Content {
			t.Errorf("msg[%d] content: got %q, want %q", i, got.Content, msg.Content)
		}
		if got.Timestamp != msg.Timestamp {
			t.Errorf("msg[%d] timestamp: got %d, want %d", i, got.Timestamp, msg.Timestamp)
		}
		if got.Author != msg.Author {
			t.Errorf("msg[%d] author: got %q, want %q", i, got.Author, msg.Author)
		}
		if got.EventID != msg.EventID {
			t.Errorf("msg[%d] eventID: got %q, want %q", i, got.EventID, msg.EventID)
		}
		if got.PubKey != msg.PubKey {
			t.Errorf("msg[%d] pubkey: got %q, want %q", i, got.PubKey, msg.PubKey)
		}
	}
}

func TestLoadMaxMessages(t *testing.T) {
	dir := t.TempDir()

	// Write 100 messages.
	for i := 0; i < 100; i++ {
		msg := ChatMessage{
			Timestamp: nostr.Timestamp(1700000000 + int64(i)),
			EventID:   "abcdef0012345678",
			PubKey:    "1234567890abcdef",
			Author:    "user",
			Content:   "message",
		}
		appendLogEntry(dir, "channel", "testroom", msg, "user")
	}

	// Load only last 10.
	loaded, err := loadLogHistory(dir, "channel", "testroom", 10)
	if err != nil {
		t.Fatalf("loadLogHistory: %v", err)
	}
	if len(loaded) != 10 {
		t.Fatalf("expected 10 messages, got %d", len(loaded))
	}

	// Should be the last 10 (timestamps 90-99).
	if loaded[0].Timestamp != nostr.Timestamp(1700000090) {
		t.Errorf("first loaded message timestamp: got %d, want %d", loaded[0].Timestamp, 1700000090)
	}
	if loaded[9].Timestamp != nostr.Timestamp(1700000099) {
		t.Errorf("last loaded message timestamp: got %d, want %d", loaded[9].Timestamp, 1700000099)
	}
}

func TestLoadLargeFile(t *testing.T) {
	dir := t.TempDir()

	// Write 100K messages directly to file for speed.
	path := logFilePath(dir, "channel", "bigroom")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100000; i++ {
		f.WriteString("2024-01-15 10:30:45\tabcdef00\t12345678\tuser\tmessage number " + filepath.Base(dir) + "\n")
	}
	f.Close()

	// Loading last 500 should be fast and correct.
	loaded, err := loadLogHistory(dir, "channel", "bigroom", 500)
	if err != nil {
		t.Fatalf("loadLogHistory: %v", err)
	}
	if len(loaded) != 500 {
		t.Fatalf("expected 500 messages, got %d", len(loaded))
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	loaded, err := loadLogHistory(dir, "channel", "noroom", 100)
	if err != nil {
		t.Fatalf("expected nil error for non-existent file, got: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(loaded))
	}
}

func TestAppendWithEmptyLogDir(t *testing.T) {
	// Should be a no-op, not panic.
	appendLogEntry("", "channel", "room", ChatMessage{Content: "test"}, "user")
}

func TestLoadWithEmptyLogDir(t *testing.T) {
	loaded, err := loadLogHistory("", "channel", "room", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(loaded))
	}
}

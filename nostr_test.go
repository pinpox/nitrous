package main

import (
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestShortPK(t *testing.T) {
	tests := []struct {
		name string
		pk   string
		want string
	}{
		{"normal 64-char key", "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", "abcdef12"},
		{"exactly 8 chars", "abcdef12", "abcdef12"},
		{"shorter than 8", "abc", "abc"},
		{"empty string", "", ""},
		{"9 chars truncated", "123456789", "12345678"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortPK(tt.pk)
			if got != tt.want {
				t.Errorf("shortPK(%q) = %q, want %q", tt.pk, got, tt.want)
			}
		})
	}
}

func TestGroupKey(t *testing.T) {
	relay := "wss://relay.example.com"
	group := "abc123"
	got := groupKey(relay, group)
	want := "wss://relay.example.com\tabc123"
	if got != want {
		t.Errorf("groupKey(%q, %q) = %q, want %q", relay, group, got, want)
	}
}

func TestSplitGroupKey(t *testing.T) {
	tests := []struct {
		name      string
		gk        string
		wantRelay string
		wantGroup string
	}{
		{"normal", "wss://relay.example.com\tabc123", "wss://relay.example.com", "abc123"},
		{"no tab", "notabhere", "", "notabhere"},
		{"empty", "", "", ""},
		{"multiple tabs", "a\tb\tc", "a", "b\tc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, group := splitGroupKey(tt.gk)
			if relay != tt.wantRelay || group != tt.wantGroup {
				t.Errorf("splitGroupKey(%q) = (%q, %q), want (%q, %q)",
					tt.gk, relay, group, tt.wantRelay, tt.wantGroup)
			}
		})
	}
}

func TestGroupKeyRoundtrip(t *testing.T) {
	relay := "wss://groups.example.com"
	group := "deadbeef"
	gk := groupKey(relay, group)
	gotRelay, gotGroup := splitGroupKey(gk)
	if gotRelay != relay || gotGroup != group {
		t.Errorf("roundtrip failed: got (%q, %q), want (%q, %q)",
			gotRelay, gotGroup, relay, group)
	}
}

func TestParseGroupInput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantRelay string
		wantGroup string
		wantErr   bool
	}{
		{
			name:      "host'groupid format",
			input:     "groups.example.com'abc123",
			wantRelay: "wss://groups.example.com",
			wantGroup: "abc123",
		},
		{
			name:    "invalid format",
			input:   "not-a-valid-input",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid naddr",
			input:   "naddrinvalid",
			wantErr: true,
		},
	}

	// Test valid naddr encoding: build one and parse it back.
	naddr, err := nip19.EncodeEntity(
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		39000,
		"testgroup",
		[]string{"wss://relay.test.com"},
	)
	if err != nil {
		t.Fatalf("failed to encode naddr for test: %v", err)
	}
	tests = append(tests, struct {
		name      string
		input     string
		wantRelay string
		wantGroup string
		wantErr   bool
	}{
		name:      "valid naddr",
		input:     naddr,
		wantRelay: "wss://relay.test.com",
		wantGroup: "testgroup",
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, group, err := parseGroupInput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseGroupInput(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGroupInput(%q) unexpected error: %v", tt.input, err)
			}
			if relay != tt.wantRelay {
				t.Errorf("relay = %q, want %q", relay, tt.wantRelay)
			}
			if group != tt.wantGroup {
				t.Errorf("group = %q, want %q", group, tt.wantGroup)
			}
		})
	}
}

func TestPickPreviousTags(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		tags := pickPreviousTags(nil)
		if tags != nil {
			t.Errorf("expected nil, got %v", tags)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		tags := pickPreviousTags([]string{})
		if tags != nil {
			t.Errorf("expected nil, got %v", tags)
		}
	})

	t.Run("fewer than 3", func(t *testing.T) {
		tags := pickPreviousTags([]string{"aaa", "bbb"})
		if len(tags) != 2 {
			t.Fatalf("expected 2 tags, got %d", len(tags))
		}
		for _, tag := range tags {
			if tag[0] != "previous" {
				t.Errorf("expected tag key 'previous', got %q", tag[0])
			}
		}
	})

	t.Run("exactly 3", func(t *testing.T) {
		tags := pickPreviousTags([]string{"aaa", "bbb", "ccc"})
		if len(tags) != 3 {
			t.Fatalf("expected 3 tags, got %d", len(tags))
		}
	})

	t.Run("more than 3 returns exactly 3", func(t *testing.T) {
		ids := []string{"a", "b", "c", "d", "e", "f"}
		tags := pickPreviousTags(ids)
		if len(tags) != 3 {
			t.Fatalf("expected 3 tags, got %d", len(tags))
		}
	})

	t.Run("long IDs truncated to 8 chars", func(t *testing.T) {
		longID := "abcdef1234567890abcdef"
		tags := pickPreviousTags([]string{longID})
		if len(tags) != 1 {
			t.Fatalf("expected 1 tag, got %d", len(tags))
		}
		if len(tags[0][1]) > 8 {
			t.Errorf("expected ID truncated to 8 chars, got %q (len=%d)", tags[0][1], len(tags[0][1]))
		}
	})

	t.Run("short IDs not truncated", func(t *testing.T) {
		tags := pickPreviousTags([]string{"abc"})
		if tags[0][1] != "abc" {
			t.Errorf("expected %q, got %q", "abc", tags[0][1])
		}
	})
}

func TestContainsStr(t *testing.T) {
	tests := []struct {
		name string
		sl   []string
		s    string
		want bool
	}{
		{"found", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty slice", []string{}, "a", false},
		{"nil slice", nil, "a", false},
		{"empty string found", []string{""}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsStr(tt.sl, tt.s)
			if got != tt.want {
				t.Errorf("containsStr(%v, %q) = %v, want %v", tt.sl, tt.s, got, tt.want)
			}
		})
	}
}

func TestSlicesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different content", []string{"a", "b"}, []string{"a", "c"}, false},
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"nil vs empty", nil, []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slicesEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("slicesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

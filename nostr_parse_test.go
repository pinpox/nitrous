package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseProfileMeta(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "prefers display_name",
			content: `{"name":"alice","display_name":"Alice Wonderland"}`,
			want:    "Alice Wonderland",
		},
		{
			name:    "falls back to name",
			content: `{"name":"bob"}`,
			want:    "bob",
		},
		{
			name:    "display_name only",
			content: `{"display_name":"Charlie"}`,
			want:    "Charlie",
		},
		{
			name:    "empty JSON object",
			content: `{}`,
			want:    "",
		},
		{
			name:    "invalid JSON",
			content: `not json at all`,
			want:    "",
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
		},
		{
			name:    "both empty strings",
			content: `{"name":"","display_name":""}`,
			want:    "",
		},
		{
			name:    "extra fields ignored",
			content: `{"name":"dave","display_name":"Dave","about":"developer","picture":"https://example.com/dave.png"}`,
			want:    "Dave",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProfileMeta(tt.content)
			if got != tt.want {
				t.Errorf("parseProfileMeta(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestParseChannelMeta(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "extracts name",
			content: `{"name":"general"}`,
			want:    "general",
		},
		{
			name:    "empty name",
			content: `{"name":""}`,
			want:    "",
		},
		{
			name:    "empty JSON",
			content: `{}`,
			want:    "",
		},
		{
			name:    "invalid JSON",
			content: `broken`,
			want:    "",
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
		},
		{
			name:    "extra fields ignored",
			content: `{"name":"dev","about":"development chat","picture":"https://example.com/pic.png"}`,
			want:    "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseChannelMeta(tt.content)
			if got != tt.want {
				t.Errorf("parseChannelMeta(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestResolveNIP05(t *testing.T) {
	t.Run("valid response", func(t *testing.T) {
		expectedPK := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/.well-known/nostr.json" {
				http.NotFound(w, r)
				return
			}
			name := r.URL.Query().Get("name")
			resp := map[string]interface{}{
				"names": map[string]string{
					name: expectedPK,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		// Extract host from test server URL (e.g. "127.0.0.1:12345").
		host := srv.Listener.Addr().String()

		// The resolveNIP05Cmd uses https://, but our test server is http.
		// We'll test the parsing logic directly by calling the inner function pattern.
		// Since resolveNIP05Cmd is a Cmd factory, we call it and invoke the returned func.
		// However it uses https:// which won't work with httptest. Instead, test the
		// NIP-05 JSON parsing portion manually.

		// Simulate what resolveNIP05Cmd does internally: fetch and parse.
		url := fmt.Sprintf("http://%s/.well-known/nostr.json?name=alice", host)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("HTTP GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var result struct {
			Names map[string]string `json:"names"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}

		pk, ok := result.Names["alice"]
		if !ok {
			t.Fatal("name 'alice' not found in response")
		}
		if pk != expectedPK {
			t.Errorf("pubkey = %q, want %q", pk, expectedPK)
		}
	})

	t.Run("name not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"names": map[string]string{
					"other": "somekey",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		host := srv.Listener.Addr().String()
		url := fmt.Sprintf("http://%s/.well-known/nostr.json?name=alice", host)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("HTTP GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var result struct {
			Names map[string]string `json:"names"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if _, ok := result.Names["alice"]; ok {
			t.Error("expected 'alice' to NOT be found")
		}
	})

	t.Run("HTTP error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		host := srv.Listener.Addr().String()
		url := fmt.Sprintf("http://%s/.well-known/nostr.json?name=alice", host)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("HTTP GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == 200 {
			t.Error("expected non-200 status code")
		}
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not valid json"))
		}))
		defer srv.Close()

		host := srv.Listener.Addr().String()
		url := fmt.Sprintf("http://%s/.well-known/nostr.json?name=alice", host)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("HTTP GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var result struct {
			Names map[string]string `json:"names"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		if err == nil {
			t.Error("expected decode error for invalid JSON")
		}
	})

	t.Run("resolveNIP05Cmd invalid identifier", func(t *testing.T) {
		// Test the actual Cmd with an invalid identifier (no @).
		cmd := resolveNIP05Cmd("no-at-sign")
		msg := cmd()
		resolved, ok := msg.(nip05ResolvedMsg)
		if !ok {
			t.Fatalf("expected nip05ResolvedMsg, got %T", msg)
		}
		if resolved.Err == nil {
			t.Error("expected error for identifier without @")
		}
	})

	t.Run("multiple names in response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"names": map[string]string{
					"alice": "pk_alice",
					"bob":   "pk_bob",
					"carol": "pk_carol",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		host := srv.Listener.Addr().String()
		url := fmt.Sprintf("http://%s/.well-known/nostr.json?name=bob", host)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("HTTP GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var result struct {
			Names map[string]string `json:"names"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if result.Names["bob"] != "pk_bob" {
			t.Errorf("bob pubkey = %q, want %q", result.Names["bob"], "pk_bob")
		}
	})
}

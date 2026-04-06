package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"fiatjaf.com/nostr"
)

func TestIsFilePath(t *testing.T) {
	t.Run("absolute existing file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
		if !isFilePath(f) {
			t.Errorf("expected true for existing file %q", f)
		}
	})

	t.Run("nonexistent absolute path", func(t *testing.T) {
		if isFilePath("/nonexistent/path/to/file.txt") {
			t.Error("expected false for nonexistent file")
		}
	})

	t.Run("relative path returns false", func(t *testing.T) {
		// Even if the file exists, relative paths should return false.
		if isFilePath("go.mod") {
			t.Error("expected false for relative path")
		}
	})

	t.Run("multiline returns false", func(t *testing.T) {
		if isFilePath("/some/path\nwith newline") {
			t.Error("expected false for multiline string")
		}
	})

	t.Run("directory returns false", func(t *testing.T) {
		dir := t.TempDir()
		if isFilePath(dir) {
			t.Error("expected false for directory")
		}
	})

	t.Run("empty string returns false", func(t *testing.T) {
		if isFilePath("") {
			t.Error("expected false for empty string")
		}
	})

	t.Run("tilde path nonexistent", func(t *testing.T) {
		// We can't reliably test ~/ expansion in CI, but we can verify non-existent ~/path returns false.
		if isFilePath("~/nonexistent_test_file_12345") {
			t.Error("expected false for nonexistent ~/path")
		}
	})
}

func TestBlossomUploadCmd_EncryptsBeforeUpload(t *testing.T) {
	t.Parallel()

	plaintext := []byte("this is plaintext that should be encrypted before upload")

	// Create a temp file with plaintext content.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.png")
	if err := os.WriteFile(filePath, plaintext, 0o644); err != nil {
		t.Fatal(err)
	}

	// Track what the server received.
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
		resp := map[string]string{"url": "https://blossom.example.com/abc123"}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	sk := nostr.Generate()
	pk := nostr.GetPublicKey(sk)
	keys := Keys{SK: sk, PK: pk}

	cmd := blossomUploadCmd([]string{srv.URL}, filePath, keys)
	msg := cmd()

	uploadMsg, ok := msg.(blossomUploadMsg)
	if !ok {
		t.Fatalf("expected blossomUploadMsg, got %T: %v", msg, msg)
	}

	// Verify ciphertext was uploaded, not plaintext.
	if bytes.Equal(receivedBody, plaintext) {
		t.Fatal("server received plaintext — expected ciphertext")
	}

	// Verify the received body can be decrypted to the original plaintext.
	key, err := hex.DecodeString(uploadMsg.KeyHex)
	if err != nil {
		t.Fatalf("decoding key: %v", err)
	}
	nonce, err := hex.DecodeString(uploadMsg.NonceHex)
	if err != nil {
		t.Fatalf("decoding nonce: %v", err)
	}

	decrypted, err := decryptAESGCM(key, nonce, receivedBody)
	if err != nil {
		t.Fatalf("decrypting uploaded body: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Error("decrypted upload does not match original plaintext")
	}

	// Verify auth event hash matches ciphertext hash.
	ciphertextHash := sha256.Sum256(receivedBody)
	if uploadMsg.SHA256 != hex.EncodeToString(ciphertextHash[:]) {
		t.Errorf("SHA256 = %s, want hash of ciphertext %s", uploadMsg.SHA256, hex.EncodeToString(ciphertextHash[:]))
	}

	// Verify ox hash matches plaintext hash.
	plaintextHash := sha256.Sum256(plaintext)
	if uploadMsg.OxHex != hex.EncodeToString(plaintextHash[:]) {
		t.Errorf("OxHex = %s, want hash of plaintext %s", uploadMsg.OxHex, hex.EncodeToString(plaintextHash[:]))
	}

	// Verify encryption params are populated.
	if uploadMsg.KeyHex == "" {
		t.Error("KeyHex is empty")
	}
	if uploadMsg.NonceHex == "" {
		t.Error("NonceHex is empty")
	}
	if uploadMsg.MimeType == "" {
		t.Error("MimeType is empty")
	}
}

func TestBlossomUploadCmd_AllServersFail(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sk := nostr.Generate()
	pk := nostr.GetPublicKey(sk)
	keys := Keys{SK: sk, PK: pk}

	cmd := blossomUploadCmd([]string{srv.URL}, filePath, keys)
	msg := cmd()

	if _, ok := msg.(blossomUploadErrMsg); !ok {
		t.Fatalf("expected blossomUploadErrMsg, got %T", msg)
	}
}

// Ensure blossomUploadMsg implements tea.Msg.
var _ tea.Msg = blossomUploadMsg{}
var _ tea.Msg = blossomUploadErrMsg{}

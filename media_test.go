package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestDetectContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filePath string
		data     []byte
		want     string
	}{
		{"png by extension", "photo.png", nil, "image/png"},
		{"jpeg by extension", "photo.jpg", nil, "image/jpeg"},
		{"gif by extension", "anim.gif", nil, "image/gif"},
		{"unknown extension uses sniffing", "data.zzz", []byte("<html>"), "text/html; charset=utf-8"},
		{"no extension uses sniffing", "noext", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectContentType(tt.filePath, tt.data)
			if got != tt.want {
				t.Errorf("detectContentType(%q, ...) = %q, want %q", tt.filePath, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"normal.txt", "normal.txt"},
		{"unsafe<>:\"/\\|?*.txt", "unsafe_________.txt"},
		{"", "attachment"},
		{".", "attachment"},
		{"/", "_"},
		{strings.Repeat("a", 300) + ".png", strings.Repeat("a", 196) + ".png"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHandleFileMessage_Unencrypted(t *testing.T) {
	t.Parallel()

	content := []byte("hello world file")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	peerPK := "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567"

	rumor := nostr.Event{
		Kind:    KindFileMessage,
		Content: srv.URL + "/test.txt",
		Tags: nostr.Tags{
			{"file-type", "text/plain"},
		},
	}

	result := handleFileMessage(context.Background(), rumor, cacheDir, peerPK)

	if !strings.Contains(result, "📎 file:") {
		t.Errorf("expected file display string, got %q", result)
	}

	// Verify the file was downloaded (directory uses truncated 8-char pubkey).
	attachDir := filepath.Join(cacheDir, "attachments", peerPK[:8])
	entries, err := os.ReadDir(attachDir)
	if err != nil {
		t.Fatalf("reading attachments dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no files downloaded")
	}

	data, err := os.ReadFile(filepath.Join(attachDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("downloaded content = %q, want %q", data, content)
	}
}

func TestHandleFileMessage_Encrypted(t *testing.T) {
	t.Parallel()

	plaintext := []byte("secret image data 🔐")

	srcPath := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(srcPath, plaintext, 0o644); err != nil {
		t.Fatal(err)
	}

	enc, err := encryptFileForUpload(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(enc.CiphertextPath) }()

	ciphertext, err := os.ReadFile(enc.CiphertextPath)
	if err != nil {
		t.Fatalf("reading ciphertext: %v", err)
	}

	xHash := sha256.Sum256(ciphertext)
	xHex := hex.EncodeToString(xHash[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	peerPK := "aaaa000011112222aaaa000011112222aaaa000011112222aaaa000011112222"

	rumor := nostr.Event{
		Kind:    KindFileMessage,
		Content: srv.URL + "/encrypted.bin",
		Tags: nostr.Tags{
			{"file-type", "image/png"},
			{"encryption-algorithm", "aes-gcm"},
			{"decryption-key", enc.KeyHex},
			{"decryption-nonce", enc.NonceHex},
			{"x", xHex},
			{"ox", enc.OxHex},
		},
	}

	result := handleFileMessage(context.Background(), rumor, cacheDir, peerPK)

	if !strings.Contains(result, "📎 image:") {
		t.Errorf("expected image display string, got %q", result)
	}

	// Verify the file was decrypted (directory uses truncated 8-char pubkey).
	attachDir := filepath.Join(cacheDir, "attachments", peerPK[:8])
	entries, err := os.ReadDir(attachDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no files downloaded")
	}

	data, err := os.ReadFile(filepath.Join(attachDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(plaintext) {
		t.Errorf("decrypted content = %q, want %q", data, plaintext)
	}
}

func TestDownloadURL_SizeLimit(t *testing.T) {
	t.Parallel()

	// Serve a response larger than maxDownloadSize.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write maxDownloadSize + 2 bytes.
		buf := make([]byte, 8192)
		written := int64(0)
		for written < maxDownloadSize+2 {
			n := int64(len(buf))
			if written+n > maxDownloadSize+2 {
				n = maxDownloadSize + 2 - written
			}
			_, _ = w.Write(buf[:n])
			written += n
		}
	}))
	defer srv.Close()

	cacheDir := t.TempDir()

	_, err := downloadURL(context.Background(), srv.URL+"/big.bin", cacheDir, "testpeer")
	if err == nil {
		t.Fatal("expected error for oversized download")
	}
	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify no partial file remains.
	attachDir := filepath.Join(cacheDir, "attachments", "testpeer"[:8])
	entries, _ := os.ReadDir(attachDir)
	if len(entries) > 0 {
		t.Errorf("partial file not cleaned up: found %d entries", len(entries))
	}
}

func TestHandleFileMessage_DownloadFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	peerPK := "bbbb000011112222bbbb000011112222bbbb000011112222bbbb000011112222"

	rumor := nostr.Event{
		Kind:    KindFileMessage,
		Content: srv.URL + "/missing.txt",
		Tags: nostr.Tags{
			{"file-type", "text/plain"},
		},
	}

	result := handleFileMessage(context.Background(), rumor, cacheDir, peerPK)

	if !strings.Contains(result, "download failed") {
		t.Errorf("expected download failed message, got %q", result)
	}
}

func TestHandleFileMessage_EmptyContent(t *testing.T) {
	t.Parallel()

	rumor := nostr.Event{
		Kind:    KindFileMessage,
		Content: "",
	}

	result := handleFileMessage(context.Background(), rumor, t.TempDir(), "peer")
	if !strings.Contains(result, "no URL") {
		t.Errorf("expected no URL message, got %q", result)
	}
}

// TestDownloadURL_KillMidCopy reproduces the actual bug: a process
// killed mid-download leaving a truncated file at the cache path that
// the next run accepts as "already cached".
//
// The old code wrote straight to cachedPath. SIGKILL bypasses all
// defers, so the partial bytes already on disk stayed there. The fix
// writes to .part and renames only after a checked Sync+Close — SIGKILL
// leaves a .part file that the os.Stat cache check ignores.
//
// We can't kill our own process, so we re-exec the test binary as a
// child. The child enters TestMain, sees NITROUS_TEST_DOWNLOAD_KILL,
// and runs downloadURL against a server that stalls forever — but only
// after the child has signalled (via a side-band HTTP hit) that
// io.Copy has begun. The parent then SIGKILLs.
func TestDownloadURL_KillMidCopy(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs test binary")
	}

	cacheDir := t.TempDir()
	rawURL, unblock, stop := serveSlowAndWaitForCopy(t)
	defer stop()

	// First run: child process, killed mid-copy.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"NITROUS_TEST_DOWNLOAD_KILL=1",
		"NITROUS_TEST_URL="+rawURL,
		"NITROUS_TEST_CACHE="+cacheDir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	waitForChildToBeginCopy(t)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	_ = cmd.Wait()
	unblock() // release the stalled handler so srv.Close returns promptly

	// The bug: old code already created cachedPath by now.
	// Compute the path the same way downloadURL does (peerPK
	// truncated to 8 chars; sha256(url)[:8].ext).
	urlHash := sha256.Sum256([]byte(rawURL))
	cachedPath := filepath.Join(cacheDir, "attachments", "killpeer",
		hex.EncodeToString(urlHash[:8])+".bin")

	if _, err := os.Stat(cachedPath); err == nil {
		got, _ := os.ReadFile(cachedPath)
		t.Fatalf("SIGKILL mid-copy left %d bytes at cache path — "+
			"next run would return this as a cache hit", len(got))
	}

	// A .part file is acceptable — it's invisible to the cache check
	// and will be truncated by the next os.Create. (We don't assert
	// it exists because the kill might land before os.Create.)
}

// childCopying is closed by serveSlowAndWaitForCopy's handler once it
// has flushed bytes to the child, proving io.Copy is in progress.
var childCopying chan struct{}

func serveSlowAndWaitForCopy(t *testing.T) (rawURL string, unblock, stop func()) {
	t.Helper()
	childCopying = make(chan struct{})
	release := make(chan struct{})
	var signalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write a chunk and flush so the child's io.Copy actually
		// puts bytes on disk before we kill it.
		_, _ = w.Write([]byte("partial-bytes-on-disk"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if !signalled {
			signalled = true
			close(childCopying)
		}
		// Stall until the parent releases us (after SIGKILL +
		// Wait). The child's io.Copy blocks here. Bounded so a
		// test bug can't wedge the suite.
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	return srv.URL + "/slow.bin", func() { close(release) }, srv.Close
}

func waitForChildToBeginCopy(t *testing.T) {
	t.Helper()
	select {
	case <-childCopying:
		// Give io.Copy a moment to actually write(2) those bytes.
		// We could fsync-probe but this is enough in practice.
		time.Sleep(50 * time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatal("child never began downloading")
	}
}

// downloadKillChildMain is invoked from TestMain when re-exec'd by
// TestDownloadURL_KillMidCopy. It never returns: io.Copy blocks on the
// stalled server until the parent SIGKILLs.
func downloadKillChildMain() {
	_, _ = downloadURL(context.Background(),
		os.Getenv("NITROUS_TEST_URL"),
		os.Getenv("NITROUS_TEST_CACHE"),
		"killpeer")
	// Unreachable: server stalls until we're killed. If we get here
	// the server stopped stalling — exit nonzero so the parent's
	// os.Stat check is the only thing that can pass.
	os.Exit(2)
}

func TestDownloadURL_PreservesExtension(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data")
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	const fullPeerPK = "peer1234longkeythatshouldbetrimmed"
	localPath, err := downloadURL(context.Background(), srv.URL+"/my%20file.txt", cacheDir, fullPeerPK)
	if err != nil {
		t.Fatal(err)
	}

	if ext := filepath.Ext(localPath); ext != ".txt" {
		t.Errorf("extension = %q, want .txt", ext)
	}

	// Verify peer directory is truncated to first 8 characters.
	wantDir := filepath.Join("attachments", fullPeerPK[:8])
	if !strings.Contains(localPath, wantDir) {
		t.Errorf("path %q does not contain truncated peer directory %q", localPath, wantDir)
	}
	if strings.Contains(localPath, fullPeerPK) {
		t.Errorf("path %q contains full peer key; truncation not applied", localPath)
	}
}

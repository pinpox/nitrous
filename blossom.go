package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
)

// blossomUploadMsg is returned on successful upload.
type blossomUploadMsg struct {
	URL      string
	SHA256   string
	Size     int64
	MimeType string
}

// blossomUploadErrMsg is returned when all upload attempts fail.
type blossomUploadErrMsg struct{ err error }

func (e blossomUploadErrMsg) Error() string { return e.err.Error() }

// buildBlossomAuthEvent builds a kind-24242 event for Blossom upload authentication.
func buildBlossomAuthEvent(hashHex string, keys Keys) (nostr.Event, error) {
	expiration := time.Now().Add(5 * time.Minute).Unix()
	evt := nostr.Event{
		Kind:      24242,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"t", "upload"},
			{"x", hashHex},
			{"expiration", fmt.Sprintf("%d", expiration)},
		},
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// blossomUploadCmd uploads a file to the configured Blossom servers.
func blossomUploadCmd(servers []string, filePath string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		// Expand ~/
		if strings.HasPrefix(filePath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return blossomUploadErrMsg{fmt.Errorf("expand home: %w", err)}
			}
			filePath = home + filePath[1:]
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			return blossomUploadErrMsg{fmt.Errorf("read file: %w", err)}
		}

		hash := sha256.Sum256(data)
		hashHex := hex.EncodeToString(hash[:])

		mimeType := http.DetectContentType(data)

		// Build kind 24242 auth event.
		evt, err := buildBlossomAuthEvent(hashHex, keys)
		if err != nil {
			return blossomUploadErrMsg{fmt.Errorf("sign auth: %w", err)}
		}

		evtJSON, err := json.Marshal(evt)
		if err != nil {
			return blossomUploadErrMsg{fmt.Errorf("marshal auth: %w", err)}
		}
		authHeader := "Nostr " + base64.StdEncoding.EncodeToString(evtJSON)

		// Upload to all servers concurrently.
		type result struct {
			server string
			url    string
			err    error
		}

		results := make(chan result, len(servers))
		var wg sync.WaitGroup

		for _, server := range servers {
			wg.Add(1)
			go func(server string) {
				defer wg.Done()

				uploadURL := strings.TrimRight(server, "/") + "/upload"
				req, err := http.NewRequest("PUT", uploadURL, bytes.NewReader(data))
				if err != nil {
					results <- result{server: server, err: err}
					return
				}
				req.Header.Set("Authorization", authHeader)
				req.Header.Set("Content-Type", mimeType)

				client := &http.Client{Timeout: 30 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					results <- result{server: server, err: err}
					return
				}
				defer func() { _ = resp.Body.Close() }()

				body, err := io.ReadAll(resp.Body)
				if err != nil {
					results <- result{server: server, err: fmt.Errorf("read response: %w", err)}
					return
				}
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
					results <- result{server: server, err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))}
					return
				}

				// Parse response to get URL.
				var respData struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal(body, &respData); err != nil {
					// Fallback: construct URL from server + hash.
					respData.URL = strings.TrimRight(server, "/") + "/" + hashHex
				}
				if respData.URL == "" {
					respData.URL = strings.TrimRight(server, "/") + "/" + hashHex
				}

				results <- result{server: server, url: respData.URL}
			}(server)
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		var firstURL string
		var errors []string
		for r := range results {
			if r.err != nil {
				log.Printf("blossom: upload to %s failed: %v", r.server, r.err)
				errors = append(errors, fmt.Sprintf("%s: %v", r.server, r.err))
			} else {
				log.Printf("blossom: uploaded to %s -> %s", r.server, r.url)
				if firstURL == "" {
					firstURL = r.url
				}
			}
		}

		if firstURL == "" {
			return blossomUploadErrMsg{fmt.Errorf("all servers failed: %s", strings.Join(errors, "; "))}
		}

		return blossomUploadMsg{
			URL:      firstURL,
			SHA256:   hashHex,
			Size:     int64(len(data)),
			MimeType: mimeType,
		}
	}
}

// isFilePath checks if a string looks like a file path that exists on disk.
func isFilePath(s string) bool {
	if !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~/") {
		return false
	}
	if strings.ContainsRune(s, '\n') {
		return false
	}

	path := s
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = home + path[1:]
	}

	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

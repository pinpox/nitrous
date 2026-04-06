package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip17"
	"fiatjaf.com/nostr/nip59"
)

// Bubbletea message types for NIP-17 DM events.
type dmEventMsg ChatMessage

// dmSendErrMsg wraps a DM send error with the peer pubkey so the error
// message is shown in the correct DM conversation, not whatever room
// happens to be active when the async send fails.
type dmSendErrMsg struct {
	peerPK string
	err    error
}

// Subscription-ended message — triggers reconnection.
type dmSubEndedMsg struct{}

// Reconnection delay message — dispatched after a brief pause.
type dmReconnectMsg struct{}

// Subscription setup result — returned from Cmds so the model can store
// the channel and cancel func without blocking Init().
type dmSubStartedMsg struct {
	events <-chan nostr.Event
	cancel context.CancelFunc
}

// dmReconnectDelayCmd waits briefly before signalling a DM reconnection.
func dmReconnectDelayCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return dmReconnectMsg{}
	}
}

// subscribeDMCmd opens a NIP-17 DM listener inside a tea.Cmd so it doesn't block Init/Update.
// NIP-42 auth is handled by the pool's AuthRequiredHandler; we pre-connect to each relay
// and wait briefly so the AUTH handshake completes before subscribing.
func subscribeDMCmd(pool *nostr.Pool, relays []string, kr nostr.Keyer, since nostr.Timestamp) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		pk, err := kr.GetPublicKey(ctx)
		if err != nil {
			log.Printf("subscribeDMCmd: failed to get public key: %v", err)
			cancel()
			return nostrErrMsg{fmt.Errorf("subscribeDMCmd: %w", err)}
		}
		// NIP-59 gift wraps use randomized created_at timestamps (up to ±2 days)
		// to thwart time-analysis attacks. Subtract 3 days from the since filter
		// so we don't miss events whose outer timestamp is in the past.
		adjustedSince := since - 259200 // 3 days
		if adjustedSince < 0 {
			adjustedSince = 0
		}
		log.Printf("subscribeDMCmd: listening for kind 1059 gift wraps to %s since %d (adjusted from %d)", shortPK(pk.Hex()), adjustedSince, since)

		// Pre-authenticate with each relay via NIP-42 before subscribing.
		var wg sync.WaitGroup
		for _, url := range relays {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("subscribeDMCmd: failed to connect to %s: %v", url, err)
					return
				}
				authCtx, authCancel := context.WithTimeout(ctx, 5*time.Second)
				err = r.Auth(authCtx, kr.SignEvent)
				authCancel()
				if err != nil {
					log.Printf("subscribeDMCmd: NIP-42 auth on %s returned: %v (may still succeed relay-side)", url, err)
				} else {
					log.Printf("subscribeDMCmd: NIP-42 auth succeeded on %s", url)
				}
			}(url)
		}
		wg.Wait()

		// Use nip17.ListenForMessages to handle subscription + gift unwrapping.
		ch := nip17.ListenForMessages(ctx, pool, kr, relays, adjustedSince)

		return dmSubStartedMsg{events: ch, cancel: cancel}
	}
}

// fileDownloadRequestMsg is dispatched when a kind 15 file message is received.
// It triggers an async download+decrypt in a separate Cmd.
type fileDownloadRequestMsg struct {
	rumor    nostr.Event
	peer     string
	eventID  string
	isMine   bool
	cacheDir string
}

// waitForDMEvent blocks on the NIP-17 DM channel and returns the next decrypted rumor.
// Kind 14 rumors are returned as dmEventMsg (text). Kind 15 rumors are returned as
// fileDownloadRequestMsg so the file can be downloaded without blocking the event loop.
func waitForDMEvent(events <-chan nostr.Event, keys Keys, cacheDir string) tea.Cmd {
	return func() tea.Msg {
		rumor, ok := <-events
		if !ok {
			return dmSubEndedMsg{}
		}

		// rumor.PubKey = sender, rumor.Content = plaintext (already decrypted by nip17)
		// Determine peer: if sender is us, look at "p" tag for recipient
		peer := rumor.PubKey.Hex()
		if rumor.PubKey == keys.PK {
			for _, tag := range rumor.Tags {
				if len(tag) >= 2 && tag[0] == "p" {
					peer = tag[1]
					break
				}
			}
		}

		// The library sets rumor.ID via GetID(); use it for dedup.
		eventID := rumor.ID.Hex()
		if eventID == nostr.ZeroID.Hex() {
			h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s", rumor.PubKey.Hex(), peer, rumor.CreatedAt, rumor.Content)))
			eventID = hex.EncodeToString(h[:])
		}

		// Kind 15 = file message — dispatch async download+decrypt.
		if rumor.Kind == KindFileMessage {
			return fileDownloadRequestMsg{
				rumor:    rumor,
				peer:     peer,
				eventID:  eventID,
				isMine:   rumor.PubKey == keys.PK,
				cacheDir: cacheDir,
			}
		}

		return dmEventMsg(ChatMessage{
			Author:    shortPK(rumor.PubKey.Hex()),
			PubKey:    peer,
			Content:   rumor.Content,
			Timestamp: rumor.CreatedAt,
			EventID:   eventID,
			IsMine:    rumor.PubKey == keys.PK,
		})
	}
}

// sendDM publishes a NIP-17 gift-wrapped DM to a recipient.
// Returns a dmEventMsg with the plaintext so it appears locally.
func sendDM(pool *nostr.Pool, relays []string, recipientPK string, content string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		recipient, err := nostr.PubKeyFromHex(recipientPK)
		if err != nil {
			return dmSendErrMsg{peerPK: recipientPK, err: fmt.Errorf("send DM: invalid recipient pubkey: %w", err)}
		}

		theirRelays := nip17.GetDMRelays(ctx, recipient, pool, relays)
		if len(theirRelays) == 0 {
			theirRelays = relays // fallback to our relays
		}

		err = nip17.PublishMessage(ctx, content, nil, pool, relays, theirRelays, kr, recipient, nil)
		if err != nil {
			return dmSendErrMsg{peerPK: recipientPK, err: fmt.Errorf("send DM: %w", err)}
		}

		ts := nostr.Now()
		h := sha256.Sum256([]byte(fmt.Sprintf("local:%s:%s:%d:%s", keys.PK.Hex(), recipientPK, ts, content)))
		return dmEventMsg(ChatMessage{
			Author:    shortPK(keys.PK.Hex()),
			PubKey:    recipientPK,
			Content:   content,
			Timestamp: ts,
			EventID:   hex.EncodeToString(h[:]),
			IsMine:    true,
		})
	}
}

// handleFileDownloadCmd downloads and decrypts a kind 15 file message in the
// background, returning a dmEventMsg with the display string.
func handleFileDownloadCmd(req fileDownloadRequestMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()

		content := handleFileMessage(ctx, req.rumor, req.cacheDir, req.peer)

		return dmEventMsg(ChatMessage{
			Author:    shortPK(req.rumor.PubKey.Hex()),
			PubKey:    req.peer,
			Content:   content,
			Timestamp: req.rumor.CreatedAt,
			EventID:   req.eventID,
			IsMine:    req.isMine,
		})
	}
}

// sendFileMessageCmd publishes a NIP-17 kind 15 file message via gift wrap.
// The URL is sent as the rumor content, and encryption/file metadata are
// included as tags so the recipient can download and decrypt the file.
func sendFileMessageCmd(pool *nostr.Pool, relays []string, recipientPK string, msg blossomUploadMsg, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		recipient, err := nostr.PubKeyFromHex(recipientPK)
		if err != nil {
			return dmSendErrMsg{peerPK: recipientPK, err: fmt.Errorf("send file msg: invalid recipient pubkey: %w", err)}
		}

		ourPubkey, err := kr.GetPublicKey(ctx)
		if err != nil {
			return dmSendErrMsg{peerPK: recipientPK, err: fmt.Errorf("send file msg: get pubkey: %w", err)}
		}

		// Build kind 15 rumor with encryption metadata tags.
		tags := nostr.Tags{
			{"p", recipientPK},
			{"file-type", msg.MimeType},
			{"encryption-algorithm", "aes-gcm"},
			{"decryption-key", msg.KeyHex},
			{"decryption-nonce", msg.NonceHex},
			{"x", msg.SHA256},
			{"ox", msg.OxHex},
		}

		rumor := nostr.Event{
			Kind:      KindFileMessage,
			Content:   msg.URL,
			Tags:      tags,
			CreatedAt: nostr.Now(),
			PubKey:    ourPubkey,
		}
		rumor.ID = rumor.GetID()

		// Gift-wrap to ourselves.
		toUs, err := nip59.GiftWrap(rumor, ourPubkey,
			func(s string) (string, error) { return kr.Encrypt(ctx, s, ourPubkey) },
			func(e *nostr.Event) error { return kr.SignEvent(ctx, e) },
			nil,
		)
		if err != nil {
			return dmSendErrMsg{peerPK: recipientPK, err: fmt.Errorf("send file msg: gift-wrap toUs: %w", err)}
		}

		// Gift-wrap to recipient.
		toThem, err := nip59.GiftWrap(rumor, recipient,
			func(s string) (string, error) { return kr.Encrypt(ctx, s, recipient) },
			func(e *nostr.Event) error { return kr.SignEvent(ctx, e) },
			nil,
		)
		if err != nil {
			return dmSendErrMsg{peerPK: recipientPK, err: fmt.Errorf("send file msg: gift-wrap toThem: %w", err)}
		}

		// Publish to our relays.
		publishToRelays(ctx, pool, relays, toUs, "toUs")

		// Publish to their relays.
		theirRelays := nip17.GetDMRelays(ctx, recipient, pool, relays)
		if len(theirRelays) == 0 {
			theirRelays = relays
		}
		publishToRelays(ctx, pool, theirRelays, toThem, "toThem")

		// Return local echo.
		ts := nostr.Now()
		h := sha256.Sum256([]byte(fmt.Sprintf("local-file:%s:%s:%d:%s", keys.PK.Hex(), recipientPK, ts, msg.URL)))
		displayContent := fmt.Sprintf("📎 %s", msg.URL)
		return dmEventMsg(ChatMessage{
			Author:    shortPK(keys.PK.Hex()),
			PubKey:    recipientPK,
			Content:   displayContent,
			Timestamp: ts,
			EventID:   hex.EncodeToString(h[:]),
			IsMine:    true,
		})
	}
}

// publishToRelays connects to each relay and publishes the given event.
// label is used in log messages to distinguish the publish target (e.g.
// "toUs" vs "toThem").  Errors are logged but not returned because
// partial publish success is acceptable for gift-wrapped DMs.
func publishToRelays(ctx context.Context, pool *nostr.Pool, relays []string, event nostr.Event, label string) {
	for _, url := range relays {
		r, err := pool.EnsureRelay(url)
		if err != nil {
			log.Printf("publishToRelays[%s]: failed to connect to %s: %v", label, url, err)
			continue
		}
		if err := r.Publish(ctx, event); err != nil {
			log.Printf("publishToRelays[%s]: failed to publish to %s: %v", label, url, err)
		}
	}
}

// buildDMRelaysEvent builds a kind-10050 event (NIP-17 DM relay list).
func buildDMRelaysEvent(relays []string, keys Keys) (nostr.Event, error) {
	var tags nostr.Tags
	for _, r := range relays {
		tags = append(tags, nostr.Tag{"relay", r})
	}

	evt := nostr.Event{
		Kind:      nostr.KindDMRelayList,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// dmRelaysPublishedMsg is returned after publishing a kind-10050 DM relay list event.
type dmRelaysPublishedMsg struct {
	err error
}

// publishDMRelaysCmd publishes a kind-10050 event (NIP-17 DM relay list)
// so other clients know where to send gift-wrapped DMs.
func publishDMRelaysCmd(pool *nostr.Pool, relays []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildDMRelaysEvent(relays, keys)
		if err != nil {
			return dmRelaysPublishedMsg{err: fmt.Errorf("publishDMRelays: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
		log.Printf("publishDMRelays: published kind 10050 with %d relays", len(relays))
		return dmRelaysPublishedMsg{}
	}
}

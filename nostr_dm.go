package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip17"
	"github.com/nbd-wtf/go-nostr/nip59"
)

// Bubbletea message types for NIP-17 DM events.
type dmEventMsg ChatMessage

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
// NIP-42 auth is handled by the pool's WithAuthHandler; we pre-connect to each relay
// and wait briefly so the AUTH handshake completes before subscribing.
func subscribeDMCmd(pool *nostr.SimplePool, relays []string, kr nostr.Keyer, since nostr.Timestamp) tea.Cmd {
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
		// The seenEvents dedup map handles any duplicates.
		adjustedSince := since - 259200 // 3 days
		if adjustedSince < 0 {
			adjustedSince = 0
		}
		log.Printf("subscribeDMCmd: listening for kind 1059 gift wraps to %s since %d (adjusted from %d)", shortPK(pk), adjustedSince, since)

		// Pre-authenticate with each relay via NIP-42 in the background.
		// Some relays send an AUTH challenge on connect; r.Auth() reads it and responds.
		// We don't wait for completion — the subscription starts immediately and the
		// pool's WithAuthHandler handles any late AUTH challenges reactively.
		for _, url := range relays {
			go func(url string) {
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("subscribeDMCmd: failed to connect to %s: %v", url, err)
					return
				}
				time.Sleep(500 * time.Millisecond)
				authCtx, authCancel := context.WithTimeout(ctx, 3*time.Second)
				err = r.Auth(authCtx, func(ae *nostr.Event) error { return kr.SignEvent(authCtx, ae) })
				authCancel()
				if err != nil {
					log.Printf("subscribeDMCmd: NIP-42 auth on %s returned: %v (may still succeed relay-side)", url, err)
				} else {
					log.Printf("subscribeDMCmd: NIP-42 auth succeeded on %s", url)
				}
			}(url)
		}

		ch := make(chan nostr.Event)

		go func() {
			defer close(ch)
			for ie := range pool.SubscribeMany(ctx, relays, nostr.Filter{
				Kinds: []int{1059},
				Tags:  nostr.TagMap{"p": []string{pk}},
				Since: &adjustedSince,
			}) {
				log.Printf("subscribeDMCmd: got kind 1059 event id=%s from relay=%s", shortPK(ie.ID), ie.Relay.URL)
				rumor, err := nip59.GiftUnwrap(
					*ie.Event,
					func(otherpubkey, ciphertext string) (string, error) { return kr.Decrypt(ctx, ciphertext, otherpubkey) },
				)
				if err != nil {
					log.Printf("subscribeDMCmd: unwrap failed: %v", err)
					continue
				}
				log.Printf("subscribeDMCmd: unwrapped rumor kind=%d from=%s content=%q", rumor.Kind, shortPK(rumor.PubKey), rumor.Content[:min(len(rumor.Content), 50)])
				ch <- rumor
			}
			log.Println("subscribeDMCmd: subscription ended")
		}()

		return dmSubStartedMsg{events: ch, cancel: cancel}
	}
}

// waitForDMEvent blocks on the NIP-17 DM channel and returns the next decrypted rumor.
func waitForDMEvent(events <-chan nostr.Event, keys Keys) tea.Cmd {
	return func() tea.Msg {
		rumor, ok := <-events
		if !ok {
			return dmSubEndedMsg{}
		}

		// rumor.PubKey = sender, rumor.Content = plaintext (already decrypted by nip17)
		// Determine peer: if sender is us, look at "p" tag for recipient
		peer := rumor.PubKey
		if peer == keys.PK {
			for _, tag := range rumor.Tags {
				if len(tag) >= 2 && tag[0] == "p" {
					peer = tag[1]
					break
				}
			}
		}

		// Rumors (kind 14) are unsigned and have no ID; synthesize one for dedup.
		eventID := rumor.ID
		if eventID == "" {
			h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s", rumor.PubKey, peer, rumor.CreatedAt, rumor.Content)))
			eventID = hex.EncodeToString(h[:])
		}

		return dmEventMsg(ChatMessage{
			Author:    shortPK(rumor.PubKey),
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
func sendDM(pool *nostr.SimplePool, relays []string, recipientPK string, content string, keys Keys, kr nostr.Keyer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		theirRelays := nip17.GetDMRelays(ctx, recipientPK, pool, relays)
		if len(theirRelays) == 0 {
			theirRelays = relays // fallback to our relays
		}

		toUs, toThem, err := nip17.PrepareMessage(ctx, content, nil, kr, recipientPK, nil)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("send DM: prepare: %w", err)}
		}

		// Publish to all relays concurrently. A dead relay won't block the rest.
		var wg sync.WaitGroup
		var sentToUs, sentToThem atomic.Bool

		// "to us" copy → our relays
		for _, url := range relays {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("sendDM: connect %s: %v", url, err)
					return
				}
				if err := r.Publish(ctx, toUs); err != nil {
					log.Printf("sendDM: publish toUs to %s: %v", url, err)
					return
				}
				sentToUs.Store(true)
				log.Printf("sendDM: published toUs to %s", url)
			}(url)
		}

		// "to them" copy → their relays
		for _, url := range theirRelays {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				r, err := pool.EnsureRelay(url)
				if err != nil {
					log.Printf("sendDM: connect %s: %v", url, err)
					return
				}
				if err := r.Publish(ctx, toThem); err != nil {
					log.Printf("sendDM: publish toThem to %s: %v", url, err)
					return
				}
				sentToThem.Store(true)
				log.Printf("sendDM: published toThem to %s", url)
			}(url)
		}

		wg.Wait()

		if !sentToUs.Load() && !sentToThem.Load() {
			return nostrErrMsg{fmt.Errorf("send DM: failed to publish to any relay")}
		}
		if !sentToThem.Load() {
			log.Printf("sendDM: warning: could not deliver to recipient's relays %v", theirRelays)
		}

		ts := nostr.Now()
		h := sha256.Sum256([]byte(fmt.Sprintf("local:%s:%s:%d:%s", keys.PK, recipientPK, ts, content)))
		return dmEventMsg(ChatMessage{
			Author:    shortPK(keys.PK),
			PubKey:    recipientPK,
			Content:   content,
			Timestamp: ts,
			EventID:   hex.EncodeToString(h[:]),
			IsMine:    true,
		})
	}
}

// getPeerRelays fetches the NIP-65 relay list (kind 10002) for a pubkey
// and returns the write relay URLs. Falls back to nil if not found.
func getPeerRelays(pool *nostr.SimplePool, relays []string, pubkey string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	re := pool.QuerySingle(ctx, relays, nostr.Filter{
		Kinds:   []int{10002},
		Authors: []string{pubkey},
	})
	if re == nil {
		return nil
	}

	var urls []string
	for _, tag := range re.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		log.Printf("getPeerRelays: %s tag: %v", shortPK(pubkey), tag)
		urls = append(urls, tag[1])
	}
	log.Printf("getPeerRelays: %s -> %v", shortPK(pubkey), urls)
	return urls
}

// buildDMRelaysEvent builds a kind-10050 event (NIP-17 DM relay list).
func buildDMRelaysEvent(relays []string, keys Keys) (nostr.Event, error) {
	var tags nostr.Tags
	for _, r := range relays {
		tags = append(tags, nostr.Tag{"relay", r})
	}

	evt := nostr.Event{
		Kind:      10050,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := evt.Sign(keys.SK); err != nil {
		return evt, err
	}
	return evt, nil
}

// publishDMRelaysCmd publishes a kind-10050 event (NIP-17 DM relay list)
// so other clients know where to send gift-wrapped DMs.
func publishDMRelaysCmd(pool *nostr.SimplePool, relays []string, keys Keys) tea.Cmd {
	return func() tea.Msg {
		evt, err := buildDMRelaysEvent(relays, keys)
		if err != nil {
			return nostrErrMsg{fmt.Errorf("publishDMRelays: %w", err)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() {
			defer cancel()
			drainPublish(ctx, pool.PublishMany(ctx, relays, evt))
			log.Printf("publishDMRelays: published kind 10050 with %d relays", len(relays))
		}()
		return nil
	}
}

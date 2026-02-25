package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func main() {
	configFlag := flag.String("config", "", "path to config file")
	debugFlag := flag.Bool("debug", false, "enable debug logging to debug.log")
	flag.Parse()

	if *debugFlag {
		f, err := tea.LogToFile("debug.log", "nitrous")
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not open debug log: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		log.Println("debug logging enabled")
	} else {
		log.SetOutput(io.Discard)
	}

	cfg, err := LoadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	log.Printf("config loaded: %d relays", len(cfg.Relays))

	if len(flag.Args()) > 0 && flag.Args()[0] == "keygen" {
		runKeygen(cfg)
		return
	}

	keys, err := loadKeys(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "key error: %v\n", err)
		os.Exit(1)
	}
	log.Printf("keys loaded: npub=%s", keys.NPub)

	rooms, err := LoadRooms(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rooms error: %v\n", err)
		os.Exit(1)
	}
	log.Printf("rooms loaded: %d rooms", len(rooms))

	groups, err := LoadSavedGroups(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "groups error: %v\n", err)
		os.Exit(1)
	}
	log.Printf("groups loaded: %d groups", len(groups))

	contacts, err := LoadContacts(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contacts error: %v\n", err)
		os.Exit(1)
	}
	log.Printf("contacts loaded: %d contacts", len(contacts))

	// Create the markdown renderer before the TUI starts so the terminal
	// background-color query (OSC 11) completes while stdio is still normal.
	// Detect style once, store it for re-creation on resize.
	mdStyle := detectGlamourStyle()
	mdRender := newMarkdownRenderer(mdStyle)

	kr, err := keyer.NewPlainKeySigner(keys.SK)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keyer error: %v\n", err)
		os.Exit(1)
	}

	pool := nostr.NewSimplePool(context.Background(), nostr.WithAuthHandler(func(ctx context.Context, ie nostr.RelayEvent) error {
		log.Printf("NIP-42 auth requested by %s", ie.Relay.URL)
		return kr.SignEvent(ctx, ie.Event)
	}))

	m := newModel(cfg, *configFlag, keys, pool, kr, rooms, groups, contacts, mdRender, mdStyle)

	log.Println("starting TUI")
	p := tea.NewProgram(&m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	pool.Close("shutdown")
}

func runKeygen(cfg Config) {
	path := cfg.PrivateKeyFile
	if path == "" {
		fmt.Fprintf(os.Stderr, "error: private_key_file not set in config\n")
		os.Exit(1)
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "error: %s already exists, refusing to overwrite\n", path)
		os.Exit(1)
	}

	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error deriving public key: %v\n", err)
		os.Exit(1)
	}
	nsec, err := nip19.EncodePrivateKey(sk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding nsec: %v\n", err)
		os.Exit(1)
	}
	npub, err := nip19.EncodePublicKey(pk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding npub: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "error creating directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, []byte(nsec+"\n"), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "error writing key file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated new keypair:\n")
	fmt.Printf("  nsec: %s\n", nsec)
	fmt.Printf("  npub: %s\n", npub)
	fmt.Printf("  file: %s\n", path)
}

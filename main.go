package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip19"
)

//go:embed config.example.toml
var defaultConfigContent string

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
		// Route the library's internal loggers to our debug log too.
		nostr.InfoLogger.SetOutput(f)
		nostr.DebugLogger.SetOutput(f)
	} else {
		log.SetOutput(io.Discard)
		nostr.InfoLogger.SetOutput(io.Discard)
		nostr.DebugLogger.SetOutput(io.Discard)
	}

	cfgPath := configPath(*configFlag)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		initFirstRun(cfgPath)
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
	initAuthorColors()
	mdRender := newMarkdownRenderer(mdStyle)

	kr := keyer.NewPlainKeySigner(keys.SK)

	pool := nostr.NewPool(nostr.PoolOptions{
		AuthRequiredHandler: func(ctx context.Context, evt *nostr.Event) error {
			log.Printf("NIP-42 auth requested")
			return kr.SignEvent(ctx, evt)
		},
	})

	m := newModel(cfg, *configFlag, keys, pool, &kr, rooms, groups, contacts, mdRender, mdStyle)

	log.Println("starting TUI")
	p := tea.NewProgram(&m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	pool.Close("shutdown")
}

func initFirstRun(cfgPath string) {
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "error creating config directory: %v\n", err)
		os.Exit(1)
	}

	// Generate keypair.
	sk := nostr.Generate()
	pk := nostr.GetPublicKey(sk)
	nsec := nip19.EncodeNsec(sk)
	npub := nip19.EncodeNpub(pk)

	// Write key file.
	keyPath := filepath.Join(dir, "nsec")
	if err := os.WriteFile(keyPath, []byte(nsec+"\n"), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "error writing key file: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated new keypair:\n")
	fmt.Printf("  nsec: %s\n", nsec)
	fmt.Printf("  npub: %s\n", npub)
	fmt.Printf("  file: %s\n", keyPath)

	// Write default config from embedded config.example.toml.
	if err := os.WriteFile(cfgPath, []byte(defaultConfigContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing config: %v\n", err)
		os.Exit(1)
	}
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

	sk := nostr.Generate()
	pk := nostr.GetPublicKey(sk)
	nsec := nip19.EncodeNsec(sk)
	npub := nip19.EncodeNpub(pk)

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

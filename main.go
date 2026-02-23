package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
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

	keys, err := loadKeys()
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

	// Create the markdown renderer before the TUI starts so the terminal
	// background-color query (OSC 11) completes while stdio is still normal.
	// Detect style once, store it for re-creation on resize.
	mdStyle := detectGlamourStyle()
	mdRender := newMarkdownRenderer(mdStyle)

	pool := nostr.NewSimplePool(context.Background())

	m := newModel(cfg, *configFlag, keys, pool, rooms, mdRender, mdStyle)

	log.Println("starting TUI")
	p := tea.NewProgram(&m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	pool.Close("shutdown")
}

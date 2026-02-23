package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nbd-wtf/go-nostr"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	keys, err := loadKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "key error: %v\n", err)
		os.Exit(1)
	}

	pool := nostr.NewSimplePool(context.Background())

	m := newModel(cfg, keys, pool)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	pool.Close("shutdown")
}

package main

import (
	"testing"
)

func TestColorForPubkey(t *testing.T) {
	th := buildTheme(true)
	colors := th.AuthorColors

	t.Run("deterministic", func(t *testing.T) {
		pk := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		c1 := colorForPubkey(pk, colors)
		c2 := colorForPubkey(pk, colors)
		if c1 != c2 {
			t.Errorf("same key should produce same color: %v != %v", c1, c2)
		}
	})

	t.Run("short key returns fallback", func(t *testing.T) {
		c := colorForPubkey("a", colors)
		if c != colors[0] {
			t.Errorf("expected fallback color %v, got %v", colors[0], c)
		}
	})

	t.Run("empty key returns fallback", func(t *testing.T) {
		c := colorForPubkey("", colors)
		if c != colors[0] {
			t.Errorf("expected fallback color %v, got %v", colors[0], c)
		}
	})

	t.Run("non-hex short key returns fallback", func(t *testing.T) {
		c := colorForPubkey("zz", colors)
		if c != colors[0] {
			t.Errorf("expected fallback color for non-hex, got %v", c)
		}
	})

	t.Run("nil colors returns hardcoded fallback", func(t *testing.T) {
		c := colorForPubkey("ab", nil)
		if c == nil {
			t.Error("expected a non-nil fallback color")
		}
	})
}

func TestRenderMarkdown(t *testing.T) {
	t.Run("nil renderer returns input", func(t *testing.T) {
		content := "hello **world**"
		got := renderMarkdown(nil, content)
		if got != content {
			t.Errorf("expected input passthrough, got %q", got)
		}
	})

	t.Run("real renderer produces output", func(t *testing.T) {
		r := newMarkdownRenderer("dark")
		if r == nil {
			t.Skip("could not create markdown renderer")
		}
		content := "hello **world**"
		got := renderMarkdown(r, content)
		if got == "" {
			t.Error("expected non-empty output")
		}
		// The rendered output should be different from plain input (contains ANSI).
		if got == content {
			t.Error("expected rendered output to differ from plain input")
		}
	})
}

func TestNewMarkdownRenderer(t *testing.T) {
	t.Run("dark style", func(t *testing.T) {
		r := newMarkdownRenderer("dark")
		if r == nil {
			t.Error("expected non-nil renderer for 'dark'")
		}
	})

	t.Run("light style", func(t *testing.T) {
		r := newMarkdownRenderer("light")
		if r == nil {
			t.Error("expected non-nil renderer for 'light'")
		}
	})
}

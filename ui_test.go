package main

import (
	"testing"
)

func TestColorForPubkey(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		pk := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		c1 := colorForPubkey(pk)
		c2 := colorForPubkey(pk)
		if c1 != c2 {
			t.Errorf("same key should produce same color: %v != %v", c1, c2)
		}
	})

	t.Run("different keys may differ", func(t *testing.T) {
		// Keys starting with different bytes should (usually) differ.
		pk1 := "00cdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		pk2 := "ffcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		// Just verify they don't panic; they might coincide if hash collides.
		_ = colorForPubkey(pk1)
		_ = colorForPubkey(pk2)
	})

	t.Run("short key returns fallback", func(t *testing.T) {
		c := colorForPubkey("a")
		if c != authorColors[0] {
			t.Errorf("expected fallback color %v, got %v", authorColors[0], c)
		}
	})

	t.Run("empty key returns fallback", func(t *testing.T) {
		c := colorForPubkey("")
		if c != authorColors[0] {
			t.Errorf("expected fallback color %v, got %v", authorColors[0], c)
		}
	})

	t.Run("non-hex short key returns fallback", func(t *testing.T) {
		c := colorForPubkey("zz")
		if c != authorColors[0] {
			t.Errorf("expected fallback color for non-hex, got %v", c)
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

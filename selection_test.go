package main

import (
	"testing"
	"unicode/utf8"
)

func TestSliceByColumns(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		fromCol int
		toCol   int
		want    string
	}{
		// ASCII baseline.
		{"ascii full", "hello", 0, 5, "hello"},
		{"ascii mid", "hello", 1, 4, "ell"},
		{"ascii empty", "hello", 2, 2, ""},

		// Latin-1: é is 2 bytes but 1 column.
		{"latin1 full", "héllo", 0, 5, "héllo"},
		{"latin1 after accent", "héllo", 2, 5, "llo"},
		{"latin1 accent only", "héllo", 1, 2, "é"},

		// CJK: each char is 3 bytes, 2 columns.
		{"cjk full", "日本語", 0, 6, "日本語"},
		{"cjk middle", "abc日本語def", 3, 9, "日本語"},
		{"cjk prefix", "abc日本語def", 0, 3, "abc"},
		{"cjk suffix", "abc日本語def", 9, 12, "def"},
		{"cjk one char", "日本語", 2, 4, "本"},

		// Emoji: 👋 is 4 bytes, 2 columns.
		{"emoji wide", "👋hello", 0, 2, "👋"},
		{"emoji after", "👋hello", 2, 7, "hello"},
		{"emoji mid", "a👋b", 1, 3, "👋"},

		// Clamping: out-of-range columns.
		{"clamp negative", "hello", -3, 2, "he"},
		{"clamp overflow", "hello", 3, 99, "lo"},
		{"clamp both", "héllo", -1, 99, "héllo"},
		{"clamp cjk overflow", "日本", 0, 99, "日本"},

		// Edge: column lands inside a wide char — snap right to the next
		// rune boundary. Column 1 is the right half of 日 (cols 0–1).
		// Snapping right consistently ensures slice(s,0,k) + slice(s,k,w)
		// == s with no gaps or duplicated runes, which applySelectionHighlight
		// relies on when reassembling pre + mid + suf.
		{"mid-wide from", "日x", 1, 3, "x"},
		{"mid-wide to", "x日", 0, 2, "x日"},
		{"mid-wide reassembly pre", "a日b", 0, 2, "a日"},
		{"mid-wide reassembly suf", "a日b", 2, 4, "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sliceByColumns(tt.input, tt.fromCol, tt.toCol)
			if got != tt.want {
				t.Errorf("sliceByColumns(%q, %d, %d) = %q, want %q",
					tt.input, tt.fromCol, tt.toCol, got, tt.want)
			}
		})
	}
}

func TestSliceByColumnsForCopy(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		fromCol int
		toCol   int
		want    string
	}{
		// Zero-width: combining marks and variation selectors stay attached
		// to the preceding visible rune. The user sees one glyph; copying
		// that column must yield the full sequence.
		{"combining acute", "e\u0301", 0, 1, "e\u0301"},
		{"combining mid-string", "ae\u0301b", 1, 2, "e\u0301"},
		{"variation selector", "\u2764\ufe0f", 0, 1, "\u2764\ufe0f"}, // ❤️ = U+2764 + VS-16

		// fromCol side: a combining mark right at the start of the range
		// belongs to the previous (excluded) column — do not pull it in.
		{"combining before range", "ae\u0301b", 2, 3, "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sliceByColumnsForCopy(tt.input, tt.fromCol, tt.toCol)
			if got != tt.want {
				t.Errorf("sliceByColumnsForCopy(%q, %d, %d) = %q, want %q",
					tt.input, tt.fromCol, tt.toCol, got, tt.want)
			}
		})
	}
}

func TestSliceByColumnsReassemblyInvariant(t *testing.T) {
	// applySelectionHighlight relies on slice(s,0,k)+slice(s,k,w) == s.
	// The ForCopy variant breaks this (combining marks duplicate across
	// the boundary), so highlight must use the plain variant.
	inputs := []string{"ae\u0301b", "\u2764\ufe0fx", "a日b", "héllo"}
	for _, s := range inputs {
		w := stringColumnWidth(s)
		for k := 0; k <= w; k++ {
			pre := sliceByColumns(s, 0, k)
			suf := sliceByColumns(s, k, w)
			if pre+suf != s {
				t.Errorf("reassembly broken for %q at k=%d: %q + %q = %q",
					s, k, pre, suf, pre+suf)
			}
		}
	}
}

func TestSliceByColumnsValidUTF8(t *testing.T) {
	// Regression: byte-slicing produced invalid UTF-8 (rendered as �).
	// Ensure every output is valid regardless of column alignment.
	inputs := []string{"日本語hello", "👋world", "héllo", "abc日本語def"}
	for _, s := range inputs {
		w := stringColumnWidth(s)
		for from := -1; from <= w+1; from++ {
			for to := from; to <= w+1; to++ {
				got := sliceByColumns(s, from, to)
				if !utf8.ValidString(got) {
					t.Errorf("sliceByColumns(%q, %d, %d) = %q: invalid UTF-8",
						s, from, to, got)
				}
			}
		}
	}
}

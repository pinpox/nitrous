package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsFilePath(t *testing.T) {
	t.Run("absolute existing file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
		if !isFilePath(f) {
			t.Errorf("expected true for existing file %q", f)
		}
	})

	t.Run("nonexistent absolute path", func(t *testing.T) {
		if isFilePath("/nonexistent/path/to/file.txt") {
			t.Error("expected false for nonexistent file")
		}
	})

	t.Run("relative path returns false", func(t *testing.T) {
		// Even if the file exists, relative paths should return false.
		if isFilePath("go.mod") {
			t.Error("expected false for relative path")
		}
	})

	t.Run("multiline returns false", func(t *testing.T) {
		if isFilePath("/some/path\nwith newline") {
			t.Error("expected false for multiline string")
		}
	})

	t.Run("directory returns false", func(t *testing.T) {
		dir := t.TempDir()
		if isFilePath(dir) {
			t.Error("expected false for directory")
		}
	})

	t.Run("empty string returns false", func(t *testing.T) {
		if isFilePath("") {
			t.Error("expected false for empty string")
		}
	})

	t.Run("tilde path with existing file", func(t *testing.T) {
		// Create a temp file and test with ~/... by faking it.
		// We can't reliably test ~/ in CI, but we can test that non-existent ~/path returns false.
		if isFilePath("~/nonexistent_test_file_12345") {
			t.Error("expected false for nonexistent ~/path")
		}
	})
}

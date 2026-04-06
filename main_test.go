package main

import (
	"os"
	"testing"
)

// TestMain dispatches re-exec'd child entry points before the normal
// test runner takes over. Tests that need to observe SIGKILL behaviour
// re-run os.Args[0] with a sentinel env var; the child enters here and
// branches off to its dedicated main instead of running tests.
func TestMain(m *testing.M) {
	if os.Getenv("NITROUS_TEST_DOWNLOAD_KILL") == "1" {
		downloadKillChildMain() // never returns
	}
	os.Exit(m.Run())
}

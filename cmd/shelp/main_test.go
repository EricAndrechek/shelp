package main

import (
	"os"
	"testing"

	"github.com/ericandrechek/shelp/internal/provider"
)

// TestMain points the history file (and the cooldown/model caches) at a
// throwaway directory, so no test in this package can read or write the
// user's real request history: runGenerate records every call it makes.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shelp-cmd-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir+"/state")
	os.Setenv("XDG_CACHE_HOME", dir+"/cache")
	// Structural guard against real backends (provider/execx.go), and a
	// default chain that names nothing real if a test clears SHELP_PROVIDER.
	os.Setenv(provider.NoBackendsEnv, "1")
	if os.Getenv("SHELP_PROVIDER") == "" {
		os.Setenv("SHELP_PROVIDER", "nosuch:model")
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

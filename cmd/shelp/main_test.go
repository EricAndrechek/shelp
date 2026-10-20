package main

import (
	"os"
	"testing"
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
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

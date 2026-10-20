package main

import "fmt"

// exitStatus ends shelp quietly with an exit code (exitCode in main.go):
// Ctrl-C in an interactive screen or prompt (130), or a SIGTERM/SIGHUP the
// provider manager caught so it could restore the terminal first (128 +
// the signal). In raw mode Ctrl-C is a key, not a SIGINT, so the context
// is not canceled and this is how such a run still exits 130 silently.
type exitStatus struct{ code int }

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", e.code) }

var errInterrupted = exitStatus{130}

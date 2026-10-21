package main

import (
	"strings"
	"testing"
)

// `config -i` is `providers -i`: without a terminal, the same refusal.
func TestConfigInteractiveNeedsTerminal(t *testing.T) {
	for _, flag := range []string{"-i", "--interactive"} {
		_, _, err := runOut(t, "config", flag)
		if err == nil || !strings.Contains(err.Error(), "needs a terminal") {
			t.Errorf("config %s: err = %v, want 'needs a terminal'", flag, err)
		}
	}
}

// Package shellinit holds the shell integration printed by `shelp init`.
//
// A binary cannot put text on its parent shell's command line, so the
// integration wraps `shelp` in a shell function that does (zsh: print -z,
// bash: read -e -i, fish: a widget), in the style of zoxide/starship.
package shellinit

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed scripts/*
var scripts embed.FS

// Script returns the integration for shell ("zsh", "bash", "fish").
func Script(shell string) (string, error) {
	b, err := scripts.ReadFile("scripts/shelp." + shell)
	if err != nil {
		return "", fmt.Errorf("no shell integration for %q (have: %s)", shell, strings.Join(Shells(), ", "))
	}
	return string(b), nil
}

// Shells lists supported shells.
func Shells() []string {
	ents, _ := scripts.ReadDir("scripts")
	var out []string
	for _, e := range ents {
		out = append(out, strings.TrimPrefix(e.Name(), "shelp."))
	}
	sort.Strings(out)
	return out
}

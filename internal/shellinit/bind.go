package shellinit

import (
	"fmt"
	"strings"
)

// ConvertBind converts a keybinding spec into shell's own bindkey/bind
// syntax. The spec is either caret notation ("^G", meaning Ctrl-G) or a
// shell-native escape form (e.g. zsh-style "\eg" for Alt/Meta-g, or
// already-target-native forms like bash's "\C-g" or fish's "\cg") --
// anything that isn't plain caret notation is passed through unchanged,
// since zsh's `bindkey`, bash's `bind`, and fish's `bind` all parse `\e`
// escape forms directly. An empty spec returns "", nil (no binding).
func ConvertBind(shell, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil
	}
	switch shell {
	case "zsh":
		// zsh's bindkey parses ^X and \e-style sequences itself; pass
		// through unchanged in either notation.
		return key, nil
	case "bash", "fish":
		if c, ok := caretChar(key); ok {
			if shell == "bash" {
				return `\C-` + string(c), nil
			}
			return `\c` + string(c), nil
		}
		// Not caret notation (e.g. already "\eg", "\C-g", "\cg"): both
		// bash's readline and fish's bind accept a leading \e escape form
		// (and their own native \C-/\c forms) directly.
		return key, nil
	default:
		return "", fmt.Errorf("shellinit: unknown shell %q", shell)
	}
}

// caretChar reports whether key is exactly caret notation "^X" -- a single
// character after '^' -- returning the lowercased control character used
// by bash's \C- and fish's \c prefixes.
func caretChar(key string) (byte, bool) {
	r := []rune(key)
	if len(r) != 2 || r[0] != '^' {
		return 0, false
	}
	c := r[1]
	if c > 127 {
		return 0, false
	}
	if c >= 'a' && c <= 'z' {
		return byte(c), true
	}
	if c >= 'A' && c <= 'Z' {
		return byte(c - 'A' + 'a'), true
	}
	// Non-letter control characters (^[, ^?, ^@, ^\, ^], ^^, ^_, ...):
	// bash/fish accept these forms literally too, lowercase letters aside.
	return byte(c), true
}

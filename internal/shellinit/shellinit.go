// Package shellinit holds the shell integration printed by `shelp init`
// and the `shelp setup shell` rc-file installer (see setup.go).
//
// A binary cannot put text on its parent shell's command line, so the
// integration wraps `shelp` in a shell function that does (zsh: print -z,
// bash: read -e -i, fish: commandline -r), in the style of zoxide/
// starship. It also, by default, rebinds Enter itself so a line starting
// with `shelp ` reaches shelp byte-for-byte instead of being parsed by
// the shell first (see Options.LineMode and each scripts/*.tmpl file for
// the per-shell mechanism).
package shellinit

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed scripts/*.tmpl
var scripts embed.FS

// Options controls what `shelp init` renders.
type Options struct {
	// Bind is a keybinding for shelp-widget (see ConvertBind for the
	// notation), already resolved from --bind/config by the caller. Empty
	// means no binding is emitted; the script's comments tell the user how
	// to bind it themselves.
	Bind string
	// LineMode turns on the raw-line Enter interception. Callers default
	// this to true themselves (see cmd/shelp/main.go's runInit) so that
	// Script, used by tests and the Homebrew formula, can default it too.
	LineMode bool
}

// view is what each template sees.
type view struct {
	Bind     string
	LineMode bool
}

// Render renders shell's integration script under opts.
func Render(shell string, opts Options) (string, error) {
	shell = strings.ToLower(strings.TrimSpace(shell))
	data, err := scripts.ReadFile("scripts/shelp." + shell + ".tmpl")
	if err != nil {
		return "", fmt.Errorf("no shell integration for %q (have: %s)", shell, strings.Join(Shells(), ", "))
	}
	bind, err := ConvertBind(shell, opts.Bind)
	if err != nil {
		return "", err
	}
	t, err := template.New(shell).Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("shellinit: parse %s template: %w", shell, err)
	}
	var buf strings.Builder
	if err := t.Execute(&buf, view{Bind: bind, LineMode: opts.LineMode}); err != nil {
		return "", fmt.Errorf("shellinit: render %s template: %w", shell, err)
	}
	return buf.String(), nil
}

// Script renders shell's integration with the defaults: raw line mode on,
// no keybinding. Equivalent to Render(shell, Options{LineMode: true}).
// Kept for callers (tests, the Homebrew formula's `brew test`) that just
// want the default script.
func Script(shell string) (string, error) {
	return Render(shell, Options{LineMode: true})
}

// Shells lists supported shells.
func Shells() []string {
	ents, _ := scripts.ReadDir("scripts")
	var out []string
	for _, e := range ents {
		name := strings.TrimSuffix(e.Name(), ".tmpl")
		out = append(out, strings.TrimPrefix(name, "shelp."))
	}
	sort.Strings(out)
	return out
}

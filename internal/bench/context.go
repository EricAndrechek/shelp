package bench

import (
	"fmt"
	"sort"

	"github.com/ericandrechek/shelp/internal/prompt"
)

// osInfo is the SYNTHETIC environment the benchmark tells the model about
// for a given case os value. It is never detected from the host running the
// benchmark, so results are comparable across machines. Keep this in sync
// with bench/README.md, which documents it for anyone reading raw results.
type osFixture struct {
	OS       string
	Userland string
	Cwd      string
	Tools    []string
}

var osFixtures = map[string]osFixture{
	"macos": {
		OS:       "macOS 27.0",
		Userland: "BSD",
		Cwd:      "/Users/bench/project",
		Tools:    []string{"brew", "git", "gh", "jq", "rg", "fd", "fzf", "docker", "kubectl", "uv", "pnpm", "node", "python3", "go"},
	},
	"linux": {
		OS:       "Ubuntu 24.04",
		Userland: "GNU",
		Cwd:      "/home/bench/project",
		Tools:    []string{"apt", "git", "gh", "jq", "rg", "fd", "fzf", "docker", "kubectl", "uv", "pnpm", "node", "python3", "go"},
	},
}

// OSKeys lists the recognized case.OS values, sorted.
func OSKeys() []string {
	var ks []string
	for k := range osFixtures {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// SyntheticContext builds a prompt.Context for a case's os/shell entirely
// from fixed data: never from this host. fields selects which of
// prompt.AllFields to populate ("os", "shell", "cwd", "tools"); empty means
// none. Unknown os values are an error since there is no host to fall back
// to.
func SyntheticContext(osKey, shell string, fields []string) (prompt.Context, error) {
	fx, ok := osFixtures[osKey]
	if !ok {
		return prompt.Context{}, fmt.Errorf("bench: unknown case os %q (want one of: %v)", osKey, OSKeys())
	}
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}
	var c prompt.Context
	if want["os"] {
		c.OS, c.Userland = fx.OS, fx.Userland
	}
	if want["shell"] {
		c.Shell = shell
	}
	if want["cwd"] {
		c.Cwd = fx.Cwd
	}
	if want["tools"] {
		c.Tools = append([]string(nil), fx.Tools...)
	}
	return c, nil
}

// ContextFields is the JSON-friendly, stable serialization of a
// prompt.Context as recorded in a trial result. prompt.Context itself has no
// json tags (internal/prompt is not ours to edit), so we mirror it here.
type ContextFields struct {
	OS       string   `json:"os,omitempty"`
	Userland string   `json:"userland,omitempty"`
	Shell    string   `json:"shell,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Tools    []string `json:"tools,omitempty"`
}

// FromPromptContext converts a prompt.Context to its recorded form.
func FromPromptContext(c prompt.Context) ContextFields {
	return ContextFields{OS: c.OS, Userland: c.Userland, Shell: c.Shell, Cwd: c.Cwd, Tools: c.Tools}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/shellinit"
)

var completionShells = []string{"zsh", "bash", "fish"}

// helpFlags returns the flags a help text lists in its Flags sections:
// the "-p, --provider IDS   ..." lines.
func helpFlags(text string) []string {
	var out []string
	in := false
	for _, l := range strings.Split(text, "\n") {
		switch {
		case isHeading(l):
			in = strings.HasPrefix(l, "Flags")
		case in && strings.HasPrefix(l, "  -"):
			spec := strings.TrimPrefix(l, "  ")
			if i := strings.Index(spec, "  "); i >= 0 {
				spec = spec[:i]
			}
			for _, f := range strings.Split(spec, ", ") {
				out = append(out, strings.Fields(f)[0])
			}
		}
	}
	return out
}

func TestCompletionFlagsCoverHelp(t *testing.T) {
	cases := map[string]string{ // spec key -> help text
		"":            usage,
		"suggest":     commandHelp["suggest"],
		"explain":     commandHelp["explain"],
		"providers":   commandHelp["providers"],
		"init":        commandHelp["init"],
		"setup shell": commandHelp["setup"],
		"completion":  commandHelp["completion"],
		"config add":  commandHelp["config"],
	}
	for key, text := range cases {
		flags := helpFlags(text)
		if len(flags) == 0 {
			t.Errorf("%q: found no flags in its help; the parser or the help changed", key)
		}
		spec := map[string]bool{}
		for _, f := range shellinit.CompletionFlags(key) {
			spec[f] = true
		}
		for _, f := range flags {
			if !spec[f] {
				t.Errorf("%q: help lists %s but completion does not offer it", key, f)
			}
		}
	}
	// and nothing stale the other way, for commands whose help lists every flag
	for _, key := range []string{"suggest", "explain", "providers", "init", "setup shell", "config add"} {
		text := commandHelp[strings.Fields(key)[0]]
		have := map[string]bool{}
		for _, f := range helpFlags(text) {
			have[f] = true
		}
		for _, f := range shellinit.CompletionFlags(key) {
			if !have[f] {
				t.Errorf("%q: completion offers %s but help does not list it", key, f)
			}
		}
	}
}

func TestCompletionCommandsCoverHelp(t *testing.T) {
	have := map[string]bool{}
	for _, c := range shellinit.CompletionCommands {
		have[c.Name] = true
	}
	in := false
	for _, l := range strings.Split(usage, "\n") {
		if isHeading(l) {
			in = l == "Commands" || l == "Topics"
			continue
		}
		if in && strings.HasPrefix(l, "  ") && strings.TrimSpace(l) != "" {
			if name := strings.Fields(l)[0]; !have[name] {
				t.Errorf("help lists command %q but completion does not offer it", name)
			}
		}
	}
	for _, c := range shellinit.CompletionCommands {
		if _, ok := helpTopic(c.Name); !ok && c.Name != "config" {
			t.Errorf("completion offers %q, which has no help", c.Name)
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, sh := range completionShells {
		var out, errb bytes.Buffer
		if err := run(context.Background(), []string{"completion", sh}, &out, &errb); err != nil {
			t.Fatalf("completion %s: %v", sh, err)
		}
		script := out.String()
		want, _ := shellinit.Completion(sh)
		if script != want {
			t.Errorf("%s: `shelp completion` differs from shellinit.Completion", sh)
		}
		for _, c := range shellinit.CompletionCommands {
			if !strings.Contains(script, c.Name) {
				t.Errorf("%s: script lacks command %s", sh, c.Name)
			}
		}
		for _, key := range []string{"", "suggest", "explain", "providers", "init", "setup shell"} {
			for _, f := range shellinit.CompletionFlags(key) {
				if !strings.Contains(script, f) {
					t.Errorf("%s: script lacks flag %s (%q)", sh, f, key)
				}
			}
		}
		for _, v := range []string{"__complete providers", "__complete templates", "os shell cwd tools"} {
			if !strings.Contains(script, v) {
				t.Errorf("%s: script lacks %q", sh, v)
			}
		}
		// shelp init embeds exactly this text
		init, err := shellinit.Script(sh)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(init, script) {
			t.Errorf("%s: `shelp init` does not embed `shelp completion`", sh)
		}
	}
}

func TestCompletionZshRegistersBothWays(t *testing.T) {
	s, _ := shellinit.Completion("zsh")
	for _, want := range []string{"#compdef shelp", "compdef _shelp shelp", "precmd_functions+=(_shelp_register_completion)"} {
		if !strings.Contains(s, want) {
			t.Errorf("zsh completion lacks %q", want)
		}
	}
	if strings.Contains(strings.ReplaceAll(s, "compinit,", ""), "compinit\n") && regexp.MustCompile(`(?m)^\s*(autoload -Uz )?compinit\b`).MatchString(s) {
		t.Error("zsh completion must never call compinit")
	}
}

func TestCompletionUsage(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{"completion"}, {"completion", "powershell"}, {"completion", "zsh", "extra"}} {
		err := run(context.Background(), args, &out, &errb)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v: want a usage error, got %v", args, err)
		}
	}
	out.Reset()
	if err := run(context.Background(), []string{"completion", "-h"}, &out, &errb); err != nil || !strings.Contains(out.String(), "shelp completion") {
		t.Errorf("completion -h: %v %q", err, out.String())
	}
}

func TestCompleteValues(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.toml")
	os.WriteFile(cfg, []byte(`[[openai.endpoint]]
name = "zzhost"
base_url = "http://127.0.0.1:9/v1"
api_key_env = "ZZ_NO_SUCH_KEY"
models = ["alpha-1", "beta-2"]
`), 0o644)
	t.Setenv("SHELP_CONFIG", cfg)
	t.Setenv("XDG_CACHE_HOME", dir)

	var out, errb bytes.Buffer
	if err := run(context.Background(), []string{"__complete", "providers"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	ids := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, want := range []string{"fm", "zzhost:alpha-1", "zzhost:beta-2"} {
		found := false
		for _, id := range ids {
			found = found || id == want
		}
		if !found {
			t.Errorf("providers lacks %q: %v", want, ids)
		}
	}

	out.Reset()
	if err := run(context.Background(), []string{"__complete", "templates"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out.String()), strings.Join(prompt.Names(), "\n"); got != want {
		t.Errorf("templates = %q, want %q", got, want)
	}

	// never noisy: unknown kind, no kind, and a broken config print nothing
	os.WriteFile(cfg, []byte("this is = = not toml"), 0o644)
	for _, args := range [][]string{{"__complete", "bogus"}, {"__complete"}, {"__complete", "providers"}} {
		out.Reset()
		errb.Reset()
		if err := run(context.Background(), args, &out, &errb); err != nil || out.Len() != 0 || errb.Len() != 0 {
			t.Errorf("%v: err=%v out=%q err=%q", args, err, out.String(), errb.String())
		}
	}
}

// TestCompletionSyntax runs each shell's own parser over the completion
// script and over the whole `shelp init` script, when that shell is here.
func TestCompletionSyntax(t *testing.T) {
	check := map[string][]string{"zsh": {"-n"}, "bash": {"-n"}, "fish": {"--no-execute"}}
	for _, sh := range completionShells {
		bin, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("%s not installed; syntax check skipped", sh)
			continue
		}
		for name, gen := range map[string]func(string) (string, error){
			"completion": shellinit.Completion,
			"init":       shellinit.Script,
		} {
			s, err := gen(sh)
			if err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(t.TempDir(), name+"."+sh)
			os.WriteFile(f, []byte(s), 0o644)
			cmd := exec.Command(bin, append(check[sh], f)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s -n on %s script: %v\n%s", sh, name, err, out)
			}
		}
	}
}

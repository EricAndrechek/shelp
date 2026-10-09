package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/shellinit"
)

// Every command's help is reachable three ways, on stdout with no error.
func TestCommandHelpPaths(t *testing.T) {
	setColorEnv(t, "")
	for _, name := range []string{"suggest", "explain", "providers", "setup", "init", "version", "help"} {
		want := commandHelp[name]
		if want == "" || !strings.HasPrefix(want, "shelp "+name+":") {
			t.Fatalf("%s: help text missing or misnamed: %q", name, want)
		}
		paths := [][]string{{"help", name}, {name, "-h"}, {name, "--help"}}
		if name == "version" {
			paths = append(paths, []string{"--version", "-h"})
		}
		for _, args := range paths {
			out, errOut, err := runOut(t, args...)
			if err != nil || out != want || errOut != "" {
				t.Errorf("%v: got (%q, %q, %v), want help on stdout", args, out, errOut, err)
			}
		}
	}
	// explain's alias.
	if out, _, err := runOut(t, "-e", "-h"); err != nil || out != commandHelp["explain"] {
		t.Errorf("-e -h: got (%q, %v)", out, err)
	}
	// setup shell -h and setup shortcuts -h show setup's help.
	for _, args := range [][]string{{"setup", "shell", "-h"}, {"setup", "shortcuts", "-h"}, {"setup", "--help"}} {
		if out, _, err := runOut(t, args...); err != nil || out != commandHelp["setup"] {
			t.Errorf("%v: got (%q, %v)", args, out, err)
		}
	}
	// init -h anywhere.
	if out, _, err := runOut(t, "init", "zsh", "-h"); err != nil || out != commandHelp["init"] {
		t.Errorf("init zsh -h: got (%q, %v)", out, err)
	}
}

func TestHelpContents(t *testing.T) {
	for name, parts := range map[string][]string{
		"suggest":   {"-p, --provider", "--shell", "--template", "--context", "--json", "-v, --verbose", "--force"},
		"explain":   {"-p, --provider", "--shell", "--template", "--context", "--json", "--force", "-e"},
		"providers": {"--all", "--json", "--reset-cooldowns"},
		"setup":     {"shelp setup shell", "shelp setup shortcuts", "--shell", "--bind", "--dry-run"},
		"init":      {strings.Join(shellinit.Shells(), "|"), "--bind", "--no-line-mode", `eval "$(shelp init zsh)"`},
	} {
		for _, w := range parts {
			if !strings.Contains(commandHelp[name], w) {
				t.Errorf("%s help lacks %q", name, w)
			}
		}
	}
}

// Natural language that starts with a command word is a task, not help or
// version. These go to suggest, which fails here only because the config
// names no usable provider; what matters is the error is not help output.
func TestCollisionsAreTasks(t *testing.T) {
	setColorEnv(t, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SHELP_PROVIDER", "nosuch:model") // never reach a real backend
	for _, args := range [][]string{
		{"help", "me", "find", "big", "files"},
		{"help", "find"},
		{"version", "of", "node", "installed"},
		{"--version", "x"},
		{"help", "suggest", "me"},
	} {
		out, _, _ := runOut(t, args...)
		if out == usage || out == "shelp "+version+"\n" || out == commandHelp["help"] {
			t.Errorf("%v: treated as help/version", args)
		}
		for _, h := range commandHelp {
			if out == h {
				t.Errorf("%v: printed a command's help", args)
			}
		}
	}
}

func TestSetupNoTarget(t *testing.T) {
	setColorEnv(t, "")
	out, errOut, err := runOut(t, "setup")
	if err != nil || out != commandHelp["setup"] || errOut != "" {
		t.Errorf("setup: got (%q, %q, %v)", out, errOut, err)
	}
	if !strings.Contains(out, "shelp setup shell") || !strings.Contains(out, "shelp setup shortcuts") {
		t.Error("setup help does not list its targets")
	}
	if strings.Contains(out, "==") {
		t.Error("setup printed a step header")
	}
	out, errOut, err = runOut(t, "setup", "nope")
	var ue usageError
	if !errors.As(err, &ue) || out != "" || errOut != commandHelp["setup"] {
		t.Errorf("setup nope: got (%q, %q, %v), want setup help on stderr and a usageError", out, errOut, err)
	}
}

// A flag error prints that command's help on stderr and exits 2.
func TestFlagErrorsPrintCommandHelp(t *testing.T) {
	setColorEnv(t, "")
	for _, c := range []struct {
		args []string
		help string
	}{
		{[]string{"--nope", "x"}, "suggest"},
		{[]string{"suggest", "--nope", "x"}, "suggest"},
		{[]string{"explain", "--nope", "x"}, "explain"},
		{[]string{"-e", "--nope", "x"}, "explain"},
		{[]string{"providers", "--nope"}, "providers"},
		{[]string{"setup", "shell", "--nope"}, "setup"},
		{[]string{"setup", "shell", "extra"}, "setup"},
		{[]string{"init"}, "init"},
		{[]string{"init", "--bind"}, "init"},
		{[]string{"explain"}, "explain"},
	} {
		out, errOut, err := runOut(t, c.args...)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v: err = %v, want a usageError", c.args, err)
			continue
		}
		if code, show := exitCode(t.Context(), err); code != 2 || !show {
			t.Errorf("%v: exit %d, show %v; want 2, true", c.args, code, show)
		}
		if out != "" || errOut != commandHelp[c.help] {
			t.Errorf("%v: got stdout %q, stderr %q; want only %s help on stderr", c.args, out, errOut, c.help)
		}
	}
}

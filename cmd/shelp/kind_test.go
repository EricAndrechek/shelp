package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kindCases: every command, and the shapes where a command word starts a
// task. want is what `shelp __kind ARGS...` prints.
var kindCases = []struct {
	args []string
	want string
}{
	{nil, "command"}, // bare `shelp` is help
	{[]string{"-h"}, "command"},
	{[]string{"--help"}, "command"},
	{[]string{"--help", "anything", "else"}, "command"},

	{[]string{"help"}, "command"},
	{[]string{"help", "providers"}, "command"},
	{[]string{"help", "config"}, "command"},
	{[]string{"help", "-e"}, "command"},
	{[]string{"help", "-h"}, "command"},
	{[]string{"help", "--help"}, "command"},
	{[]string{"help", "me", "find", "files"}, "task"},
	{[]string{"help", "find"}, "task"},
	{[]string{"help", "providers", "please"}, "task"},
	{[]string{"help", "suggest", "me"}, "task"},
	{[]string{"help", ""}, "task"},

	{[]string{"version"}, "command"},
	{[]string{"--version"}, "command"},
	{[]string{"version", "-h"}, "command"},
	{[]string{"--version", "--help"}, "command"},
	{[]string{"version", "of", "node"}, "task"},
	{[]string{"version", "of", "node", "installed"}, "task"},
	{[]string{"--version", "x"}, "task"},
	{[]string{"version", "-h", "x"}, "command"}, // a flag first is version's own

	{[]string{"config"}, "command"},
	{[]string{"config", "chain"}, "command"},
	{[]string{"config", "chain", "fm,claude:haiku"}, "command"},
	{[]string{"config", "edit"}, "command"},
	{[]string{"config", "-h"}, "command"},
	{[]string{"config", "--help"}, "command"},
	{[]string{"config", "of", "nginx"}, "task"},
	{[]string{"config", "-i"}, "command"},
	{[]string{"config", "--interactive"}, "command"},
	{[]string{"config", "--bogus"}, "command"},
	{[]string{"config", "-x", "nginx"}, "command"},
	{[]string{"version", "-x"}, "command"},
	{[]string{"version", "--bogus", "of", "node"}, "command"},
	{[]string{"help", "-x"}, "command"},
	{[]string{"help", "--bogus", "me"}, "command"},
	{[]string{"config", "-"}, "task"},
	{[]string{"config", "--"}, "task"},
	// suggest and -- are the ways to ask the model
	{[]string{"--", "config", "-i"}, "task"},
	{[]string{"suggest", "config", "-i"}, "command"}, // suggest's, a task for the model
	{[]string{"config", "files", "for", "nginx"}, "task"},
	{[]string{"config", "add"}, "command"}, // run's usage error
	{[]string{"config", "add", "-h"}, "command"},
	{[]string{"config", "add", "endpoint", "or", "--preset", "openrouter", "--model", "a/b"}, "command"},
	{[]string{"config", "add", "endpoint"}, "command"},
	{[]string{"config", "add", "local", "box", "--base-url", "http://10.0.0.2/v1"}, "command"},
	{[]string{"config", "add", "a", "cron", "job"}, "task"},
	{[]string{"config", "add", "user", "to", "sudoers"}, "task"},
	{[]string{"config", "remove"}, "command"}, // run's usage error
	{[]string{"config", "remove", "openrouter"}, "command"},
	{[]string{"config", "remove", "-h"}, "command"},
	{[]string{"config", "remove", "old", "kernels"}, "task"},

	{[]string{"history"}, "command"},
	{[]string{"history", "-n", "5"}, "command"},
	{[]string{"history", "--all"}, "command"},
	{[]string{"history", "--json", "-s", "git"}, "command"},
	{[]string{"history", "--search=git", "--provider", "fm"}, "command"},
	{[]string{"history", "-s", "find big files"}, "command"}, // a value, not a task
	{[]string{"history", "-h"}, "command"},
	{[]string{"history", "show", "3"}, "command"},
	{[]string{"history", "clear", "--yes"}, "command"},
	{[]string{"history", "path"}, "command"},
	{[]string{"history", "of", "git", "commits", "for", "main.go"}, "task"},
	{[]string{"history", "-n", "5", "of", "git"}, "command"}, // flag first: history's own error
	{[]string{"history", "--nope"}, "command"},
	{[]string{"history", "git"}, "task"},
	{[]string{"History"}, "task"},

	{[]string{"setup"}, "command"},
	{[]string{"setup", "shell"}, "command"},
	{[]string{"setup", "shell", "--bind", "^G"}, "command"},
	{[]string{"setup", "shortcuts"}, "command"},
	{[]string{"setup", "bogus"}, "command"}, // run's error: unknown setup target

	{[]string{"explain"}, "command"},
	{[]string{"explain", "ls", "-la"}, "command"},
	{[]string{"-e", "ls -la"}, "command"},
	{[]string{"suggest"}, "command"},
	{[]string{"suggest", "find", "big", "files"}, "command"},
	{[]string{"providers"}, "command"},
	{[]string{"providers", "--json"}, "command"},
	{[]string{"init", "zsh"}, "command"},
	{[]string{"init"}, "command"},
	{[]string{"completion", "fish"}, "command"},
	{[]string{"__complete", "providers"}, "command"},
	{[]string{"__kind"}, "command"},
	{[]string{"__kind", "find", "files"}, "command"},

	{[]string{"find", "big", "files"}, "task"},
	{[]string{"zzcmd"}, "task"},
	{[]string{"Config"}, "task"},
	{[]string{"configure", "nginx"}, "task"},
	{[]string{"helpme"}, "task"},
	{[]string{"what's", "my", "ip"}, "task"},
	{[]string{""}, "task"},
	// flags first: the whole list is suggest's (run parses them there)
	{[]string{"-p", "x", "task"}, "task"},
	{[]string{"-p", "fm", "find", "files"}, "task"},
	{[]string{"--provider=fm", "find", "files"}, "task"},
	{[]string{"--json", "list", "files"}, "task"},
	{[]string{"-v", "config"}, "task"},
}

func TestCommandFor(t *testing.T) {
	for _, c := range kindCases {
		got := "task"
		if commandFor(c.args) != "" {
			got = "command"
		}
		if got != c.want {
			t.Errorf("commandFor(%q) = %q: a %s, want a %s", c.args, commandFor(c.args), got, c.want)
		}
	}
}

// `shelp __kind` prints exactly one word and never fails -- and reads
// nothing: a malformed config, which every command that loads one
// reports, doesn't touch it.
func TestKindOutput(t *testing.T) {
	setColorEnv(t, "")
	bad := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(bad, []byte("providers = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELP_CONFIG", bad)
	t.Setenv("SHELP_PROVIDER", "nosuch:model") // a miss must never reach a real backend
	if _, err := loadConfig(); err == nil {
		t.Fatal("the malformed config loaded; this test proves nothing")
	}
	for _, c := range kindCases {
		out, errOut, err := runOut(t, append([]string{"__kind"}, c.args...)...)
		if err != nil || errOut != "" || out != c.want+"\n" {
			t.Errorf("__kind %q: got (%q, %q, %v), want (%q, \"\", nil)", c.args, out, errOut, err, c.want+"\n")
		}
	}
}

// run and `shelp __kind` agree: with every handler replaced by a recorder
// (except __kind's own), each case runs the handler __kind's answer says
// it does -- the subcommand commandFor names, or the task path.
func TestRunAgreesWithKind(t *testing.T) {
	realCommands, realTask := commands, runTask
	t.Cleanup(func() { commands, runTask = realCommands, realTask })

	var got string
	commands = map[string]func(context.Context, []string, io.Writer, io.Writer) error{}
	for name := range realCommands {
		commands[name] = func(context.Context, []string, io.Writer, io.Writer) error {
			got = "command " + name
			return nil
		}
	}
	commands["__kind"] = realCommands["__kind"]
	runTask = func(context.Context, []string, io.Writer, io.Writer) error {
		got = "task"
		return nil
	}

	reached := map[string]bool{}
	for _, c := range kindCases {
		got = ""
		var out, errb bytes.Buffer
		if err := run(context.Background(), c.args, &out, &errb); err != nil {
			t.Errorf("run(%q): %v", c.args, err)
			continue
		}
		dispatched := got
		if name := strings.TrimPrefix(dispatched, "command "); name != dispatched {
			reached[name] = true
		}
		if len(c.args) > 0 && c.args[0] == "__kind" {
			reached["__kind"] = true
			continue // ran the real __kind, which records nothing
		}
		out.Reset()
		if err := run(context.Background(), append([]string{"__kind"}, c.args...), &out, &errb); err != nil {
			t.Fatalf("__kind %q: %v", c.args, err)
		}
		kind := strings.TrimSpace(out.String())
		want := "task"
		if kind == "command" {
			want = "command " + commandFor(c.args)
		}
		if dispatched != want {
			t.Errorf("%q: run dispatched to %q, but __kind says %q", c.args, dispatched, kind)
		}
	}
	// every command has a case above, so a new one can't go untested
	for name := range realCommands {
		if !reached[name] {
			t.Errorf("command %q: no case in kindCases reaches it; add one", name)
		}
	}
}

// A flag right after help/version/config/history is that command's: a
// mistyped one is a usage error (exit 2) with the command's help on
// stderr, never a model call (runTask is replaced by one that fails).
func TestCommandFlagErrors(t *testing.T) {
	realTask := runTask
	t.Cleanup(func() { runTask = realTask })
	runTask = func(context.Context, []string, io.Writer, io.Writer) error {
		t.Error("a task reached the model")
		return nil
	}
	t.Setenv("SHELP_HISTORY", "0")
	for _, args := range [][]string{
		{"config", "--bogus"}, {"config", "-x", "nginx"}, {"version", "-x"},
		{"help", "-x"}, {"history", "--nope"},
	} {
		_, errOut, err := runOut(t, args...)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%q: err = %v, want a usage error", args, err)
		}
		if !strings.Contains(errOut, "shelp "+args[0]) {
			t.Errorf("%q: stderr lacks the command's help: %q", args, errOut)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/style"
)

func setColorEnv(t *testing.T, force string) {
	t.Helper()
	for _, k := range []string{"NO_COLOR", "TERM", "COLORTERM"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLICOLOR_FORCE", force)
}

func runOut(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = run(context.Background(), args, &out, &errb)
	return out.String(), errb.String(), err
}

// TestBannerPlain pins the output scripts see (not a terminal).
func TestBannerPlain(t *testing.T) {
	setColorEnv(t, "")
	for _, arg := range []string{"version", "--version"} {
		out, errOut, err := runOut(t, arg)
		if err != nil || out != "shelp "+version+"\n" || errOut != "" {
			t.Errorf("%s: got (%q, %q, %v), want (%q, \"\", nil)", arg, out, errOut, err, "shelp "+version+"\n")
		}
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		out, errOut, err := runOut(t, args...)
		if err != nil || out != usage || errOut != "" {
			t.Errorf("%v: not exactly usage on stdout (err %v, stderr %q):\n%s", args, err, errOut, out)
		}
	}
	out, errOut, err := runOut(t)
	if err != nil || out != usage || errOut != "" {
		t.Errorf("no args: got (stdout %q, stderr %q, err %v), want usage on stdout and no error", out, errOut, err)
	}
	// The short top-level help names every command and the help routes.
	for _, w := range []string{"shelp turns a task", "explain ", "providers ", "setup ", "init ", "version ", "help ", "-h, --help", `Run "shelp help <command>"`, `"shelp help config"`, "Topics"} {
		if !strings.Contains(usage, w) {
			t.Errorf("usage lacks %q", w)
		}
	}
	if n := strings.Count(usage, "\n"); n > 25 {
		t.Errorf("top-level help is %d lines, want it short", n)
	}
}

// TestBannerTerminal checks the colored layout: the logo on top, then the
// same text as the plain output.
func TestBannerTerminal(t *testing.T) {
	setColorEnv(t, "1")
	logo := style.Logo(style.Plain())

	out, _, err := runOut(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	want := logo + "\n\nshelp " + version + "\n" + versionTagline + "\n"
	if got := style.Strip(out); got != want {
		t.Errorf("version: got\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("version: no color codes")
	}

	out, _, err = runOut(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if got := style.Strip(out); got != logo+"\n\n"+usage {
		t.Errorf("--help: stripped output is not logo + usage:\n%s", got)
	}
	for _, h := range []string{"Usage", "Commands", "Flags"} {
		if !strings.Contains(out, style.Forced(style.Color16).Bold(style.Forced(style.Color16).Accent(h))) {
			t.Errorf("--help: heading %q is not bold+accent", h)
		}
	}

	out, errOut, _ := runOut(t)
	if got := style.Strip(out); got != logo+"\n\n"+usage || errOut != "" {
		t.Errorf("no args: stripped stdout is not logo + usage:\n%s", got)
	}
}

// A command's help on a terminal has no logo, colored headings, and the
// same text once stripped.
func TestCommandHelpTerminal(t *testing.T) {
	setColorEnv(t, "1")
	out, _, err := runOut(t, "help", "setup")
	if err != nil {
		t.Fatal(err)
	}
	if got := style.Strip(out); got != commandHelp["setup"] {
		t.Errorf("stripped setup help differs:\n%s", got)
	}
	p := style.Forced(style.Color16)
	for _, h := range []string{"Usage", "Flags (setup shell)"} {
		if !strings.Contains(out, p.Bold(p.Accent(h))) {
			t.Errorf("heading %q is not bold+accent", h)
		}
	}
}

func TestIsHeading(t *testing.T) {
	for l, want := range map[string]bool{
		"Usage": true, "Flags (setup shell)": true, "Commands": true,
		"": false, "  Usage": false, "usage": false, "Usage:": false,
		"Run \"shelp help <command>\" for a command's flags.":       false,
		"shelp turns a task into a shell command, or explains one.": false,
	} {
		if got := isHeading(l); got != want {
			t.Errorf("isHeading(%q) = %v, want %v", l, got, want)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
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

// TestBannerPlain pins the output scripts see (not a terminal): byte for
// byte what shelp printed before it had a logo.
func TestBannerPlain(t *testing.T) {
	setColorEnv(t, "")
	for _, arg := range []string{"version", "--version"} {
		out, errOut, err := runOut(t, arg)
		if err != nil || out != "shelp "+version+"\n" || errOut != "" {
			t.Errorf("%s: got (%q, %q, %v), want (%q, \"\", nil)", arg, out, errOut, err, "shelp "+version+"\n")
		}
	}
	for _, arg := range []string{"-h", "--help", "help"} {
		out, errOut, err := runOut(t, arg)
		if err != nil || out != usage || errOut != "" {
			t.Errorf("%s: not exactly usage on stdout (err %v, stderr %q):\n%s", arg, err, errOut, out)
		}
	}
	out, errOut, err := runOut(t)
	var ue usageError
	if !errors.As(err, &ue) || out != "" || errOut != usage {
		t.Errorf("no args: got (stdout %q, err %v), want usage on stderr and a usageError:\n%s", out, err, errOut)
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
	for _, h := range []string{"Usage:", "Flags (suggest/explain):", "Flags (init):", "Flags (setup shell):"} {
		if !strings.Contains(out, style.Forced(style.Color16).Bold(style.Forced(style.Color16).Accent(h))) {
			t.Errorf("--help: heading %q is not bold+accent", h)
		}
	}

	_, errOut, _ := runOut(t)
	if got := style.Strip(errOut); got != logo+"\n\n"+usage {
		t.Errorf("no args: stripped stderr is not logo + usage:\n%s", got)
	}
}

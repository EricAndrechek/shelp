package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// Enter on a provider that needs the Apple Shortcut: the screen is left,
// the fix is explained, setup runs (through the seam) when accepted, and
// the shortcut rows are checked again once back.
func TestManagerFixRunsShortcutsSetup(t *testing.T) {
	isolateCfg(t, "y\n", true)
	var setupRan atomic.Bool
	fakes := append(listProviders(nil), fakeProv{"shortcuts:chatgpt", "ChatGPT", provider.AppleCloud,
		provider.Status{Reason: "not installed", Fix: "run `shelp setup shortcuts`"}})
	listProviders = func(*config.Config) []provider.Provider { return fakes }
	probeProviders = func(ctx context.Context, ps []provider.Provider, _ map[string]cooldown.Entry) []provider.Status {
		out := make([]provider.Status, len(ps))
		for i, p := range ps {
			out[i] = p.Detect(ctx)
			if setupRan.Load() && strings.HasPrefix(p.ID(), "shortcuts:") {
				out[i] = provider.Status{Available: true}
			}
		}
		return out
	}
	oldStep := runShortcutsStep
	runShortcutsStep = func(_ context.Context, _ *config.Config, stdout, _ io.Writer) error {
		setupRan.Store(true)
		io.WriteString(stdout, "shortcut installed\n")
		return nil
	}
	t.Cleanup(func() { runShortcutsStep = oldStep })
	// #1, #2 are the default chain; the first other is shortcuts:chatgpt
	// (needs setup; Apple's cloud sorts before claude).
	scr := newFakeScreen(keys("j", "j", kEnter, "q")...)
	withScreen(t, scr)
	out, _, err := runWithTimeout(t, "providers", "-i")
	t.Logf("stdout:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !setupRan.Load() {
		t.Fatal("setup did not run")
	}
	for _, w := range []string{"shortcuts:chatgpt  ChatGPT", "not installed", "Run `shelp setup shortcuts`.",
		"Set up the Apple Shortcut now?", "shortcut installed", "press any key to go back", "no changes"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
	if !strings.Contains(scr.last(), "✓ shortcuts:chatgpt") {
		t.Errorf("not checked again:\n%s", scr.last())
	}
	if scr.started || scr.starts != scr.stops || scr.starts < 2 {
		t.Errorf("screen: %d starts, %d stops, started %v", scr.starts, scr.stops, scr.started)
	}
}

// Ctrl-C at "press any key" ends shelp like Ctrl-C anywhere: 130.
func TestManagerFixCtrlCAtPressAnyKey(t *testing.T) {
	isolateCfg(t, "", true)
	scr := newFakeScreen(keys("j", "j", kEnter)...) // claude:haiku: needs setup
	scr.waitKeys = []term.Key{term.CtrlC}
	withScreen(t, scr)
	out, _, err := runWithTimeout(t, "providers", "--interactive")
	if code, show := exitCode(context.Background(), err); code != 130 || show {
		t.Errorf("exit %d show %v err %v", code, show, err)
	}
	if !strings.Contains(out, "Install the claude CLI.") {
		t.Errorf("fix text missing:\n%s", out)
	}
	if scr.started {
		t.Error("screen left started")
	}
}

// Saving from the manager to a chezmoi-managed file leaves the screen,
// shows the change and takes the offer.
func TestManagerSaveChezmoi(t *testing.T) {
	e := isolateCfg(t, "y\n", true)
	write(t, e.cfgPath, userCfg)
	source, applied := chezmoiFakes(t, "config.toml", userCfg)
	scr := newFakeScreen(keys("J", "s", "q")...)
	withScreen(t, scr)
	out, _, err := runWithTimeout(t, "config", "chain")
	t.Logf("stdout:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scr.frames[1], "managed by chezmoi") {
		t.Errorf("header does not say the file is managed:\n%s", scr.frames[1])
	}
	if got := read(t, source); !strings.Contains(got, `providers = ["fm", "shortcuts:apple-pro"]`) {
		t.Errorf("source:\n%s", got)
	}
	if len(*applied) != 1 || read(t, e.cfgPath) != userCfg {
		t.Errorf("apply calls %v; target written directly: %v", *applied, read(t, e.cfgPath) != userCfg)
	}
	if !strings.Contains(out, "saved fm → shortcuts:apple-pro via chezmoi") {
		t.Errorf("summary missing:\n%s", out)
	}
}

// Declining the chezmoi offer keeps the changes pending: q then asks.
func TestManagerSaveChezmoiDeclined(t *testing.T) {
	e := isolateCfg(t, "n\n", true)
	write(t, e.cfgPath, userCfg)
	source, _ := chezmoiFakes(t, "config.toml", userCfg)
	scr := newFakeScreen(keys("J", "s", "q", "y")...)
	withScreen(t, scr)
	out, _, err := runWithTimeout(t, "config", "chain")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, source) != userCfg || !strings.Contains(out, "changes discarded") {
		t.Errorf("source changed, or no discard:\n%s", out)
	}
	found := false
	for _, f := range scr.frames {
		found = found || strings.Contains(f, "Discard your unsaved changes?")
	}
	if !found {
		t.Error("q did not ask about the unsaved changes")
	}
}

func loopManager(t *testing.T) *manager {
	t.Helper()
	isolateCfg(t, "", true)
	m, err := newManager(style.Plain(), "/tmp/c.toml", "", listProviders)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The terminal is put back when the loop panics.
func TestManagerLoopRestoresOnPanic(t *testing.T) {
	m := loopManager(t)
	scr := newFakeScreen()
	scr.onDraw = func(n int) {
		if n == 1 {
			panic("boom")
		}
	}
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Errorf("recovered %v", r)
			}
		}()
		managerLoop(context.Background(), m, scr, io.Discard, io.Discard)
	}()
	if scr.started || scr.stops != 1 {
		t.Errorf("not restored: started %v, %d stops", scr.started, scr.stops)
	}
}

// SIGTERM (and SIGHUP) are caught so the terminal is restored, then shelp
// exits 128+signal without a message.
func TestManagerLoopSIGTERM(t *testing.T) {
	m := loopManager(t)
	scr := newFakeScreen()
	scr.onDraw = func(n int) {
		if n == 0 {
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}
	err := managerLoop(context.Background(), m, scr, io.Discard, io.Discard)
	if code, show := exitCode(context.Background(), err); code != 143 || show {
		t.Errorf("exit %d show %v err %v", code, show, err)
	}
	if scr.started {
		t.Error("not restored")
	}
}

// SIGINT from outside cancels main's context: the loop restores and ends.
func TestManagerLoopContextCancel(t *testing.T) {
	m := loopManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	scr := newFakeScreen()
	scr.onDraw = func(int) { cancel() }
	err := managerLoop(ctx, m, scr, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) || scr.started {
		t.Errorf("err %v started %v", err, scr.started)
	}
	if code, _ := exitCode(ctx, err); code != 130 {
		t.Errorf("exit %d", code)
	}
}

// A backend whose Detect panics shows as a failed check, not a crash.
func TestProbeRowRecovers(t *testing.T) {
	isolateCfg(t, "", true)
	boom := func(context.Context, []provider.Provider, map[string]cooldown.Entry) []provider.Status {
		panic("detect exploded")
	}
	r := probeRow(context.Background(), boom, fakeProv{"x", "X", provider.API, provider.Status{}}, nil)
	if r.Status != statusUnavailable || !strings.Contains(r.Reason, "detect exploded") {
		t.Errorf("row %+v", r)
	}
}

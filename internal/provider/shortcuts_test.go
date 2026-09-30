package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

func TestDefaultShortcutName(t *testing.T) {
	cases := []struct {
		v    shortcutVariant
		want string
	}{
		{shortcutVariant{model: "Apple Intelligence", web: false}, "shelp · Apple Intelligence"},
		{shortcutVariant{model: "Apple Intelligence", web: true}, "shelp · Apple Intelligence + Web"},
		{shortcutVariant{model: "Apple Intelligence Pro", web: false}, "shelp · Apple Intelligence Pro"},
		{shortcutVariant{model: "Apple Intelligence Pro", web: true}, "shelp · Apple Intelligence Pro + Web"},
		{shortcutVariant{model: "ChatGPT", web: false}, "shelp · ChatGPT"},
		{shortcutVariant{model: "ChatGPT", web: true}, "shelp · ChatGPT + Web"},
	}
	for _, tc := range cases {
		if got := defaultShortcutName(tc.v); got != tc.want {
			t.Errorf("defaultShortcutName(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestResolveNameOverride(t *testing.T) {
	v, ok := variantByKey("apple-pro")
	if !ok {
		t.Fatal("apple-pro variant not found")
	}
	cfg := shortcutsConfig{Names: map[string]string{"apple-pro": "Ask AI"}}
	if got := resolveName(v, cfg); got != "Ask AI" {
		t.Errorf("resolveName with override = %q, want %q", got, "Ask AI")
	}
	// Blank override falls back to the default, not an empty name.
	cfg = shortcutsConfig{Names: map[string]string{"apple-pro": "  "}}
	if got := resolveName(v, cfg); got != defaultShortcutName(v) {
		t.Errorf("resolveName with blank override = %q, want default %q", got, defaultShortcutName(v))
	}
	// No entry at all: default.
	cfg = shortcutsConfig{}
	if got := resolveName(v, cfg); got != defaultShortcutName(v) {
		t.Errorf("resolveName with no config = %q, want default %q", got, defaultShortcutName(v))
	}
}

func TestVariantByKey(t *testing.T) {
	if _, ok := variantByKey("apple-pro"); !ok {
		t.Error("expected apple-pro to be found")
	}
	if _, ok := variantByKey("nonexistent"); ok {
		t.Error("expected nonexistent key to be not-found")
	}
	// Provider IDs (with the "shortcuts:" prefix) are not config keys.
	if _, ok := variantByKey("shortcuts:apple-pro"); ok {
		t.Error("variantByKey should not match full provider IDs")
	}
}

func TestInstallList(t *testing.T) {
	t.Setenv("SHELP_SHORTCUTS", "")

	if got := installList(shortcutsConfig{}); len(got) != 1 || got[0] != "apple-pro" {
		t.Errorf("default install list = %v, want [apple-pro]", got)
	}

	cfg := shortcutsConfig{Install: []string{"apple", "chatgpt"}}
	if got := installList(cfg); len(got) != 2 || got[0] != "apple" || got[1] != "chatgpt" {
		t.Errorf("config install list = %v, want [apple chatgpt]", got)
	}

	t.Setenv("SHELP_SHORTCUTS", "all")
	if got := installList(cfg); len(got) != len(shortcutVariants) {
		t.Errorf("SHELP_SHORTCUTS=all gave %d entries, want %d", len(got), len(shortcutVariants))
	}

	t.Setenv("SHELP_SHORTCUTS", "apple, chatgpt-web")
	got := installList(cfg)
	want := []string{"apple", "chatgpt-web"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("SHELP_SHORTCUTS=apple,chatgpt-web gave %v, want %v", got, want)
	}
}

func TestParseShortcutsList(t *testing.T) {
	out := "Ask AI\nNew Shortcut\n\n  Translate with Google Lens  \n"
	set := parseShortcutsList(out)
	for _, want := range []string{"Ask AI", "New Shortcut", "Translate with Google Lens"} {
		if !set[want] {
			t.Errorf("parseShortcutsList: missing %q", want)
		}
	}
	if set[""] {
		t.Error("parseShortcutsList: blank lines should not produce an empty-string entry")
	}
	if len(set) != 3 {
		t.Errorf("parseShortcutsList: got %d entries, want 3", len(set))
	}
}

func TestExtractModelIDs(t *testing.T) {
	logText := `
2026-09-30 08:30:01 PrivateMLClientInferenceProviderService noise noise
model=com.apple.fm.language.instruct_server_v2.shortcuts_use_model_pro requestID=abc
2026-09-30 08:30:02 more noise mentioning com.apple.fm.language.instruct_server_v2.shortcuts_use_model_pro again
2026-09-30 08:30:03 com.apple.something.else.entirely engaged
`
	got := extractModelIDs(logText)
	want := []string{
		"com.apple.fm.language.instruct_server_v2.shortcuts_use_model_pro",
		"com.apple.something.else.entirely",
	}
	if len(got) != len(want) {
		t.Fatalf("extractModelIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extractModelIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestVerifyBackendArgsNeverUsesBareLog(t *testing.T) {
	args := VerifyBackendArgs(time.Now())
	if args[0] != "/usr/bin/log" {
		t.Fatalf("VerifyBackendArgs[0] = %q, want /usr/bin/log (bare `log` is a zsh builtin that silently no-ops)", args[0])
	}
	if args[1] != "show" {
		t.Errorf("VerifyBackendArgs[1] = %q, want show", args[1])
	}
}

// --- Provider.Generate error paths (real `shortcuts` calls, no PCC/network) ---

func TestShortcutsProviderGenerateNotInstalled(t *testing.T) {
	if _, ok := LookPath("shortcuts"); !ok {
		t.Skip("shortcuts CLI not on PATH")
	}
	p := &shortcutsProvider{
		variant: shortcutVariant{id: "shortcuts:apple", model: "Apple Intelligence"},
		name:    "shelp-definitely-not-a-real-shortcut-name-xyz",
	}
	_, err := p.Generate(context.Background(), Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected an error for a nonexistent shortcut")
	}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v (%T), want *UnavailableError", err, err)
	}
	if !strings.Contains(ue.Reason, "not installed") {
		t.Errorf("UnavailableError.Reason = %q, want it to mention \"not installed\"", ue.Reason)
	}
	if !strings.Contains(ue.Reason, "shelp setup shortcuts") {
		t.Errorf("UnavailableError.Reason = %q, want it to mention `shelp setup shortcuts`", ue.Reason)
	}
}

// --- Setup step control flow (openCmd/shortcutsListCmd faked; no dialogs) ---

// fakeInstalled and callLog let tests fake `shortcuts list` (via
// shortcutsListCmd) and record/fake `open` (via openCmd) without ever
// touching the real Shortcuts app.
type setupFakes struct {
	mu        sync.Mutex
	installed map[string]bool // name -> installed, mutated to simulate the user clicking Add Shortcut
	opened    []string        // paths passed to openCmd, in order
	listCalls int
}

func installFakes(t *testing.T, f *setupFakes) {
	t.Helper()
	origList, origOpen := shortcutsListCmd, openCmd
	origInterval, origTimeout := shortcutsPollInterval, shortcutsPollTimeout
	t.Cleanup(func() {
		shortcutsListCmd, openCmd = origList, origOpen
		shortcutsPollInterval, shortcutsPollTimeout = origInterval, origTimeout
	})
	shortcutsPollInterval = time.Millisecond
	shortcutsPollTimeout = 20 * time.Millisecond

	shortcutsListCmd = func(ctx context.Context) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.listCalls++
		var names []string
		for n, ok := range f.installed {
			if ok {
				names = append(names, n)
			}
		}
		return strings.Join(names, "\n"), nil
	}
	openCmd = func(ctx context.Context, path string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.opened = append(f.opened, path)
		return nil
	}
}

func TestRunShortcutsSetupSkipsAlreadyInstalled(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{"Ask AI": true}}
	installFakes(t, f)

	cfg, err := config.Parse(`[shortcuts]
names = { "apple-pro" = "Ask AI" }
install = ["apple-pro"]
`)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}

	var stdout, stderr strings.Builder
	if err := runShortcutsSetup(context.Background(), cfg, &stdout, &stderr); err != nil {
		t.Fatalf("runShortcutsSetup: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.opened) != 0 {
		t.Errorf("open was called %d times, want 0 (already installed)", len(f.opened))
	}
	if !strings.Contains(stdout.String(), "already installed") {
		t.Errorf("stdout = %q, want a mention of \"already installed\"", stdout.String())
	}
}

func TestRunShortcutsSetupInstallsAndPolls(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{}}
	installFakes(t, f)

	// Simulate the user clicking "Add Shortcut" shortly after openCmd is
	// called for real: flip the shortcut on after a couple of `list` polls.
	realOpen := openCmd
	openCmd = func(ctx context.Context, path string) error {
		if err := realOpen(ctx, path); err != nil {
			return err
		}
		go func() {
			time.Sleep(3 * time.Millisecond)
			f.mu.Lock()
			f.installed["shelp · Apple Intelligence"] = true
			f.mu.Unlock()
		}()
		return nil
	}

	cfg, err := config.Parse(`[shortcuts]
install = ["apple"]
`)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}

	var stdout, stderr strings.Builder
	if err := runShortcutsSetup(context.Background(), cfg, &stdout, &stderr); err != nil {
		t.Fatalf("runShortcutsSetup: %v", err)
	}

	f.mu.Lock()
	opened := append([]string(nil), f.opened...)
	f.mu.Unlock()
	if len(opened) != 1 {
		t.Fatalf("open was called %d times, want 1", len(opened))
	}
	if !strings.Contains(opened[0], "shelp · Apple Intelligence") {
		t.Errorf("opened path = %q, want it to derive from the shortcut name", opened[0])
	}
	if !strings.Contains(stdout.String(), "installed") {
		t.Errorf("stdout = %q, want it to report the install", stdout.String())
	}
	if strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stderr = %q, did not expect a timeout", stderr.String())
	}
}

func TestRunShortcutsSetupTimesOut(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{}} // never flips to installed
	installFakes(t, f)

	cfg, err := config.Parse(`[shortcuts]
install = ["apple"]
`)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}

	var stdout, stderr strings.Builder
	if err := runShortcutsSetup(context.Background(), cfg, &stdout, &stderr); err != nil {
		t.Fatalf("runShortcutsSetup: %v", err)
	}

	f.mu.Lock()
	opened := len(f.opened)
	f.mu.Unlock()
	if opened != 1 {
		t.Fatalf("open was called %d times, want 1", opened)
	}
	if !strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stderr = %q, want a timeout message", stderr.String())
	}
	if !strings.Contains(stdout.String(), "not installed") {
		t.Errorf("stdout summary = %q, want it to list the variant as not installed", stdout.String())
	}
}

func TestRunShortcutsSetupUnknownVariantIsSkippedNotFatal(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{}}
	installFakes(t, f)

	cfg, err := config.Parse(`[shortcuts]
install = ["not-a-real-variant", "apple"]
`)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	// Let "apple" install immediately so this test stays fast.
	f.installed["shelp · Apple Intelligence"] = false
	openCmd = func(ctx context.Context, path string) error {
		f.mu.Lock()
		f.opened = append(f.opened, path)
		f.installed["shelp · Apple Intelligence"] = true
		f.mu.Unlock()
		return nil
	}

	var stdout, stderr strings.Builder
	if err := runShortcutsSetup(context.Background(), cfg, &stdout, &stderr); err != nil {
		t.Fatalf("runShortcutsSetup: %v", err)
	}
	if !strings.Contains(stderr.String(), "unknown variant") {
		t.Errorf("stderr = %q, want a warning about the unknown variant", stderr.String())
	}
	f.mu.Lock()
	opened := len(f.opened)
	f.mu.Unlock()
	if opened != 1 {
		t.Errorf("open was called %d times, want 1 (only the valid variant)", opened)
	}
}

func TestRunShortcutsSetupMissingCLI(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	// A PATH with nothing useful on it: LookPath("shortcuts") fails, and
	// runShortcutsSetup should report that and return cleanly rather than
	// trying (and failing) to shell out.
	t.Setenv("PATH", t.TempDir())

	var stdout, stderr strings.Builder
	if err := runShortcutsSetup(context.Background(), &config.Config{}, &stdout, &stderr); err != nil {
		t.Fatalf("runShortcutsSetup: %v", err)
	}
	if !strings.Contains(stdout.String(), "not found") {
		t.Errorf("stdout = %q, want a not-found message", stdout.String())
	}
}

// --- Live Apple PCC verification (opt-in; real network calls) -----------

// TestLiveAskAI drives the real, already-installed "Ask AI" shortcut
// through shortcuts:apple-pro via a [shortcuts].names config override,
// end to end: Detect, several timed Generate calls, and a backend
// verification via /usr/bin/log show. It makes real Apple Private Cloud
// Compute calls against an unpublished daily quota, so it's gated behind
// SHELP_LIVE_SHORTCUTS=1 and is never run as part of `go test ./...`.
// Results from running it once are recorded in MERGE NOTES.
func TestLiveAskAI(t *testing.T) {
	if os.Getenv("SHELP_LIVE_SHORTCUTS") != "1" {
		t.Skip("set SHELP_LIVE_SHORTCUTS=1 to run (makes real Apple PCC calls against a daily quota)")
	}

	cfg, err := config.Parse(`[shortcuts]
names = { "apple-pro" = "Ask AI" }
`)
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}

	p, err := Get(cfg, "shortcuts:apple-pro")
	if err != nil {
		t.Fatalf("Get(shortcuts:apple-pro): %v", err)
	}

	st := p.Detect(context.Background())
	t.Logf("Detect: available=%v reason=%q version=%q", st.Available, st.Reason, st.Version)
	if !st.Available {
		t.Fatalf("expected Ask AI to be detected as installed: %+v", st)
	}

	since := time.Now().Add(-2 * time.Second)
	for i := 0; i < 5; i++ {
		start := time.Now()
		text, err := p.Generate(context.Background(), Request{
			Prompt: fmt.Sprintf("Reply with only the single word: pong%d", i),
		})
		lat := time.Since(start)
		if err != nil {
			t.Fatalf("Generate call %d: %v", i, err)
		}
		t.Logf("call %d: %q in %s", i, text, lat)
	}

	time.Sleep(2 * time.Second) // give unified logging a moment to flush
	raw, models, err := VerifyBackend(context.Background(), since)
	if err != nil {
		t.Logf("VerifyBackend error (non-fatal, reporting only): %v", err)
	}
	t.Logf("VerifyBackend models: %v", models)
	t.Logf("VerifyBackend raw output length: %d bytes", len(raw))
	if len(raw) > 0 {
		max := len(raw)
		if max > 4000 {
			max = 4000
		}
		t.Logf("VerifyBackend raw output (first %d bytes):\n%s", max, raw[:max])
	}
}

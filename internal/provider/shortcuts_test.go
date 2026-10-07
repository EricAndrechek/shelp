package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// --- Outdated-version detection (action count; shortcutActionCountCmd faked) ---

func TestAppleScriptQuote(t *testing.T) {
	cases := map[string]string{
		`Ask AI`:              `Ask AI`,
		`shelp · "Pro" + Web`: `shelp · \"Pro\" + Web`,
		`back\slash`:          `back\\slash`,
	}
	for in, want := range cases {
		if got := appleScriptQuote(in); got != want {
			t.Errorf("appleScriptQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func installFakeActionCount(t *testing.T, fn func(ctx context.Context, name string) (int, error)) {
	t.Helper()
	orig := shortcutActionCountCmd
	t.Cleanup(func() { shortcutActionCountCmd = orig })
	shortcutActionCountCmd = fn
}

func TestOutdatedShortcutNoteCurrent(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return currentShortcutActionCount(), nil
	})
	if got := outdatedShortcutNote(context.Background(), "Ask AI"); got != "" {
		t.Errorf("outdatedShortcutNote() = %q, want empty for a current shortcut", got)
	}
}

func TestOutdatedShortcutNoteV1(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return shortcutV1ActionCount, nil
	})
	got := outdatedShortcutNote(context.Background(), "Ask AI")
	for _, want := range []string{"outdated", "format v1"} {
		if !strings.Contains(got, want) {
			t.Errorf("outdatedShortcutNote() = %q, want it to contain %q", got, want)
		}
	}
}

func installFakeMarkerVersion(t *testing.T, fn func(ctx context.Context, name string) (int, error)) *int {
	t.Helper()
	orig := shortcutMarkerVersionCmd
	t.Cleanup(func() { shortcutMarkerVersionCmd = orig })
	calls := new(int)
	shortcutMarkerVersionCmd = func(ctx context.Context, name string) (int, error) {
		*calls++
		return fn(ctx, name)
	}
	return calls
}

// A format change that keeps the same action count is still detected:
// the marker, not the count, says which version is installed.
func TestOutdatedShortcutNoteSameCountOlderVersion(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return currentShortcutActionCount(), nil
	})
	installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		return ShortcutFormatVersion - 1, nil
	})
	got := outdatedShortcutNote(context.Background(), "shelp · Apple Intelligence Pro")
	for _, want := range []string{"outdated", fmt.Sprintf("format v%d", ShortcutFormatVersion-1)} {
		if !strings.Contains(got, want) {
			t.Errorf("outdatedShortcutNote() = %q, want it to contain %q", got, want)
		}
	}
}

// An unexpected action count is settled by asking the marker, not by
// the count alone.
func TestOutdatedShortcutNoteUnexpectedCountAsksMarker(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return 99, nil // neither v1 nor the current format's count
	})
	calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		return ShortcutFormatVersion, nil
	})
	if got := outdatedShortcutNote(context.Background(), "Ask AI"); got != "" {
		t.Errorf("outdatedShortcutNote() = %q, want empty: the marker says it's current", got)
	}
	if *calls != 1 {
		t.Errorf("marker probe called %d times, want 1", *calls)
	}
}

// The marker must never reach a v1 shortcut (2 actions: its only real
// action is Use Model) or anything too small to hold the marker branch.
func TestInstalledShortcutVersionNeverProbesSmallShortcuts(t *testing.T) {
	for _, count := range []int{shortcutV1ActionCount, 3, 5} {
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) { return count, nil })
		calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
			return ShortcutFormatVersion, nil
		})
		v, err := installedShortcutVersion(context.Background(), "x")
		if *calls != 0 {
			t.Errorf("count %d: marker probe called %d times, want 0", count, *calls)
		}
		if count == shortcutV1ActionCount {
			if err != nil || v != 1 {
				t.Errorf("count %d: got v%d, %v; want v1", count, v, err)
			}
		} else if err == nil {
			t.Errorf("count %d: got v%d, want an error (not a shelp format)", count, v)
		}
	}
}

// A shortcut imported from a file reads action count 0 (Shortcuts never
// caches one for it), so only the marker can say which version it is.
func TestInstalledShortcutVersionZeroCountAsksMarker(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) { return 0, nil })
	calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		return ShortcutFormatVersion - 1, nil
	})
	v, err := installedShortcutVersion(context.Background(), "shelp · Apple Intelligence Pro")
	if err != nil || v != ShortcutFormatVersion-1 {
		t.Errorf("got v%d, %v; want v%d from the marker", v, err, ShortcutFormatVersion-1)
	}
	if *calls != 1 {
		t.Errorf("marker probe called %d times, want 1", *calls)
	}
}

func TestOutdatedShortcutNoteMarkerErrorIsNeverOutdated(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return currentShortcutActionCount(), nil
	})
	installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		return 0, errors.New("shortcuts run exited 1")
	})
	if got := outdatedShortcutNote(context.Background(), "x"); got != "" {
		t.Errorf("outdatedShortcutNote() = %q, want empty when the marker probe fails", got)
	}
}

func TestParseShortcutMarkerReply(t *testing.T) {
	for in, want := range map[string]int{"shelp-shortcut 2": 2, "shelp-shortcut 3\n": 3, "  shelp-shortcut 10 ": 10} {
		if got, err := parseShortcutMarkerReply(in); err != nil || got != want {
			t.Errorf("parseShortcutMarkerReply(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ls -la", "shelp-shortcut", "shelp-shortcut x", "shelp-shortcut 0"} {
		if _, err := parseShortcutMarkerReply(in); err == nil {
			t.Errorf("parseShortcutMarkerReply(%q): want an error", in)
		}
	}
}

func TestOutdatedShortcutNoteErrorIsNeverOutdated(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return 0, errors.New("osascript exited 1: Can't get shortcut")
	})
	if got := outdatedShortcutNote(context.Background(), "Ask AI"); got != "" {
		t.Errorf("outdatedShortcutNote() = %q, want empty when the check itself fails (never treated as outdated)", got)
	}
}

// TestShortcutsProviderDetectReportsOutdated exercises Detect end to end
// (installedShortcuts + outdatedShortcutNote together): an installed but
// outdated shortcut must still report Available true (it works) with a
// Reason mentioning the update. installedShortcuts caches for the life of
// the process (see its doc comment), so this is deliberately the only test
// in this package that calls Detect -- a second one would just read back
// this one's cached `shortcuts list` fake.
func TestShortcutsProviderDetectReportsOutdated(t *testing.T) {
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	shortcutsListCmd = func(ctx context.Context) (string, error) {
		return "Ask AI", nil
	}
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return shortcutV1ActionCount, nil
	})

	p := &shortcutsProvider{
		variant: shortcutVariant{id: "shortcuts:apple-pro", model: "Apple Intelligence Pro"},
		name:    "Ask AI",
	}
	st := p.Detect(context.Background())
	if !st.Available {
		t.Fatalf("Detect() Available = false, want true (an outdated shortcut still works): %+v", st)
	}
	if !strings.Contains(st.Reason, "outdated") || !strings.Contains(st.Fix, "shelp setup shortcuts") {
		t.Errorf("Detect() Reason = %q, want it to mention the shortcut is outdated and how to update", st.Reason)
	}
}

// --- Rate-limit classification (pure function, no `shortcuts` calls) ---

func TestShortcutsRateLimitReason(t *testing.T) {
	const realFailureText = "Error: You have reached the usage limit for this model. Please try again later."
	cases := []struct {
		name           string
		exitCode       int
		stdout, stderr string
		wantOK         bool
		wantReason     string
	}{
		{
			"real failure text, exit 0 (observed verbatim on Apple Intelligence Pro: the shortcut itself succeeds, its output IS the error)",
			0, realFailureText, "",
			true, realFailureText,
		},
		{
			"real failure text, exit 1 (rule a: non-zero exit + known phrase)",
			1, realFailureText, "",
			true, realFailureText,
		},
		{
			"successful exit-0 answer that happens to contain \"try again later\" must NOT be classified as rate-limited",
			0, "The command failed because of a network blip; the script will try again later on its own.", "",
			false, "",
		},
		{"case insensitive, exit 1", 1, "USAGE LIMIT reached", "", true, "USAGE LIMIT reached"},
		{"\"try again later\" alone, exit 1", 1, "please try again later", "", true, "please try again later"},
		{"in stderr instead of stdout, exit 1", 1, "", "usage limit exceeded", true, "usage limit exceeded"},
		{
			"exit 0, starts with \"Error:\" but no usage-limit phrase -- must NOT match rule b on \"Error:\" alone",
			0, "Error: something else entirely went wrong", "",
			false, "",
		},
		{
			"exit 0, contains \"usage limit\" but does not start with \"Error:\" -- must NOT match rule b without the prefix",
			0, "You have reached the usage limit today, see you tomorrow.", "",
			false, "",
		},
		{"not a rate limit, exit 0", 0, "find ~ -type f -size +100M", "", false, ""},
		{"empty, exit 0", 0, "", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ok := shortcutsRateLimitReason(c.exitCode, c.stdout, c.stderr)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && reason != c.wantReason {
				t.Errorf("reason = %q, want %q", reason, c.wantReason)
			}
		})
	}
}

// TestShortcutsProviderGenerateRateLimitedEvenOnExitZero exercises the
// whole Generate rate-limit path (not just the pure helper above) via
// RunCmd's real `sh -c`-less exec of a fake "shortcuts" binary: a
// rate-limited "Use Model" run typically still exits 0 with the error text
// AS its output, so this must be classified as *RateLimitedError before
// any exit-code handling ever runs.
func TestShortcutsProviderGenerateRateLimitedEvenOnExitZero(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "shortcuts")
	script := "#!/bin/sh\nprintf '%s' 'Error: You have reached the usage limit for this model. Please try again later.'\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := &shortcutsProvider{
		variant: shortcutVariant{id: "shortcuts:apple-pro", model: "Apple Intelligence Pro"},
		name:    "shelp · Apple Intelligence Pro",
	}
	_, err := p.Generate(context.Background(), Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
	var rle *RateLimitedError
	if !errors.As(err, &rle) {
		t.Fatalf("error = %v (%T), want *RateLimitedError", err, err)
	}
	if !strings.Contains(rle.Reason, "usage limit") {
		t.Errorf("Reason = %q, want it to mention usage limit", rle.Reason)
	}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want also *UnavailableError via errors.As", err)
	}
}

// TestShortcutsProviderGenerateRateLimitedOnNonZeroExit covers rule (a):
// the real failure text on a non-zero exit (e.g. a different Shortcuts
// failure mode than the exit-0 one above) must still be classified as
// *RateLimitedError.
func TestShortcutsProviderGenerateRateLimitedOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "shortcuts")
	script := "#!/bin/sh\nprintf '%s' 'Error: You have reached the usage limit for this model. Please try again later.'\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := &shortcutsProvider{
		variant: shortcutVariant{id: "shortcuts:apple-pro", model: "Apple Intelligence Pro"},
		name:    "shelp · Apple Intelligence Pro",
	}
	_, err := p.Generate(context.Background(), Request{Prompt: "hi"})
	var rle *RateLimitedError
	if !errors.As(err, &rle) {
		t.Fatalf("error = %v (%T), want *RateLimitedError", err, err)
	}
}

// TestShortcutsProviderGenerateNotRateLimitedForLegitimateAnswer is the
// regression test for the false-positive this rule (b) narrowing fixes: a
// SUCCESSFUL (exit 0) suggest/explain answer that happens to contain the
// phrase "try again later" (e.g. explaining a retry loop) must be returned
// as a normal answer, not misclassified as a rate limit.
func TestShortcutsProviderGenerateNotRateLimitedForLegitimateAnswer(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "shortcuts")
	answer := "This retries the request on failure and will try again later if the server is busy."
	script := "#!/bin/sh\nprintf '%s' '" + answer + "'\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := &shortcutsProvider{
		variant: shortcutVariant{id: "shortcuts:apple-pro", model: "Apple Intelligence Pro"},
		name:    "shelp · Apple Intelligence Pro",
	}
	got, err := p.Generate(context.Background(), Request{Prompt: "explain the retry logic"})
	if err != nil {
		t.Fatalf("Generate: unexpected error %v (%T), want the answer returned normally", err, err)
	}
	if got != answer {
		t.Errorf("Generate() = %q, want %q", got, answer)
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

func TestRunShortcutsSetupSkipsCurrent(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{"Ask AI": true}}
	installFakes(t, f)
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return currentShortcutActionCount(), nil // already up to date
	})

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
		t.Errorf("open was called %d times, want 0 (already current)", len(f.opened))
	}
	if !strings.Contains(stdout.String(), "up to date") {
		t.Errorf("stdout = %q, want a mention of being up to date", stdout.String())
	}
}

func TestRunShortcutsSetupRegeneratesOutdated(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{"Ask AI": true}}
	installFakes(t, f)
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return shortcutV1ActionCount, nil // outdated: still the marker-less v1 format
	})

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
	opened := len(f.opened)
	f.mu.Unlock()
	if opened != 1 {
		t.Errorf("open was called %d times, want 1 (regenerate + reopen an outdated shortcut)", opened)
	}
	if !strings.Contains(stdout.String(), "outdated") {
		t.Errorf("stdout = %q, want a mention of being outdated", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Replace") {
		t.Errorf("stdout = %q, want it to tell the user to click Replace (not Add Shortcut)", stdout.String())
	}
	// shortcutActionCountCmd is still faked to return shortcutV1ActionCount,
	// so waitForShortcutUpdate never sees the new count and should time out
	// -- this test only cares that regeneration+reopen happened, not that
	// the (unverified, see MERGE NOTES) Replace click was simulated too.
	if !strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stderr = %q, want a timeout message (the fake action count never changes)", stderr.String())
	}
}

func TestRunShortcutsSetupAssumesCurrentOnActionCountError(t *testing.T) {
	f := &setupFakes{installed: map[string]bool{"Ask AI": true}}
	installFakes(t, f)
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return 0, errors.New("osascript: Shortcuts Events not available")
	})

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
	opened := len(f.opened)
	f.mu.Unlock()
	if opened != 0 {
		t.Errorf("open was called %d times, want 0 (an action-count error must never trigger a reopen)", opened)
	}
	if !strings.Contains(stdout.String(), "leaving it alone") {
		t.Errorf("stdout = %q, want a mention of leaving it alone on a check failure", stdout.String())
	}
}

// TestShortcutSetupKeysIncludesAlreadyInstalledVariant covers "Also
// include in the install list any shelp-named variant that is already
// installed even if not listed in config, so upgrades cover everything
// the user has": apple-pro (as "Ask AI") is installed but not in
// [shortcuts].install, which only names chatgpt.
func TestShortcutSetupKeysIncludesAlreadyInstalledVariant(t *testing.T) {
	sc := shortcutsConfig{Names: map[string]string{"apple-pro": "Ask AI"}}
	wanted := []string{"chatgpt"}
	installed := map[string]bool{"Ask AI": true}

	got := shortcutSetupKeys(wanted, sc, installed)

	wantContains := map[string]bool{"chatgpt": false, "apple-pro": false}
	for _, k := range got {
		if _, ok := wantContains[k]; ok {
			wantContains[k] = true
		}
	}
	for k, found := range wantContains {
		if !found {
			t.Errorf("shortcutSetupKeys(%v) = %v, missing %q", wanted, got, k)
		}
	}
	if got[0] != "chatgpt" {
		t.Errorf("shortcutSetupKeys(%v) = %v, want configured keys first", wanted, got)
	}
}

func TestShortcutSetupKeysNoExtras(t *testing.T) {
	sc := shortcutsConfig{}
	wanted := []string{"apple-pro"}
	installed := map[string]bool{} // nothing else installed
	got := shortcutSetupKeys(wanted, sc, installed)
	if len(got) != 1 || got[0] != "apple-pro" {
		t.Errorf("shortcutSetupKeys(%v) = %v, want just %v unchanged", wanted, got, wanted)
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

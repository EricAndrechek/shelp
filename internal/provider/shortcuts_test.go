package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/style"
)

// resetShortcutsCache empties the process-wide `shortcuts list`/version
// cache now and after the test, so tests that call Detect see their own
// fakes.
func resetShortcutsCache(t *testing.T) {
	t.Helper()
	reset := func() {
		c := &shortcutsCache
		c.mu.Lock()
		defer c.mu.Unlock()
		c.listDone, c.list, c.listErr, c.versions = false, nil, nil, nil
	}
	reset()
	t.Cleanup(reset)
}

func TestShortcutName(t *testing.T) {
	for in, want := range map[string]string{"": "shelp", "   ": "shelp", "Ask AI": "Ask AI", " my shelp ": "my shelp"} {
		if got := (shortcutsConfig{Name: in}).shortcutName(); got != want {
			t.Errorf("shortcutName(%q) = %q, want %q", in, got, want)
		}
	}
	cfg, err := config.Parse("[shortcuts]\nname = \"Ask AI\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveShortcutName(cfg); got != "Ask AI" {
		t.Errorf("resolveShortcutName = %q, want %q", got, "Ask AI")
	}
	if got := resolveShortcutName(&config.Config{}); got != defaultShortcutName {
		t.Errorf("resolveShortcutName(no table) = %q, want %q", got, defaultShortcutName)
	}
}

func TestVariantByKey(t *testing.T) {
	if _, ok := variantByKey("apple-pro"); !ok {
		t.Error("expected apple-pro to be found")
	}
	if _, ok := variantByKey("nonexistent"); ok {
		t.Error("expected nonexistent key to be not-found")
	}
	// Provider IDs (with the "shortcuts:" prefix) are not variant keys.
	if _, ok := variantByKey("shortcuts:apple-pro"); ok {
		t.Error("variantByKey should not match full provider IDs")
	}
}

func TestLegacyShortcutName(t *testing.T) {
	want := []string{
		"shelp · Apple Intelligence", "shelp · Apple Intelligence + Web",
		"shelp · Apple Intelligence Pro", "shelp · Apple Intelligence Pro + Web",
		"shelp · ChatGPT", "shelp · ChatGPT + Web",
	}
	for i, v := range shortcutVariants {
		if got := legacyShortcutName(v); got != want[i] {
			t.Errorf("legacyShortcutName(%s) = %q, want %q", v.id, got, want[i])
		}
	}
}

func TestLegacyShortcuts(t *testing.T) {
	installed := map[string]bool{
		"shelp":                              true, // the current one
		"shelp 1":                            true, // a copy of the current one: not legacy
		"shelp · Apple Intelligence Pro":     true,
		"shelp · Apple Intelligence Pro 1":   true,
		"shelp · Apple Intelligence + Web 3": true,
		"shelp · ChatGPT":                    true,
		"shelp · ChatGPT 1a":                 true, // not a copy
		"shelp · Something Else":             true,
		"Ask AI":                             true,
	}
	got := legacyShortcuts(installed, "shelp")
	want := []string{"shelp · Apple Intelligence + Web 3", "shelp · Apple Intelligence Pro", "shelp · Apple Intelligence Pro 1", "shelp · ChatGPT"}
	if !slices.Equal(got, want) {
		t.Errorf("legacyShortcuts = %q, want %q", got, want)
	}
	// A name configured as the current one is never legacy.
	got = legacyShortcuts(installed, "shelp · ChatGPT")
	if slices.Contains(got, "shelp · ChatGPT") {
		t.Errorf("legacyShortcuts listed the configured name: %q", got)
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

func TestCountShortcutName(t *testing.T) {
	raw := "shelp\nshelp 1\nAsk AI\nshelp\n  shelp  \n"
	if got := countShortcutName(raw, "shelp"); got != 3 {
		t.Errorf("countShortcutName = %d, want 3", got)
	}
	if got := countShortcutName(raw, "nope"); got != 0 {
		t.Errorf("countShortcutName(nope) = %d, want 0", got)
	}
}

func TestConflictCopies(t *testing.T) {
	list := map[string]bool{"shelp": true, "shelp 1": true, "shelp 12": true, "shelp + Web": true, "shelp 1a": true, "Other 1": true}
	got := conflictCopies(list, "shelp")
	want := []string{"shelp 1", "shelp 12"}
	if !slices.Equal(got, want) {
		t.Errorf("conflictCopies = %q, want %q", got, want)
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
	if !slices.Equal(got, want) {
		t.Fatalf("extractModelIDs = %v, want %v", got, want)
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

// --- Version detection (shortcutActionCountCmd/shortcutMarkerVersionCmd faked) ---

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

func installFakeMarkerVersion(t *testing.T, fn func(ctx context.Context, name string) (int, error)) *int {
	t.Helper()
	orig := shortcutMarkerVersionCmd
	t.Cleanup(func() { shortcutMarkerVersionCmd = orig })
	var mu sync.Mutex
	calls := new(int)
	shortcutMarkerVersionCmd = func(ctx context.Context, name string) (int, error) {
		mu.Lock()
		*calls++
		mu.Unlock()
		return fn(ctx, name)
	}
	return calls
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
// caches one for it), and a saved one its real count: either way only the
// marker can say which version it is.
func TestInstalledShortcutVersionAsksMarker(t *testing.T) {
	for _, count := range []int{0, 7, currentShortcutActionCount(), 99} {
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) { return count, nil })
		calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
			return ShortcutFormatVersion - 1, nil
		})
		v, err := installedShortcutVersion(context.Background(), "shelp")
		if err != nil || v != ShortcutFormatVersion-1 {
			t.Errorf("count %d: got v%d, %v; want v%d from the marker", count, v, err, ShortcutFormatVersion-1)
		}
		if *calls != 1 {
			t.Errorf("count %d: marker probe called %d times, want 1", count, *calls)
		}
	}
}

func TestInstalledShortcutVersionErrors(t *testing.T) {
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return 0, errors.New("osascript exited 1: Can't get shortcut")
	})
	calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) { return 5, nil })
	if _, err := installedShortcutVersion(context.Background(), "shelp"); err == nil || *calls != 0 {
		t.Errorf("got err %v after %d marker runs; want the count's error and no run", err, *calls)
	}
}

func TestParseShortcutMarkerReply(t *testing.T) {
	for in, want := range map[string]int{"shelp-shortcut 2": 2, "shelp-shortcut 5\n": 5, "  shelp-shortcut 10 ": 10} {
		if got, err := parseShortcutMarkerReply(in); err != nil || got != want {
			t.Errorf("parseShortcutMarkerReply(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ls -la", "shelp-shortcut", "shelp-shortcut x", "shelp-shortcut 0", shortcutUnknownModelText + "x"} {
		if _, err := parseShortcutMarkerReply(in); err == nil {
			t.Errorf("parseShortcutMarkerReply(%q): want an error", in)
		}
	}
}

// --- Detect -------------------------------------------------------------

// detectWith runs p.Detect with `shortcuts list` faked to names and the
// marker answering versions (missing: the run fails), action count 0.
func detectWith(t *testing.T, p *shortcutsProvider, names []string, versions map[string]int) (Status, *int) {
	t.Helper()
	resetShortcutsCache(t)
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	shortcutsListCmd = func(ctx context.Context) (string, error) { return strings.Join(names, "\n"), nil }
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) { return 0, nil })
	calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		if v, ok := versions[name]; ok {
			return v, nil
		}
		return 0, errors.New("shortcuts run exited 1")
	})
	return p.Detect(context.Background()), calls
}

func variantProvider(t *testing.T, key, name string) *shortcutsProvider {
	t.Helper()
	v, ok := variantByKey(key)
	if !ok {
		t.Fatalf("no variant %q", key)
	}
	return &shortcutsProvider{variant: v, name: name}
}

func TestShortcutsProviderDetect(t *testing.T) {
	legacy := []string{"shelp · Apple Intelligence Pro", "shelp · ChatGPT"}
	cases := []struct {
		name      string
		key       string
		names     []string
		versions  map[string]int
		available bool
		reason    string
		fix       string
	}{
		{"current", "apple-pro", append([]string{"shelp"}, legacy...), map[string]int{"shelp": ShortcutFormatVersion}, true, "", ""},
		{"newer", "apple", []string{"shelp"}, map[string]int{"shelp": ShortcutFormatVersion + 1}, true, "", ""},
		{"not installed, only legacy ones", "apple-pro", legacy, nil, false, "not installed", "shelp setup shortcuts"},
		{"chatgpt not installed", "chatgpt-web", nil, nil, false, "ChatGPT extension", "shelp setup shortcuts"},
		{"outdated", "apple-pro", []string{"shelp"}, map[string]int{"shelp": 4}, false, "format v4, current v" + strconv.Itoa(ShortcutFormatVersion) + "", "to update it"},
		{"marker fails", "apple-pro", []string{"shelp"}, nil, false, "version check", "shelp setup shortcuts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, _ := detectWith(t, variantProvider(t, c.key, "shelp"), c.names, c.versions)
			if st.Available != c.available || !strings.Contains(st.Reason, c.reason) || !strings.Contains(st.Fix, c.fix) {
				t.Errorf("Detect() = %+v; want available=%v, reason containing %q, fix containing %q", st, c.available, c.reason, c.fix)
			}
		})
	}
}

// Six providers share one shortcut: concurrent Detects (as `shelp
// providers` runs them) run its marker once, and never touch the legacy
// shortcuts.
func TestShortcutsProviderDetectProbesOnce(t *testing.T) {
	resetShortcutsCache(t)
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	shortcutsListCmd = func(ctx context.Context) (string, error) {
		return "shelp\nshelp · Apple Intelligence Pro\nshelp · ChatGPT + Web 1", nil
	}
	var mu sync.Mutex
	var asked []string
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		mu.Lock()
		asked = append(asked, name)
		mu.Unlock()
		return 0, nil
	})
	calls := installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) { return ShortcutFormatVersion, nil })

	var wg sync.WaitGroup
	for _, v := range shortcutVariants {
		wg.Add(1)
		go func(v shortcutVariant) {
			defer wg.Done()
			if st := (&shortcutsProvider{variant: v, name: "shelp"}).Detect(context.Background()); !st.Available {
				t.Errorf("%s: %+v", v.id, st)
			}
		}(v)
	}
	wg.Wait()
	if *calls != 1 || !slices.Equal(asked, []string{"shelp"}) {
		t.Errorf("marker ran %d times, action count asked of %q; want once, of \"shelp\" only", *calls, asked)
	}
}

func TestShortcutsProvidersShareOneName(t *testing.T) {
	if _, ok := LookPath("shortcuts"); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS with the shortcuts CLI")
	}
	cfg, err := config.Parse("[shortcuts]\nname = \"My shelp\"\n")
	if err != nil {
		t.Fatal(err)
	}
	ps := shortcutsFamily{}.Providers(cfg)
	if len(ps) != len(shortcutVariants) {
		t.Fatalf("got %d providers, want %d", len(ps), len(shortcutVariants))
	}
	for _, p := range ps {
		sp := p.(*shortcutsProvider)
		if sp.name != "My shelp" || !strings.Contains(p.Description(), `"My shelp"`) {
			t.Errorf("%s: name %q, description %q", p.ID(), sp.name, p.Description())
		}
		if chat := strings.HasPrefix(p.ID(), "shortcuts:chatgpt"); chat != strings.Contains(LabelOf(p), "ChatGPT extension") {
			t.Errorf("%s: label %q", p.ID(), LabelOf(p))
		}
	}
}

func TestLegacyShortcutsNotes(t *testing.T) {
	if _, ok := LookPath("shortcuts"); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS with the shortcuts CLI")
	}
	resetShortcutsCache(t)
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	list := "shelp\nshelp · Apple Intelligence Pro\nshelp · Apple Intelligence Pro 1"
	shortcutsListCmd = func(ctx context.Context) (string, error) { return list, nil }
	var probed []string
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		probed = append(probed, name)
		return 0, nil
	})
	installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		return ShortcutFormatVersion, nil
	})
	notes := legacyShortcutsNotes(context.Background(), &config.Config{})
	want := fmt.Sprintf(`Apple Shortcut "shelp": format v%d (current)`, ShortcutFormatVersion)
	if len(notes) != 2 || notes[0] != want || !strings.Contains(notes[1], `"shelp · Apple Intelligence Pro", "shelp · Apple Intelligence Pro 1"`) {
		t.Errorf("notes = %q", notes)
	}
	for _, p := range probed {
		if p != "shelp" {
			t.Errorf("the notes probed %q; legacy shortcuts are only ever listed by name", p)
		}
	}

	resetShortcutsCache(t)
	list = "Ask AI"
	if notes := legacyShortcutsNotes(context.Background(), &config.Config{}); notes != nil {
		t.Errorf("notes with no shelp shortcuts = %q, want none", notes)
	}
}

// --- Generate (a fake `shortcuts` on PATH) ----------------------------

// fakeShortcutsBin puts an executable "shortcuts" running script first on
// PATH.
func fakeShortcutsBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shortcuts"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// Generate runs the one shortcut with a JSON body naming its own variant,
// system and prompt folded into one prompt, nothing escaped for HTML.
func TestShortcutsProviderGenerateSendsJSON(t *testing.T) {
	dir := fakeShortcutsBin(t, `printf '%s\n' "$@" > "$(dirname "$0")/args"
cat > "$(dirname "$0")/stdin"
printf 'find ~ -size +100M'
`)
	for _, v := range shortcutVariants {
		p := &shortcutsProvider{variant: v, name: "shelp"}
		got, err := p.Generate(context.Background(), Request{System: "You are <shelp> & co.", Prompt: "big \"files\"\nplease 🙂"})
		if err != nil || got != "find ~ -size +100M" {
			t.Fatalf("%s: Generate = %q, %v", v.id, got, err)
		}
		args, _ := os.ReadFile(filepath.Join(dir, "args"))
		if want := "run\nshelp\n-o\n-\n--output-type\npublic.plain-text\n"; string(args) != want {
			t.Errorf("%s: args %q, want %q", v.id, args, want)
		}
		body, _ := os.ReadFile(filepath.Join(dir, "stdin"))
		want := `{"model":"` + v.key + `","prompt":"You are <shelp> & co.\n\nbig \"files\"\nplease 🙂"}`
		if string(body) != want {
			t.Errorf("%s: stdin\n%s\nwant\n%s", v.id, body, want)
		}
		var req shortcutRequest
		if err := json.Unmarshal(body, &req); err != nil || req.Model != v.key || req.Prompt != "You are <shelp> & co.\n\nbig \"files\"\nplease 🙂" {
			t.Errorf("%s: body decodes to %+v, %v", v.id, req, err)
		}
	}
}

func TestShortcutsProviderGenerateNoSystemPrompt(t *testing.T) {
	dir := fakeShortcutsBin(t, `cat > "$(dirname "$0")/stdin"; printf ok`)
	if _, err := variantProvider(t, "apple", "shelp").Generate(context.Background(), Request{Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if want := `{"model":"apple","prompt":"hi"}`; string(body) != want {
		t.Errorf("stdin %s, want %s", body, want)
	}
}

// The shortcut's fallback answer (no If matched the model) is an
// Unavailable pointing at setup, not a command.
func TestShortcutsProviderGenerateUnknownModelReply(t *testing.T) {
	fakeShortcutsBin(t, "cat >/dev/null; printf '%s' '"+shortcutUnknownModelText+"chatgpt-web'\n")
	_, err := variantProvider(t, "chatgpt-web", "shelp").Generate(context.Background(), Request{Prompt: "hi"})
	var ue *UnavailableError
	if !errors.As(err, &ue) || !strings.Contains(ue.Reason, "unknown model chatgpt-web") || !strings.Contains(ue.Reason, "shelp setup shortcuts") {
		t.Fatalf("error = %v (%T), want an Unavailable naming the unknown model and setup", err, err)
	}
	var rle *RateLimitedError
	if errors.As(err, &rle) {
		t.Error("an unknown-model reply is not a rate limit")
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
		{"the shortcut's own unknown-model answer is not a rate limit", 0, shortcutUnknownModelText + "x", "", false, ""},
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

// A rate-limited "Use Model" run typically still exits 0 with the error
// text AS its output, so Generate must classify it as *RateLimitedError
// before any exit-code handling runs -- and on a non-zero exit too.
func TestShortcutsProviderGenerateRateLimited(t *testing.T) {
	for _, exit := range []int{0, 1} {
		fakeShortcutsBin(t, fmt.Sprintf("cat >/dev/null\nprintf '%%s' 'Error: You have reached the usage limit for this model. Please try again later.'\nexit %d\n", exit))
		_, err := variantProvider(t, "apple-pro", "shelp").Generate(context.Background(), Request{Prompt: "hi"})
		var rle *RateLimitedError
		if !errors.As(err, &rle) || !strings.Contains(rle.Reason, "usage limit") {
			t.Fatalf("exit %d: error = %v (%T), want *RateLimitedError", exit, err, err)
		}
		var ue *UnavailableError
		if !errors.As(err, &ue) {
			t.Fatalf("exit %d: error = %v, want also *UnavailableError via errors.As", exit, err)
		}
	}
}

// A successful answer that happens to contain "try again later" is an
// answer, not a rate limit.
func TestShortcutsProviderGenerateNotRateLimitedForLegitimateAnswer(t *testing.T) {
	answer := "This retries the request on failure and will try again later if the server is busy."
	fakeShortcutsBin(t, "cat >/dev/null\nprintf '%s' '"+answer+"'\nexit 0\n")
	got, err := variantProvider(t, "apple-pro", "shelp").Generate(context.Background(), Request{Prompt: "explain the retry logic"})
	if err != nil || got != answer {
		t.Fatalf("Generate = %q, %v; want the answer returned normally", got, err)
	}
}

// --- Provider.Generate error paths (real `shortcuts` calls, no PCC/network) ---

func TestShortcutsProviderGenerateNotInstalled(t *testing.T) {
	if _, ok := LookPath("shortcuts"); !ok {
		t.Skip("shortcuts CLI not on PATH")
	}
	p := variantProvider(t, "apple", "shelp-definitely-not-a-real-shortcut-name-xyz")
	_, err := p.Generate(context.Background(), Request{Prompt: "hi"})
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v (%T), want *UnavailableError", err, err)
	}
	if !strings.Contains(ue.Reason, "not installed") || !strings.Contains(ue.Reason, "shelp setup shortcuts") {
		t.Errorf("UnavailableError.Reason = %q, want it to say not installed and how to fix it", ue.Reason)
	}
}

// --- Setup and the waits, against a scripted Shortcuts library ----------

// shortcutState is one moment of the user's library: the lines `shortcuts
// list` prints (a name twice while an import dialog asks "Replace" or
// "Keep Both") and what each name's marker answers (missing: the run
// fails, like a broken format's). holdUntilOpen keeps the library in this
// state until the generated file has been opened.
type shortcutState struct {
	names         []string
	versions      map[string]int
	holdUntilOpen bool
}

// scriptedLibrary fakes `shortcuts list`, `open`, generating the shortcut
// (an unsigned dummy file), and the version probes from states: each list call moves to the next state (the last one
// repeats). Action counts read 0, as for every file-imported shortcut.
type scriptedLibrary struct {
	mu       sync.Mutex
	states   []shortcutState
	i        int
	opened   []string // paths
	openedAt []int    // state index when each was opened
	probes   []string // "<name>@<state index>", in order
}

func (s *scriptedLibrary) cur() shortcutState { return s.states[max(s.i, 0)] }

func scriptLibrary(t *testing.T, states ...shortcutState) *scriptedLibrary {
	t.Helper()
	s := &scriptedLibrary{states: states, i: -1}
	origList, origOpen := shortcutsListCmd, openCmd
	origInterval, origTimeout := shortcutsPollInterval, shortcutsPollTimeout
	t.Cleanup(func() {
		shortcutsListCmd, openCmd = origList, origOpen
		shortcutsPollInterval, shortcutsPollTimeout = origInterval, origTimeout
	})
	shortcutsPollInterval, shortcutsPollTimeout = time.Millisecond, 200*time.Millisecond
	shortcutsListCmd = func(ctx context.Context) (string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !(s.i >= 0 && s.states[s.i].holdUntilOpen && len(s.opened) == 0) {
			s.i = min(s.i+1, len(s.states)-1)
		}
		return strings.Join(s.cur().names, "\n"), nil
	}
	openCmd = func(ctx context.Context, path string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.opened = append(s.opened, path)
		s.openedAt = append(s.openedAt, s.i)
		return nil
	}
	origGen := generateShortcut
	t.Cleanup(func() { generateShortcut = origGen })
	generateShortcut = func(ctx context.Context, dir, name string) (string, error) {
		path := filepath.Join(dir, sanitizeFilename(name)+".shortcut")
		return path, os.WriteFile(path, []byte("fake"), 0o600)
	}
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) { return 0, nil })
	installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.probes = append(s.probes, fmt.Sprintf("%s@%d", name, s.i))
		if v, ok := s.cur().versions[name]; ok {
			return v, nil
		}
		return 0, errors.New("shortcuts run exited 1: Error: Please choose a value for each parameter in this action.")
	})
	return s
}

// probedNames returns the distinct names whose marker ran.
func (s *scriptedLibrary) probedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.probes {
		n := p[:strings.LastIndex(p, "@")]
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// noProbeWhileDuplicated fails if a marker ran while two shortcuts shared
// name.
func (s *scriptedLibrary) noProbeWhileDuplicated(t *testing.T, name string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.probes {
		i, _ := strconv.Atoi(p[strings.LastIndex(p, "@")+1:])
		if n := strings.Count("\n"+strings.Join(s.states[i].names, "\n")+"\n", "\n"+name+"\n"); n > 1 {
			t.Errorf("ran the marker (%s) while %d shortcuts were named %q", p, n, name)
		}
	}
}

func runSetup(t *testing.T, toml string) (stdout, stderr string) {
	t.Helper()
	if _, ok := LookPath("shortcuts"); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS with the shortcuts CLI")
	}
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb strings.Builder
	if err := runShortcutsSetup(context.Background(), cfg, &out, &errb); err != nil {
		t.Fatalf("runShortcutsSetup: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errb.String())
	}
	return out.String(), errb.String()
}

func TestRunShortcutsSetupInstallsMissing(t *testing.T) {
	lib := scriptLibrary(t,
		shortcutState{names: []string{"Ask AI"}, holdUntilOpen: true},
		shortcutState{names: []string{"Ask AI"}}, // dialog open
		shortcutState{names: []string{"Ask AI", "shelp"}, versions: map[string]int{"shelp": ShortcutFormatVersion}},
	)
	stdout, stderr := runSetup(t, "")
	if len(lib.opened) != 1 || filepath.Base(lib.opened[0]) != "shelp.shortcut" {
		t.Fatalf("opened %q, want one shelp.shortcut", lib.opened)
	}
	if !strings.Contains(stdout, `"Add Shortcut"`) || !strings.Contains(stdout, `"shelp" installed (v`+strconv.Itoa(ShortcutFormatVersion)+`)`) || !strings.Contains(stdout, "serves shortcuts:apple,") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr:\n%s", stderr)
	}
	if got := lib.probedNames(); !slices.Equal(got, []string{"shelp"}) {
		t.Errorf("probed %q, want only \"shelp\"", got)
	}
}

func TestRunShortcutsSetupSkipsCurrent(t *testing.T) {
	lib := scriptLibrary(t, shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": ShortcutFormatVersion}})
	stdout, _ := runSetup(t, "")
	if len(lib.opened) != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("opened %q; stdout:\n%s", lib.opened, stdout)
	}
}

func TestRunShortcutsSetupHonorsConfiguredName(t *testing.T) {
	lib := scriptLibrary(t, shortcutState{names: []string{"shelp", "My shelp"}, versions: map[string]int{"My shelp": ShortcutFormatVersion}})
	stdout, _ := runSetup(t, "[shortcuts]\nname = \"My shelp\"\n")
	if len(lib.opened) != 0 || !strings.Contains(stdout, `"My shelp" already up to date`) {
		t.Errorf("opened %q; stdout:\n%s", lib.opened, stdout)
	}
	if got := lib.probedNames(); !slices.Equal(got, []string{"My shelp"}) {
		t.Errorf("probed %q", got)
	}
}

// Delete first: the new file opens only once the old shortcut is gone, so
// the import is a plain "Add Shortcut" with no conflict.
func TestRunShortcutsSetupUpdateDeletesFirst(t *testing.T) {
	lib := scriptLibrary(t,
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 4}}, // setup's list + version check
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 4}}, // still there
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 4}},
		shortcutState{names: nil, holdUntilOpen: true}, // deleted
		shortcutState{names: nil}, // dialog open
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": ShortcutFormatVersion}},
	)
	stdout, stderr := runSetup(t, "")
	if len(lib.opened) != 1 {
		t.Fatalf("opened %d times, want 1\n%s", len(lib.opened), stdout)
	}
	if at := lib.states[lib.openedAt[0]]; slices.Contains(at.names, "shelp") {
		t.Errorf("opened the new file while the old shortcut was still listed (state %d)", lib.openedAt[0])
	}
	for _, want := range []string{"outdated (v4, current v" + strconv.Itoa(ShortcutFormatVersion) + ")", "right-click \"shelp\" › Delete", "waiting for \"shelp\" to be deleted", `"shelp" installed (v` + strconv.Itoa(ShortcutFormatVersion) + `)`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Replace") || stderr != "" {
		t.Errorf("a delete-first update shouldn't mention Replace or warn:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	// The old one answers its version once (to know it's outdated); while
	// waiting for the deletion nothing runs.
	lib.mu.Lock()
	defer lib.mu.Unlock()
	for _, p := range lib.probes {
		if i, _ := strconv.Atoi(p[strings.LastIndex(p, "@")+1:]); i >= 1 && i <= 3 {
			t.Errorf("ran the marker (%s) while waiting for the deletion", p)
		}
	}
}

func TestRunShortcutsSetupUpdateTimesOutWithoutDeletion(t *testing.T) {
	lib := scriptLibrary(t, shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 4}})
	_, stderr := runSetup(t, "")
	if len(lib.opened) != 0 {
		t.Errorf("opened %q although the old shortcut was never deleted", lib.opened)
	}
	if !strings.Contains(stderr, "timed out waiting for \"shelp\" to be deleted") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestRunShortcutsSetupLeavesUnreadableAlone(t *testing.T) {
	lib := scriptLibrary(t, shortcutState{names: []string{"shelp"}}) // marker fails
	stdout, _ := runSetup(t, "")
	if len(lib.opened) != 0 || !strings.Contains(stdout, "leaving it alone") {
		t.Errorf("opened %q; stdout:\n%s", lib.opened, stdout)
	}
	if n := len(lib.probes); n != 1 {
		t.Errorf("ran the marker %d times, want 1", n)
	}
}

func TestRunShortcutsSetupLeavesNewerAlone(t *testing.T) {
	lib := scriptLibrary(t, shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": ShortcutFormatVersion + 1}})
	stdout, _ := runSetup(t, "")
	if len(lib.opened) != 0 || !strings.Contains(stdout, "newer than this shelp") {
		t.Errorf("opened %q; stdout:\n%s", lib.opened, stdout)
	}
}

// The per-model shortcuts of formats 1-4, and their copies, are listed for
// the user to delete -- by name, never run.
func TestRunShortcutsSetupListsLegacyWithoutRunningThem(t *testing.T) {
	legacy := []string{"shelp · Apple Intelligence Pro", "shelp · Apple Intelligence Pro 1", "shelp · ChatGPT + Web"}
	lib := scriptLibrary(t, shortcutState{
		names:    append([]string{"shelp", "shelp 2", "Ask AI"}, legacy...),
		versions: map[string]int{"shelp": ShortcutFormatVersion, legacy[0]: 4, legacy[1]: 5, legacy[2]: 4},
	})
	stdout, _ := runSetup(t, "")
	if !strings.Contains(stdout, quoteAll(legacy)) || !strings.Contains(stdout, "right-click › Delete") {
		t.Errorf("stdout should list %q for deletion:\n%s", legacy, stdout)
	}
	if !strings.Contains(stdout, `"shelp 2" look(s) like a copy`) {
		t.Errorf("stdout should point out the old copy:\n%s", stdout)
	}
	if got := lib.probedNames(); !slices.Equal(got, []string{"shelp"}) {
		t.Errorf("probed %q, want only \"shelp\"", got)
	}
}

// A new shortcut that fails its marker is asked at most maxMarkerErrors
// times: a broken format posts a notification every run.
func TestRunShortcutsSetupStopsProbingABrokenNewShortcut(t *testing.T) {
	lib := scriptLibrary(t,
		shortcutState{names: nil, holdUntilOpen: true},
		shortcutState{names: []string{"shelp"}}, // added, but the marker fails
	)
	_, stderr := runSetup(t, "")
	if n := len(lib.probes); n != maxMarkerErrors {
		t.Errorf("ran the marker %d times, want %d", n, maxMarkerErrors)
	}
	if !strings.Contains(stderr, "didn't answer its version check") || strings.Contains(stderr, "timed out") {
		t.Errorf("stderr:\n%s", stderr)
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

// The user clicked "Add Shortcut" while the old shortcut still held the
// name: both are listed under it until they pick "Replace", and the wait
// must neither run anything nor finish then, even though `shortcuts run`
// might already reach the new one. After Replace the old one is "shelp 1",
// which must be pointed out (and never run).
func TestWaitForNewShortcutDuplicatesThenReplace(t *testing.T) {
	lib := scriptLibrary(t,
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 4}},
		shortcutState{names: []string{"shelp", "shelp"}, versions: map[string]int{"shelp": 5}}, // "Add Shortcut" clicked
		shortcutState{names: []string{"shelp", "shelp"}, versions: map[string]int{"shelp": 5}},
		shortcutState{names: []string{"shelp", "shelp 1"}, versions: map[string]int{"shelp": 5, "shelp 1": 4}}, // "Replace"
		shortcutState{names: []string{"shelp", "shelp 1"}, versions: map[string]int{"shelp": 5, "shelp 1": 4}},
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 5}}, // copy deleted
	)
	var out strings.Builder
	res, err := waitForNewShortcut(context.Background(), "shelp", 5, map[string]bool{"shelp": true}, &out, style.Plain())
	if err != nil || !res.installed {
		t.Fatalf("got %+v, %v; want installed\n%s", res, err, out.String())
	}
	got := out.String()
	for msg, want := range map[string]int{`asks "Replace" or "Keep Both"`: 1, "also kept a copy, \"shelp 1\"": 1} {
		if n := strings.Count(got, msg); n != want {
			t.Errorf("%q printed %d times, want %d:\n%s", msg, n, want, got)
		}
	}
	lib.noProbeWhileDuplicated(t, "shelp")
	if slices.Contains(lib.probedNames(), "shelp 1") {
		t.Error("ran the old copy's marker; after a Replace only the name needs asking")
	}
	lib.mu.Lock()
	defer lib.mu.Unlock()
	if lib.i != len(lib.states)-1 {
		t.Errorf("finished at state %d, before the leftover copy was deleted", lib.i)
	}
}

func TestWaitForNewShortcutKeepBoth(t *testing.T) {
	lib := scriptLibrary(t,
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 4}},
		shortcutState{names: []string{"shelp", "shelp"}, versions: map[string]int{"shelp": 4}},
		shortcutState{names: []string{"shelp", "shelp 1"}, versions: map[string]int{"shelp": 4, "shelp 1": 5}}, // "Keep Both"
		shortcutState{names: []string{"shelp", "shelp 1"}, versions: map[string]int{"shelp": 4, "shelp 1": 5}},
		shortcutState{names: []string{"shelp 1"}, versions: map[string]int{"shelp 1": 5}}, // deleted the old one
		shortcutState{names: []string{"shelp"}, versions: map[string]int{"shelp": 5}},     // renamed the copy
	)
	var out strings.Builder
	res, err := waitForNewShortcut(context.Background(), "shelp", 5, map[string]bool{"shelp": true}, &out, style.Plain())
	if err != nil || !res.installed {
		t.Fatalf("got %+v, %v; want installed\n%s", res, err, out.String())
	}
	got := out.String()
	if n := strings.Count(got, `that was "Keep Both"`); n != 1 {
		t.Errorf("Keep Both message printed %d times, want 1:\n%s", n, got)
	}
	for _, want := range []string{`delete the old "shelp"`, `rename "shelp 1" to "shelp"`} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	lib.noProbeWhileDuplicated(t, "shelp")
	if n := strings.Count(strings.Join(lib.probes, " "), "shelp 1@"); n != 1 {
		t.Errorf("ran the Keep Both copy's marker %d times, want once (it doesn't need asking again)", n)
	}
}

// A copy that existed before the dialog opened may be a broken format: it
// is never run, and it doesn't count as this update's leftover.
func TestWaitForNewShortcutNeverRunsOlderCopies(t *testing.T) {
	lib := scriptLibrary(t, shortcutState{names: []string{"shelp", "shelp 1"}, versions: map[string]int{"shelp": 4}}) // dialog never answered
	var out strings.Builder
	res, err := waitForNewShortcut(context.Background(), "shelp", 5, map[string]bool{"shelp": true, "shelp 1": true}, &out, style.Plain())
	if err != nil || res.installed || res.unreadable != nil {
		t.Fatalf("got %+v, %v; want a timeout", res, err)
	}
	if slices.Contains(lib.probedNames(), "shelp 1") {
		t.Fatal("ran the marker on a copy that predates this update")
	}
	if strings.Contains(out.String(), "Keep Both\", so") || strings.Contains(out.String(), "kept a copy") {
		t.Errorf("an older copy was taken for this update's:\n%s", out.String())
	}
}

func TestWaitForShortcutGone(t *testing.T) {
	scriptLibrary(t,
		shortcutState{names: []string{"shelp", "shelp 1"}},
		shortcutState{names: []string{"shelp 1", "shelp"}},
		shortcutState{names: []string{"shelp 1"}},
	)
	var out strings.Builder
	gone, err := waitForShortcutGone(context.Background(), "shelp", &out)
	if err != nil || !gone || out.String() != ".." {
		t.Errorf("got %v, %v, progress %q; want gone after two dots", gone, err, out.String())
	}
}

// --- Live Apple PCC verification (opt-in; real network calls) -----------

// TestLiveShortcut drives the real, installed shortcut (format 5, named
// [shortcuts].name or "shelp") through shortcuts:apple-pro end to end:
// Detect, several timed Generate calls, and a backend verification via
// /usr/bin/log show. It makes real Apple Private Cloud Compute calls
// against an unpublished daily quota, so it's gated behind
// SHELP_LIVE_SHORTCUTS=1 and is never run as part of `go test ./...`.
func TestLiveShortcut(t *testing.T) {
	if os.Getenv("SHELP_LIVE_SHORTCUTS") != "1" {
		t.Skip("set SHELP_LIVE_SHORTCUTS=1 to run (makes real Apple PCC calls against a daily quota)")
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Get(cfg, "shortcuts:apple-pro")
	if err != nil {
		t.Fatalf("Get(shortcuts:apple-pro): %v", err)
	}
	st := p.Detect(context.Background())
	t.Logf("Detect: available=%v reason=%q version=%q", st.Available, st.Reason, st.Version)
	if !st.Available {
		t.Fatalf("expected the shortcut to be detected as installed and current: %+v", st)
	}

	since := time.Now().Add(-2 * time.Second)
	for i := 0; i < 3; i++ {
		start := time.Now()
		text, err := p.Generate(context.Background(), Request{
			Prompt: fmt.Sprintf("Reply with only the single word: pong%d", i),
		})
		if err != nil {
			t.Fatalf("Generate call %d: %v", i, err)
		}
		t.Logf("call %d: %q in %s", i, text, time.Since(start))
	}

	time.Sleep(2 * time.Second) // give unified logging a moment to flush
	raw, models, err := VerifyBackend(context.Background(), since)
	if err != nil {
		t.Logf("VerifyBackend error (non-fatal, reporting only): %v", err)
	}
	t.Logf("VerifyBackend models: %v (raw output %d bytes)", models, len(raw))
}

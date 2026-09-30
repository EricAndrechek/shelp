package provider

// shortcuts: Apple's cloud models (Apple Intelligence, Apple Intelligence
// Pro / Private Cloud Compute, and ChatGPT) reached headlessly through the
// Shortcuts "Use Model" action (is.workflow.actions.askllm), run via the
// `shortcuts` CLI. See shortcutgen.go for how the shortcuts themselves are
// built, and CLAUDE.md's "Shortcuts Use Model" section for how this was
// reverse-engineered.
//
// Shortcuts has no import API: a human has to click "Add Shortcut" once
// per shortcut (see `shelp setup shortcuts` / runShortcutsSetup below).
// Once installed, a shortcut is invoked purely headlessly:
//
//	printf '%s' "$prompt" | shortcuts run "<name>" -o - --output-type public.plain-text
//
// Each shelp variant is really just (model, web-search) baked into one
// installed shortcut; shelp tracks the mapping from variant to shortcut
// *name* (configurable), and looks the name up via `shortcuts list` to
// know whether it's installed.

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// shortcutsRunTimeout bounds one `shortcuts run` call so a hung Shortcuts
// process (offline PCC, a stuck extension, etc.) can't block shelp forever.
const shortcutsRunTimeout = 60 * time.Second

// shortcutVariant is one (model, web-search) combination shelp exposes as
// a provider ID.
type shortcutVariant struct {
	id    string // provider ID, e.g. "shortcuts:apple-pro"
	key   string // config key under [shortcuts].names / .install, e.g. "apple-pro"
	model string // WFLLMModel value
	web   bool   // WFAllowWebSearch ("Use Broad World Knowledge" in the UI)
}

// shortcutVariants is every "Use Model" combination shelp knows about.
// Values are those documented as valid on macOS 27 in CLAUDE.md.
var shortcutVariants = []shortcutVariant{
	{id: "shortcuts:apple", key: "apple", model: "Apple Intelligence", web: false},
	{id: "shortcuts:apple-web", key: "apple-web", model: "Apple Intelligence", web: true},
	{id: "shortcuts:apple-pro", key: "apple-pro", model: "Apple Intelligence Pro", web: false},
	{id: "shortcuts:apple-pro-web", key: "apple-pro-web", model: "Apple Intelligence Pro", web: true},
	{id: "shortcuts:chatgpt", key: "chatgpt", model: "ChatGPT", web: false},
	{id: "shortcuts:chatgpt-web", key: "chatgpt-web", model: "ChatGPT", web: true},
}

// variantByKey finds a variant by its config key (e.g. "apple-pro", not
// the full "shortcuts:apple-pro" provider ID).
func variantByKey(key string) (shortcutVariant, bool) {
	for _, v := range shortcutVariants {
		if v.key == key {
			return v, true
		}
	}
	return shortcutVariant{}, false
}

// defaultInstall is the install list used when [shortcuts].install is
// absent and $SHELP_SHORTCUTS is unset: just the best variant, matching
// DefaultChain's "shortcuts:apple-pro" entry.
var defaultInstall = []string{"apple-pro"}

// shortcutsConfig is the decoded [shortcuts] table.
//
//	[shortcuts]
//	names = { "apple-pro" = "Ask AI" }   # per-variant name override
//	install = ["apple-pro", "chatgpt"]   # what `shelp setup shortcuts` installs
type shortcutsConfig struct {
	Names   map[string]string `toml:"names"`
	Install []string          `toml:"install"`
}

// defaultShortcutName is shelp's naming scheme for a generated shortcut:
// "shelp · <Model>" / "shelp · <Model> + Web". Verified (see MERGE NOTES)
// that `shortcuts run` accepts "·" and "+" in a name without complaint --
// they're ordinary characters to the CLI, which never goes through a
// shell (exec.Command passes argv directly).
func defaultShortcutName(v shortcutVariant) string {
	name := "shelp · " + v.model
	if v.web {
		name += " + Web"
	}
	return name
}

// resolveName returns the shortcut name for v: the config override if
// present and non-blank, else the default naming scheme.
func resolveName(v shortcutVariant, cfg shortcutsConfig) string {
	if n, ok := cfg.Names[v.key]; ok {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return defaultShortcutName(v)
}

// installList resolves which variant keys `shelp setup shortcuts` should
// install: $SHELP_SHORTCUTS ("all" or a comma list) overrides
// [shortcuts].install, which overrides defaultInstall.
func installList(cfg shortcutsConfig) []string {
	if env := strings.TrimSpace(os.Getenv("SHELP_SHORTCUTS")); env != "" {
		if strings.EqualFold(env, "all") {
			keys := make([]string, len(shortcutVariants))
			for i, v := range shortcutVariants {
				keys[i] = v.key
			}
			return keys
		}
		var out []string
		for _, k := range strings.Split(env, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	if len(cfg.Install) > 0 {
		return cfg.Install
	}
	return defaultInstall
}

// --- Family / Provider -----------------------------------------------

type shortcutsFamily struct{}

func init() { Register(shortcutsFamily{}) }

func (shortcutsFamily) Name() string { return "shortcuts" }

func (shortcutsFamily) Providers(cfg *config.Config) []Provider {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if _, ok := LookPath("shortcuts"); !ok {
		return nil
	}
	var sc shortcutsConfig
	_ = cfg.Section("shortcuts", &sc) // malformed config: fall back to defaults

	out := make([]Provider, 0, len(shortcutVariants))
	for _, v := range shortcutVariants {
		out = append(out, &shortcutsProvider{variant: v, name: resolveName(v, sc)})
	}
	return out
}

type shortcutsProvider struct {
	variant shortcutVariant
	name    string
}

func (p *shortcutsProvider) ID() string { return p.variant.id }
func (p *shortcutsProvider) Kind() Kind { return AppleCloud }

func (p *shortcutsProvider) Description() string {
	d := fmt.Sprintf("Apple Shortcuts “Use Model”: %s", p.variant.model)
	if p.variant.web {
		d += " + web search"
	}
	return fmt.Sprintf("%s (shortcut %q)", d, p.name)
}

func (p *shortcutsProvider) Detect(ctx context.Context) Status {
	set, err := installedShortcuts(ctx)
	if err != nil {
		return Status{Reason: fmt.Sprintf("shortcuts list failed: %v", err)}
	}
	if !set[p.name] {
		reason := fmt.Sprintf("shortcut %q not installed: run `shelp setup shortcuts`", p.name)
		if p.variant.model == "ChatGPT" {
			reason += " (also needs the ChatGPT extension enabled in System Settings > Siri)"
		}
		return Status{Reason: reason}
	}
	// Installed and still callable either way, so Available stays true even
	// when outdated -- see outdatedShortcutNote's doc comment for why this
	// is only a Reason note, not an Unavailable.
	st := Status{Available: true, Version: p.variant.model}
	if note := outdatedShortcutNote(ctx, p.name); note != "" {
		st.Reason = note
	}
	return st
}

// shortcutV1ActionCount is how many WFWorkflowActions entries the original,
// marker-less shortcut format has (just Use Model + Stop and Output -- see
// shortcutgen.go's ShortcutFormatVersion doc comment). A shortcut at this
// count predates the version-marker If block entirely, so its single
// action IS Use Model: shortcutVersionMarker must never be sent to it, it
// would go straight to the model as the prompt.
const shortcutV1ActionCount = 2

// appleScriptQuote escapes s for embedding inside a double-quoted
// AppleScript string literal (backslash and double-quote).
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// shortcutActionCountCmd runs a read-only AppleScript query for name's
// installed action count via the Shortcuts Events app's scripting
// dictionary. Verified live on macOS 27.0 against the real, installed
// "Ask AI" shortcut (a v1, 2-action shortcut):
//
//	osascript -e 'tell application "Shortcuts Events" to get action count of shortcut "Ask AI"'
//	-> 2, ~150-170ms
//
// and against a nonexistent name (exit 1, "Can't get shortcut ... (-1728)"
// on stderr). This never runs the shortcut and never touches the model --
// it only asks the app a property of the shortcut object -- so it's the
// cheap, safe check the outdated-version detection is built on: it's safe
// to call on a shortcut of unknown format, unlike shortcutVersionMarker,
// which callers must only send once this check has confirmed the count
// already matches the current generator.
//
// It's a package var, like shortcutsListCmd/openCmd, so tests can fake it
// without ever touching the real Shortcuts app, and so production code can
// wrap/time it.
var shortcutActionCountCmd = func(ctx context.Context, name string) (int, error) {
	script := fmt.Sprintf(`tell application "Shortcuts Events" to get action count of shortcut "%s"`, appleScriptQuote(name))
	res, err := RunCmd(ctx, nil, nil, "osascript", "-e", script)
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("osascript exited %d: %s", res.ExitCode, FirstLine(res.Stderr))
	}
	n, perr := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if perr != nil {
		return 0, fmt.Errorf("parse action count %q: %w", res.Stdout, perr)
	}
	return n, nil
}

// outdatedShortcutNote reports whether the installed shortcut named name is
// outdated relative to the current generator (shortcutgen.go's
// currentShortcutActionCount), by comparing action counts -- never by
// running the marker (see shortcutActionCountCmd's doc comment: never sent
// to a shortcut whose count says v1). It returns "" when the count matches
// the current format, or when the check itself failed: an osascript error
// (Shortcuts Events unavailable, a transient failure, ...) must never make
// an otherwise-working, installed shortcut look unavailable or wrongly
// "outdated" -- this is a best-effort note, not a hard gate.
func outdatedShortcutNote(ctx context.Context, name string) string {
	count, err := shortcutActionCountCmd(ctx, name)
	if err != nil {
		return ""
	}
	current := currentShortcutActionCount()
	if count == current {
		return ""
	}
	if count == shortcutV1ActionCount {
		return fmt.Sprintf("outdated shortcut (format v1, current v%d): run `shelp setup shortcuts` to update", ShortcutFormatVersion)
	}
	return fmt.Sprintf("outdated shortcut (%d actions, current format v%d expects %d): run `shelp setup shortcuts` to update", count, ShortcutFormatVersion, current)
}

func (p *shortcutsProvider) Generate(ctx context.Context, req Request) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, shortcutsRunTimeout)
	defer cancel()

	// Use Model has no separate system-prompt field: fold it into the
	// single input Shortcuts passes through as the Shortcut Input.
	input := req.Prompt
	if req.System != "" {
		input = req.System + "\n\n" + req.Prompt
	}

	res, err := RunCmd(cctx, strings.NewReader(input), nil,
		"shortcuts", "run", p.name, "-o", "-", "--output-type", "public.plain-text")
	if err != nil {
		if cctx.Err() != nil {
			return "", Unavailable(p.ID(), "shortcut %q timed out after %s", p.name, shortcutsRunTimeout)
		}
		return "", err
	}
	// A rate-limited "Use Model" run typically still exits 0: the shortcut
	// itself "succeeds" and its output IS the error text (observed
	// verbatim: "Error: You have reached the usage limit for this model.
	// Please try again later."), not a non-zero exit -- so this check runs
	// before and independent of the ExitCode != 0 branch below, but is
	// itself narrower on exit 0 (see shortcutsRateLimitReason).
	if reason, ok := shortcutsRateLimitReason(res.ExitCode, res.Stdout, res.Stderr); ok {
		return "", RateLimited(p.ID(), 0, "%s", reason)
	}
	if res.ExitCode != 0 {
		msg := FirstLine(res.Stderr)
		if msg == "" {
			msg = FirstLine(res.Stdout)
		}
		if strings.Contains(strings.ToLower(msg), "find shortcut") {
			return "", Unavailable(p.ID(), "shortcut %q not installed: run `shelp setup shortcuts`", p.name)
		}
		if msg == "" {
			msg = fmt.Sprintf("shortcuts run exited %d", res.ExitCode)
		}
		return "", Unavailable(p.ID(), "%s", msg)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// shortcutsRateLimitPhrases are lowercase substrings that, found in a
// completed "shortcuts run" call's stdout or stderr, mean Apple's model
// backend rejected the call for being over its usage limit. Observed live
// on Apple Intelligence Pro during a shelp-bench run: ~93 calls in ~2.5
// minutes, then every further call replied
// "Error: You have reached the usage limit for this model. Please try
// again later." (reset window unknown). Every failed Shortcuts run also
// posts a macOS notification, which is the whole reason this matters:
// once this phrase is seen, provider.Chain and the bench breaker both
// stop calling the shortcut instead of retrying it (and renotifying) on
// every subsequent trial/call. Matching is case-insensitive.
var shortcutsRateLimitPhrases = []string{"usage limit", "try again later"}

// shortcutsRateLimitReason reports whether a completed "shortcuts run"
// call was rejected for being over its usage limit, and if so, a short
// human-readable reason for it. A rate-limited run typically still exits
// 0 (see Generate), so this checks two distinct shapes rather than a
// blanket substring search over stdout+stderr regardless of exit code --
// a plain substring match on exit 0 would misfire on a perfectly good
// suggest/explain answer that happens to legitimately contain the phrase
// "try again later" (e.g. explaining a retry loop):
//
//   - exitCode != 0 and stdout or stderr contains any shortcutsRateLimitPhrases
//     entry ("usage limit" or "try again later").
//   - exitCode == 0 and the trimmed stdout starts with "Error:" and
//     contains "usage limit" -- narrower than (a): "try again later" alone
//     is not enough on a successful exit, only the specific, observed
//     "Error: ... usage limit ..." shape is.
func shortcutsRateLimitReason(exitCode int, stdout, stderr string) (string, bool) {
	trimmedStdout := strings.TrimSpace(stdout)
	matched := false
	switch {
	case exitCode != 0:
		low := strings.ToLower(stdout + "\n" + stderr)
		for _, phrase := range shortcutsRateLimitPhrases {
			if strings.Contains(low, phrase) {
				matched = true
				break
			}
		}
	case strings.HasPrefix(trimmedStdout, "Error:") && strings.Contains(strings.ToLower(trimmedStdout), "usage limit"):
		matched = true
	}
	if !matched {
		return "", false
	}
	reason := FirstLine(stdout)
	if reason == "" {
		reason = FirstLine(stderr)
	}
	if reason == "" {
		reason = "usage limit"
	}
	return reason, true
}

// --- `shortcuts list` cache --------------------------------------------

// shortcutsListCmd runs `shortcuts list` and returns its raw stdout (one
// name per line). It's a package var so tests can fake it without
// spawning a real process or, for the setup step, popping any UI.
var shortcutsListCmd = func(ctx context.Context) (string, error) {
	res, err := RunCmd(ctx, nil, nil, "shortcuts", "list")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("shortcuts list exited %d: %s", res.ExitCode, FirstLine(res.Stderr))
	}
	return res.Stdout, nil
}

// parseShortcutsList turns `shortcuts list` output into a name set.
func parseShortcutsList(output string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set
}

var (
	shortcutsListOnce sync.Once
	shortcutsListSet  map[string]bool
	shortcutsListErr  error
)

// installedShortcuts returns the set of installed shortcut names, cached
// for the life of the process: `shortcuts list` is the same ~50ms-plus
// round trip every time and every provider's Detect calls it once per
// `shelp providers` invocation. Only Detect uses this cache; Generate and
// the setup step always ask fresh (see shortcutsListCmd).
func installedShortcuts(ctx context.Context) (map[string]bool, error) {
	shortcutsListOnce.Do(func() {
		out, err := shortcutsListCmd(ctx)
		if err != nil {
			shortcutsListErr = err
			return
		}
		shortcutsListSet = parseShortcutsList(out)
	})
	return shortcutsListSet, shortcutsListErr
}

// --- Setup step ---------------------------------------------------------

// openCmd opens path with the default handler (Shortcuts, for a
// .shortcut file), which shows the "Add Shortcut" import dialog. It's a
// package var so the setup step's control flow can be tested without
// ever actually popping that dialog.
var openCmd = func(ctx context.Context, path string) error {
	res, err := RunCmd(ctx, nil, nil, "open", path)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("open %s exited %d: %s", path, res.ExitCode, FirstLine(res.Stderr))
	}
	return nil
}

// shortcutsPollInterval/-Timeout govern how the setup step waits for a
// human to click "Add Shortcut". Package vars so tests can shrink them.
var (
	shortcutsPollInterval = 2 * time.Second
	shortcutsPollTimeout  = 3 * time.Minute
)

// waitForShortcut polls `shortcuts list` until name appears, printing one
// "." to progress per attempt, up to shortcutsPollTimeout. It checks
// immediately before ever sleeping, so an already-installed shortcut (or
// a very fast click) returns right away.
func waitForShortcut(ctx context.Context, name string, progress io.Writer) (bool, error) {
	deadline := time.Now().Add(shortcutsPollTimeout)
	for {
		out, err := shortcutsListCmd(ctx)
		if err != nil {
			return false, err
		}
		if parseShortcutsList(out)[name] {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		fmt.Fprint(progress, ".")
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(shortcutsPollInterval):
		}
	}
}

// waitForShortcutUpdate polls name's installed action count (via
// shortcutActionCountCmd) until it equals wantCount, printing one "." per
// attempt, up to shortcutsPollTimeout. It checks immediately before ever
// sleeping, like waitForShortcut. Used after re-opening an ALREADY
// installed, outdated shortcut for the user to click "Replace": unlike
// waitForShortcut (which waits for a new name to appear in `shortcuts
// list`), the name is already installed throughout, so presence alone
// can't distinguish an accepted replace from a declined/unopened import
// dialog -- only the action count actually changing can.
func waitForShortcutUpdate(ctx context.Context, name string, wantCount int, progress io.Writer) (bool, error) {
	deadline := time.Now().Add(shortcutsPollTimeout)
	for {
		count, err := shortcutActionCountCmd(ctx, name)
		if err != nil {
			return false, err
		}
		if count == wantCount {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		fmt.Fprint(progress, ".")
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(shortcutsPollInterval):
		}
	}
}

// shortcutSetupKeys returns the variant keys runShortcutsSetup should
// process: installList's keys (config/env, in order), plus -- so an
// upgrade covers everything the user already has, not just what's
// currently configured -- any other shortcutVariants entry whose resolved
// name is already installed. Unknown keys from installList are left in
// place; the caller reports and skips them (as before), since a typo
// there is a config problem worth surfacing, not silently dropping.
func shortcutSetupKeys(wanted []string, sc shortcutsConfig, installed map[string]bool) []string {
	keys := append([]string(nil), wanted...)
	seen := make(map[string]bool, len(wanted))
	for _, k := range wanted {
		seen[k] = true
	}
	for _, v := range shortcutVariants {
		if seen[v.key] {
			continue
		}
		if installed[resolveName(v, sc)] {
			keys = append(keys, v.key)
			seen[v.key] = true
		}
	}
	return keys
}

// runShortcutsSetup is the "shortcuts" SetupStep. For each variant in
// shortcutSetupKeys, it handles one of three states:
//
//   - missing: generate, sign, open (user clicks "Add Shortcut"), poll
//     `shortcuts list` until the name appears -- as before.
//   - outdated (installed, but its action count doesn't match the current
//     generator's -- see shortcutgen.go's currentShortcutActionCount):
//     regenerate, sign, open (user clicks "Replace" this time -- Shortcuts
//     offers that button when importing over an existing name; UNVERIFIED
//     without a human actually doing it, see MERGE NOTES), then poll the
//     action count (not presence: the name doesn't change) until it
//     matches the current count, with the same timeout as install.
//   - current: skip with a message.
//
// An action-count check that itself fails (Shortcuts Events unavailable,
// transient) is never treated as outdated -- same reasoning as Detect's
// outdatedShortcutNote -- so that variant is skipped with a note instead
// of being needlessly re-opened.
func runShortcutsSetup(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) error {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(stdout, "shortcuts: darwin only, nothing to do here")
		return nil
	}
	if _, ok := LookPath("shortcuts"); !ok {
		fmt.Fprintln(stdout, "shortcuts: `shortcuts` CLI not found (needs macOS 12+), nothing to do here")
		return nil
	}

	var sc shortcutsConfig
	if err := cfg.Section("shortcuts", &sc); err != nil {
		return err
	}
	wanted := installList(sc)

	rawList, err := shortcutsListCmd(ctx)
	if err != nil {
		return fmt.Errorf("shortcuts list: %w", err)
	}
	installed := parseShortcutsList(rawList)

	tmpDir, err := os.MkdirTemp("", "shelp-shortcuts-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	current := currentShortcutActionCount()
	for _, key := range shortcutSetupKeys(wanted, sc, installed) {
		v, ok := variantByKey(key)
		if !ok {
			fmt.Fprintf(stderr, "shortcuts: unknown variant %q in install list, skipping\n", key)
			continue
		}
		name := resolveName(v, sc)

		if !installed[name] {
			fmt.Fprintf(stdout, "shortcuts: %s: generating %q...\n", v.id, name)
			path, err := GenerateShortcut(ctx, tmpDir, name, v.model, v.web)
			if err != nil {
				return fmt.Errorf("%s: generate: %w", v.id, err)
			}
			fmt.Fprintf(stdout, "shortcuts: %s: opening Shortcuts -- click \"Add Shortcut\" to install %q\n", v.id, name)
			if err := openCmd(ctx, path); err != nil {
				return fmt.Errorf("%s: open: %w", v.id, err)
			}
			fmt.Fprintf(stdout, "shortcuts: %s: waiting for you to add it (up to %s) ", v.id, shortcutsPollTimeout)
			added, err := waitForShortcut(ctx, name, stdout)
			fmt.Fprintln(stdout)
			if err != nil {
				return fmt.Errorf("%s: %w", v.id, err)
			}
			if !added {
				fmt.Fprintf(stderr, "shortcuts: %s: timed out waiting for %q; run `shelp setup shortcuts` again once it's added\n", v.id, name)
				continue
			}
			fmt.Fprintf(stdout, "shortcuts: %s: %q installed\n", v.id, name)
			installed[name] = true
			continue
		}

		count, cerr := shortcutActionCountCmd(ctx, name)
		if cerr != nil {
			fmt.Fprintf(stdout, "shortcuts: %s: %q installed, couldn't check its version (%v); assuming current\n", v.id, name, cerr)
			continue
		}
		if count == current {
			fmt.Fprintf(stdout, "shortcuts: %s: %q already up to date (v%d), skipping\n", v.id, name, ShortcutFormatVersion)
			continue
		}

		fmt.Fprintf(stdout, "shortcuts: %s: %q is outdated (%d actions, current v%d expects %d); regenerating...\n", v.id, name, count, ShortcutFormatVersion, current)
		path, err := GenerateShortcut(ctx, tmpDir, name, v.model, v.web)
		if err != nil {
			return fmt.Errorf("%s: generate: %w", v.id, err)
		}
		fmt.Fprintf(stdout, "shortcuts: %s: opening Shortcuts -- click \"Replace\" to update %q\n", v.id, name)
		if err := openCmd(ctx, path); err != nil {
			return fmt.Errorf("%s: open: %w", v.id, err)
		}
		fmt.Fprintf(stdout, "shortcuts: %s: waiting for you to replace it (up to %s) ", v.id, shortcutsPollTimeout)
		updated, err := waitForShortcutUpdate(ctx, name, current, stdout)
		fmt.Fprintln(stdout)
		if err != nil {
			return fmt.Errorf("%s: %w", v.id, err)
		}
		if !updated {
			fmt.Fprintf(stderr, "shortcuts: %s: timed out waiting for %q to update; run `shelp setup shortcuts` again once it's replaced\n", v.id, name)
			continue
		}
		fmt.Fprintf(stdout, "shortcuts: %s: %q updated to v%d\n", v.id, name, ShortcutFormatVersion)
	}

	fmt.Fprintln(stdout, "shortcuts: status:")
	for _, v := range shortcutVariants {
		name := resolveName(v, sc)
		status := "not installed"
		if installed[name] {
			status = "installed"
		}
		fmt.Fprintf(stdout, "  %-24s %-32s %s\n", v.id, name, status)
	}
	return nil
}

func init() {
	RegisterSetup(SetupStep{
		Name: "shortcuts",
		Help: "install shelp's Apple Shortcuts \"Use Model\" wrappers (opens Shortcuts; you click Add Shortcut)",
		Run:  runShortcutsSetup,
	})
}

// --- Backend verification helper ----------------------------------------

// VerifyBackendArgs returns the `/usr/bin/log show` invocation that
// reveals which backend actually served a recent Shortcuts "Use Model"
// run. Run it right after a Generate call; matching log lines carry a
// model identifier such as
// "com.apple.fm.language.instruct_server_v2.shortcuts_use_model_pro" for
// Apple Intelligence Pro. Never invoke bare `log` from zsh -- it's a
// shell builtin there that silently no-ops; always use the full path
// (this is why the args start with "/usr/bin/log", not "log").
func VerifyBackendArgs(since time.Time) []string {
	return []string{
		"/usr/bin/log", "show",
		"--start", since.Format("2006-01-02 15:04:05"),
		"--style", "compact",
		"--predicate", `process == "PrivateMLClientInferenceProviderService" OR process == "TGOnDeviceInferenceProviderService" OR process == "BackgroundShortcutRunner"`,
	}
}

var modelIDRE = regexp.MustCompile(`\bcom\.apple\.[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+\b`)

// extractModelIDs pulls reverse-DNS-style model/service identifiers
// (e.g. "com.apple.fm.language.instruct_server_v2.shortcuts_use_model_pro")
// out of log text, deduplicated in first-seen order.
func extractModelIDs(logText string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range modelIDRE.FindAllString(logText, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// VerifyBackend runs VerifyBackendArgs and extracts model identifiers
// from its output. Give it a `since` timestamp from just before the
// Generate call under test -- macOS's log retention/buffering means this
// should be called promptly afterward.
func VerifyBackend(ctx context.Context, since time.Time) (raw string, models []string, err error) {
	args := VerifyBackendArgs(since)
	res, err := RunCmd(ctx, nil, nil, args[0], args[1:]...)
	if err != nil {
		return "", nil, err
	}
	if res.ExitCode != 0 {
		return res.Stdout + res.Stderr, nil, fmt.Errorf("log show exited %d: %s", res.ExitCode, FirstLine(res.Stderr))
	}
	return res.Stdout, extractModelIDs(res.Stdout), nil
}

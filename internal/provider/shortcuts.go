package provider

// shortcuts: Apple's cloud models (Apple Intelligence, Apple Intelligence
// Pro / Private Cloud Compute, and ChatGPT) reached headlessly through the
// Shortcuts "Use Model" action (is.workflow.actions.askllm), run via the
// `shortcuts` CLI. See shortcutgen.go for how the shortcut itself is
// built, and CLAUDE.md's "Shortcuts Use Model" section for how this was
// reverse-engineered.
//
// Shortcuts has no import API: a human has to click "Add Shortcut" (see
// `shelp setup shortcuts` / runShortcutsSetup below). Once installed, the
// shortcut is invoked purely headlessly:
//
//	printf '%s' '{"model":"apple-pro","prompt":"..."}' | shortcuts run shelp -o - --output-type public.plain-text
//
// Since format 5 one shortcut ("shelp" unless [shortcuts].name says
// otherwise) serves all six providers: each sends its own variant key as
// "model", and the shortcut picks the matching Use Model action. Formats
// 1-4 were one shortcut per variant, named "shelp · <Model>[ + Web]"; those
// are now only ever listed by name (legacyShortcuts) and never run.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/style"
)

// shortcutsRunTimeout bounds one `shortcuts run` call so a hung Shortcuts
// process (offline PCC, a stuck extension, etc.) can't block shelp forever.
const shortcutsRunTimeout = 60 * time.Second

// shortcutVariant is one (model, web-search) combination shelp exposes as
// a provider ID.
type shortcutVariant struct {
	id    string // provider ID, e.g. "shortcuts:apple-pro"
	key   string // the request's "model", which the shortcut's Ifs compare, e.g. "apple-pro"
	model string // WFLLMModel value
	web   bool   // WFAllowWebSearch ("Use Broad World Knowledge" in the UI)
}

// shortcutVariants is every "Use Model" combination shelp knows about, and
// (in this order) the branches of the generated shortcut. Values are those
// documented as valid on macOS 27 in CLAUDE.md.
var shortcutVariants = []shortcutVariant{
	{id: "shortcuts:apple", key: "apple", model: "Apple Intelligence", web: false},
	{id: "shortcuts:apple-web", key: "apple-web", model: "Apple Intelligence", web: true},
	{id: "shortcuts:apple-pro", key: "apple-pro", model: "Apple Intelligence Pro", web: false},
	{id: "shortcuts:apple-pro-web", key: "apple-pro-web", model: "Apple Intelligence Pro", web: true},
	{id: "shortcuts:chatgpt", key: "chatgpt", model: "ChatGPT", web: false},
	{id: "shortcuts:chatgpt-web", key: "chatgpt-web", model: "ChatGPT", web: true},
}

// variantByKey finds a variant by its key (e.g. "apple-pro", not the full
// "shortcuts:apple-pro" provider ID).
func variantByKey(key string) (shortcutVariant, bool) {
	for _, v := range shortcutVariants {
		if v.key == key {
			return v, true
		}
	}
	return shortcutVariant{}, false
}

// defaultShortcutName is the name `shelp setup shortcuts` installs the
// shortcut under, and every shortcuts:* provider runs, unless
// [shortcuts].name overrides it.
const defaultShortcutName = "shelp"

// chatGPTExtension is what the ChatGPT variants need beyond the shortcut.
const chatGPTExtension = "the ChatGPT extension (System Settings › Siri)"

// shortcutsConfig is the decoded [shortcuts] table.
//
//	[shortcuts]
//	name = "shelp"   # the shortcut every shortcuts:* provider runs
type shortcutsConfig struct {
	Name string `toml:"name"`
}

// shortcutName is the configured name if present and non-blank, else
// defaultShortcutName.
func (sc shortcutsConfig) shortcutName() string {
	if n := strings.TrimSpace(sc.Name); n != "" {
		return n
	}
	return defaultShortcutName
}

// resolveShortcutName decodes cfg's [shortcuts] table, falling back to the
// default on a malformed one.
func resolveShortcutName(cfg *config.Config) string {
	var sc shortcutsConfig
	_ = cfg.Section("shortcuts", &sc)
	return sc.shortcutName()
}

// legacyShortcutName is the per-variant name formats 1-4 (shelp 0.1-0.2)
// generated: "shelp · <Model>", or "shelp · <Model> + Web".
func legacyShortcutName(v shortcutVariant) string {
	name := "shelp · " + v.model
	if v.web {
		name += " + Web"
	}
	return name
}

// legacyShortcuts returns, sorted, the names in installed that formats
// 1-4 left behind: a legacyShortcutName, or a "<that name> N" copy an
// update made. current (the configured name) is never one of them. These
// are only ever listed: an old one may be a format that fails, posting a
// notification, every time it runs, so shelp never runs them.
func legacyShortcuts(installed map[string]bool, current string) []string {
	var out []string
	for _, v := range shortcutVariants {
		old := legacyShortcutName(v)
		if installed[old] && old != current {
			out = append(out, old)
		}
		for _, c := range conflictCopies(installed, old) {
			if c != current {
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
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
	name := resolveShortcutName(cfg)
	out := make([]Provider, 0, len(shortcutVariants))
	for _, v := range shortcutVariants {
		out = append(out, &shortcutsProvider{variant: v, name: name})
	}
	return out
}

type shortcutsProvider struct {
	variant shortcutVariant
	name    string // the shortcut to run, shared by every variant
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

// Label is the short display name: the model, plus " + web search" for the
// web variants, 
func (p *shortcutsProvider) Label() string {
	l := p.variant.model
	if p.variant.web {
		l += " + web search"
	}
	return l
}

// Detect reports the provider available only when the shared shortcut is
// installed and answers the version marker with ShortcutFormatVersion or
// newer: an older one doesn't take the JSON request (formats 1-4 would pass
// it to the model as the prompt). The version check runs once per process
// for all six providers (cachedShortcutVersion).
func (p *shortcutsProvider) Detect(ctx context.Context) Status {
	const fix = "run `shelp setup shortcuts`"
	set, err := installedShortcuts(ctx)
	if err != nil {
		return Status{Reason: "can't list shortcuts", Detail: err.Error()}
	}
	if !set[p.name] {
		reason := "not installed"
		if p.variant.model == "ChatGPT" {
			reason += "; needs " + chatGPTExtension
		}
		return Status{Reason: reason, Fix: fix, Detail: fmt.Sprintf("shortcut %q not found", p.name)}
	}
	v, err := cachedShortcutVersion(ctx, p.name)
	if err != nil {
		return Status{Reason: "shortcut didn't answer its version check", Fix: fix, Detail: fmt.Sprintf("shortcut %q: %v", p.name, err)}
	}
	group := fmt.Sprintf("%q · v%d", p.name, v)
	if v < ShortcutFormatVersion {
		return Status{Reason: fmt.Sprintf("outdated shortcut (format v%d, current v%d)", v, ShortcutFormatVersion), Fix: fix + " to update it", Detail: fmt.Sprintf("shortcut %q", p.name), Group: group}
	}
	return Status{Available: true, Version: p.variant.model, Group: group}
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
// "Ask AI" shortcut (a v1, 2-action shortcut built by hand in the editor):
//
//	osascript -e 'tell application "Shortcuts Events" to get action count of shortcut "Ask AI"'
//	-> 2, ~150-170ms
//
// and against a nonexistent name (exit 1, "Can't get shortcut ... (-1728)"
// on stderr). This never runs the shortcut and never touches the model --
// it only asks the app a property of the shortcut object -- so it's safe
// to call on a shortcut of unknown format, unlike shortcutVersionMarker.
//
// It reads a count Shortcuts caches when a shortcut is saved, and a
// shortcut imported from a .shortcut file -- which is how `shelp setup
// shortcuts` installs every one -- never gets it: it reads 0 (subtitle "No
// actions") with all its actions stored, verified live on macOS 27.0. Only
// a nonzero count says anything; see installedShortcutVersion.
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

// shortcutMarkerMinActions is the fewest actions any marker-carrying
// format (v2+) has: 6 (If, Stop and Output, Otherwise, Use Model, Stop and
// Output, End If; v4 adds a Text action for 7, v5 has 40). A shortcut
// below this is never sent shortcutVersionMarker: it can't contain the
// marker branch, so the marker would reach Use Model as a real prompt.
const shortcutMarkerMinActions = 6

// shortcutMarkerVersionCmd asks an installed shortcut which format it was
// generated with by sending it shortcutVersionMarker. For v4+ the marker
// branch answers "shelp-shortcut <N>" without ever reaching Use Model, so
// this costs no model call, no quota, and posts no notification. A v2 or
// v3 shortcut fails here like it fails every run (exit 1, and Shortcuts
// posts a notification), and that failure is just an unreadable version.
// Callers go through installedShortcutVersion, which never sends the
// marker to a shortcut whose count rules the marker branch out. A package
// var so tests never run a real shortcut.
var shortcutMarkerVersionCmd = func(ctx context.Context, name string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := RunCmd(cctx, strings.NewReader(shortcutVersionMarker), nil,
		"shortcuts", "run", name, "-o", "-", "--output-type", "public.plain-text")
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("shortcuts run exited %d: %s", res.ExitCode, FirstLine(res.Stderr))
	}
	return parseShortcutMarkerReply(res.Stdout)
}

// parseShortcutMarkerReply parses the marker branch's "shelp-shortcut <N>".
func parseShortcutMarkerReply(out string) (int, error) {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) == 2 && f[0] == "shelp-shortcut" {
		if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("unexpected version-marker reply %q", strings.TrimSpace(out))
}

// installedShortcutVersion reports which ShortcutFormatVersion the
// installed shortcut called name was generated with. Format 1 has no
// marker and is recognized by its action count alone; every later format
// answers the marker, so a format change that keeps the same number of
// actions is still detected. A count of 0 means Shortcuts never cached
// one (a file import, see shortcutActionCountCmd), so the marker is the
// only way to tell; a nonzero count too small to hold the marker branch
// (and not v1) is reported as an error rather than probed.
//
// The one case this sends the marker somewhere it doesn't belong is a
// file-imported shortcut without the marker branch -- a v1 file from shelp
// 0.1.x, or a shortcut from elsewhere named by [shortcuts].name -- where
// it reaches the model as one ordinary prompt.
func installedShortcutVersion(ctx context.Context, name string) (int, error) {
	count, err := shortcutActionCountCmd(ctx, name)
	if err != nil {
		return 0, err
	}
	if count == shortcutV1ActionCount {
		return 1, nil
	}
	if count != 0 && count < shortcutMarkerMinActions {
		return 0, fmt.Errorf("%q has %d actions: not a shelp-generated format", name, count)
	}
	return shortcutMarkerVersionCmd(ctx, name)
}

// shortcutRequestBody is the JSON a format 5+ shortcut takes as its
// input: {"model": key, "prompt": prompt}, without HTML escaping (the
// shortcut's Get Dictionary from Input would undo it anyway; this keeps
// the body readable) and without a trailing newline.
func shortcutRequestBody(key, prompt string) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(shortcutRequest{Model: key, Prompt: prompt}); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// shortcutErrorReply reports whether out is the shortcut's own error
// answer (its fallback branch: "shelp-shortcut-error: unknown model <key>",
// reached when no If matched the request's model), and its message.
func shortcutErrorReply(out string) (string, bool) {
	msg, ok := strings.CutPrefix(strings.TrimSpace(out), shortcutErrorPrefix)
	return strings.TrimSpace(msg), ok
}

func (p *shortcutsProvider) Generate(ctx context.Context, req Request) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, shortcutsRunTimeout)
	defer cancel()

	// Use Model has no separate system-prompt field: fold it into the one
	// prompt the request carries.
	prompt := req.Prompt
	if req.System != "" {
		prompt = req.System + "\n\n" + req.Prompt
	}
	body, err := shortcutRequestBody(p.variant.key, prompt)
	if err != nil {
		return "", err
	}

	res, err := RunCmd(cctx, strings.NewReader(body), nil,
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
	if msg, ok := shortcutErrorReply(res.Stdout); ok {
		return "", Unavailable(p.ID(), "shortcut %q answered %q: run `shelp setup shortcuts` to update it", p.name, msg)
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

// --- `shortcuts list` and version caches -----------------------------

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

// countShortcutName counts the lines of raw `shortcuts list` output that
// are exactly name. Importing over a taken name briefly lists both
// shortcuts under it (after "Add Shortcut", until "Replace"/"Keep Both"
// renames one), and `shortcuts run <name>` may then reach either, so a
// name listed twice is never taken as installed.
func countShortcutName(raw, name string) int {
	n := 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == name {
			n++
		}
	}
	return n
}

// shortcutsCache holds, for the life of the process, what Detect learns:
// `shortcuts list` (the same ~50ms-plus round trip for every provider) and
// each shortcut's version (a marker run, ~1s -- six providers share one
// shortcut, so one run serves all six). Only Detect and the `shelp
// providers` notes use it; Generate and the setup step always ask fresh.
var shortcutsCache struct {
	mu       sync.Mutex
	listDone bool
	list     map[string]bool
	listErr  error
	versions map[string]*shortcutVersionProbe
}

type shortcutVersionProbe struct {
	once sync.Once
	v    int
	err  error
}

// installedShortcuts returns the set of installed shortcut names, cached
// for the life of the process (see shortcutsCache).
func installedShortcuts(ctx context.Context) (map[string]bool, error) {
	c := &shortcutsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.listDone {
		out, err := shortcutsListCmd(ctx)
		if err != nil {
			c.listErr = err
		} else {
			c.list = parseShortcutsList(out)
		}
		c.listDone = true
	}
	return c.list, c.listErr
}

// cachedShortcutVersion is installedShortcutVersion, run at most once per
// name per process however many providers ask concurrently.
func cachedShortcutVersion(ctx context.Context, name string) (int, error) {
	c := &shortcutsCache
	c.mu.Lock()
	if c.versions == nil {
		c.versions = map[string]*shortcutVersionProbe{}
	}
	p := c.versions[name]
	if p == nil {
		p = &shortcutVersionProbe{}
		c.versions[name] = p
	}
	c.mu.Unlock()
	p.once.Do(func() { p.v, p.err = installedShortcutVersion(ctx, name) })
	return p.v, p.err
}

// legacyShortcutsNotes is the shortcuts family's `shelp providers` note:
// the format 1-4 shortcuts still installed, by name only (never run).
func legacyShortcutsNotes(ctx context.Context, cfg *config.Config) []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if _, ok := LookPath("shortcuts"); !ok {
		return nil
	}
	set, err := installedShortcuts(ctx)
	if err != nil {
		return nil
	}
	name := resolveShortcutName(cfg)
	legacy := legacyShortcuts(set, name)
	if len(legacy) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("old shelp shortcuts, no longer used (shelp now uses one, %q): %s -- delete them in the Shortcuts app", name, quoteAll(legacy))}
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

// generateShortcut is GenerateShortcut, as a package var so the setup
// step's tests don't sign (a call to Apple's server) on every run.
var generateShortcut = GenerateShortcut

// shortcutsPollInterval/-Timeout govern how the setup step waits for a
// human to act in the Shortcuts app. Package vars so tests can shrink them.
var (
	shortcutsPollInterval = time.Second
	shortcutsPollTimeout  = 3 * time.Minute
)

// maxMarkerErrors is how many failed marker runs one wait allows per
// shortcut before it stops asking that shortcut: a shortcut that fails
// every run posts a notification every time, so a broken new format must
// not be polled every second for minutes.
const maxMarkerErrors = 3

// waitForShortcutGone polls `shortcuts list` -- running nothing -- until
// no shortcut is named name, printing one "." to progress per attempt, up
// to shortcutsPollTimeout. It checks immediately before ever sleeping.
func waitForShortcutGone(ctx context.Context, name string, progress io.Writer) (bool, error) {
	deadline := time.Now().Add(shortcutsPollTimeout)
	for {
		raw, err := shortcutsListCmd(ctx)
		if err != nil {
			return false, err
		}
		if countShortcutName(raw, name) == 0 {
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

// conflictCopies returns the names in list that Shortcuts gives a
// shortcut imported over an existing name -- "<name> 1", "<name> 2", ...
// -- sorted. Neither button in the import's "Replace"/"Keep Both" prompt
// deletes anything (verified on macOS 27, 2026-10-07): "Replace" gives the
// new shortcut the name and renames the old one "<name> 1"; "Keep Both"
// names the new one "<name> 1". N can skip numbers: names of deleted
// shortcuts still count.
func conflictCopies(list map[string]bool, name string) []string {
	var out []string
	for n := range list {
		if rest, ok := strings.CutPrefix(n, name+" "); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}

// waitResult is how waitForNewShortcut ended.
type waitResult struct {
	installed bool
	// unreadable is the marker's last error when the new shortcut, the only
	// one with the name, failed its version check maxMarkerErrors times.
	unreadable error
}

// waitForNewShortcut runs after the import dialog opens and returns once
// exactly one shortcut is named name, it answers wantVersion or newer, and
// no "<name> N" copy that appeared during the wait is left. Normally the
// name is free when the dialog opens (a fresh install, or an update whose
// old shortcut the user already deleted) and "Add Shortcut" is all there
// is. If the name is taken anyway (the user didn't delete the old one, or
// it came back), Shortcuts asks "Replace" or "Keep Both" after "Add
// Shortcut", and every poll works out which happened:
//
//   - the name is listed twice: the dialog is still open (both shortcuts
//     carry the name until the user answers, and `shortcuts run` may reach
//     either), so nothing is run and nothing counts as done.
//   - Replace: name answers wantVersion, and the old shortcut was kept as
//     "<name> N". It says to delete that copy and waits for it to go;
//     Ctrl-C there is harmless, the update has already worked.
//   - Keep Both: name still answers the old version while a copy that
//     appeared during this wait answers wantVersion. It says to delete the
//     old one and rename the copy, then waits for name to answer
//     wantVersion with no copy left over.
//
// before is the shortcut list from just before the dialog opened. Only
// name and copies that appeared since are ever asked their version, at
// most maxMarkerErrors failed runs each, so an older leftover -- perhaps a
// broken v2 that would fail and post a notification -- is never run. Each
// new instruction restarts the shortcutsPollTimeout clock, since the user
// has something new to do. out gets one "." per poll after the caller's
// "waiting..." line, which this function ends. A zero waitResult is a
// timeout.
func waitForNewShortcut(ctx context.Context, name string, wantVersion int, before map[string]bool, out io.Writer, pal style.Palette) (waitResult, error) {
	deadline := time.Now().Add(shortcutsPollTimeout)
	midLine := true
	say := func(msg string) {
		if midLine {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, pal.Warn(msg))
		midLine = false
		deadline = time.Now().Add(shortcutsPollTimeout)
	}
	finish := func(r waitResult) (waitResult, error) {
		if midLine {
			fmt.Fprintln(out)
		}
		return r, nil
	}
	failures := map[string]int{}
	var lastErr error
	probe := func(n string) (int, bool) {
		if failures[n] >= maxMarkerErrors {
			return 0, false
		}
		v, err := installedShortcutVersion(ctx, n)
		if err != nil {
			failures[n]++
			lastErr = err
			return 0, false
		}
		return v, true
	}
	var toldDup bool
	var toldCopies, keptBoth string
	for {
		if raw, err := shortcutsListCmd(ctx); err == nil {
			list := parseShortcutsList(raw)
			var fresh []string
			for _, c := range conflictCopies(list, name) {
				if !before[c] {
					fresh = append(fresh, c)
				}
			}
			switch count := countShortcutName(raw, name); {
			case count > 1:
				if !toldDup {
					toldDup = true
					say(fmt.Sprintf("two shortcuts are named %q while Shortcuts asks \"Replace\" or \"Keep Both\"; either works, and shelp will say what's left to tidy up.", name))
				}
			default:
				current := false
				if count == 1 {
					v, ok := probe(name)
					current = ok && v >= wantVersion
					if !ok && failures[name] >= maxMarkerErrors && len(fresh) == 0 {
						return finish(waitResult{unreadable: lastErr})
					}
				}
				switch {
				case current && len(fresh) == 0:
					return finish(waitResult{installed: true})
				case current:
					if q := quoteAll(fresh); q != toldCopies {
						toldCopies = q
						say(fmt.Sprintf("updated -- but Shortcuts also kept a copy, %s: delete it in the Shortcuts app (shelp can't delete shortcuts). Waiting for that; Ctrl-C is fine too, the update itself is done.", q))
					}
				default:
					for _, c := range fresh {
						if c == keptBoth {
							break // already found and explained; no need to run it again
						}
						if v, ok := probe(c); ok && v >= wantVersion {
							keptBoth = c
							say(fmt.Sprintf("that was \"Keep Both\", so the update is %q: in the Shortcuts app, delete the old %q, then rename %q to %q. Waiting for that...", c, name, c, name))
							break
						}
					}
				}
			}
		}
		if !time.Now().Before(deadline) {
			return finish(waitResult{})
		}
		fmt.Fprint(out, ".")
		midLine = true
		select {
		case <-ctx.Done():
			finish(waitResult{})
			return waitResult{}, ctx.Err()
		case <-time.After(shortcutsPollInterval):
		}
	}
}

// addShortcut opens the generated file at path (Shortcuts shows "Add
// Shortcut") and waits for the user to add it. It reports whether the
// shortcut ended up installed and current.
func addShortcut(ctx context.Context, name, path string, stdout, stderr io.Writer) (bool, error) {
	out, errp := style.For(stdout), style.For(stderr)
	before := map[string]bool{}
	if raw, err := shortcutsListCmd(ctx); err == nil {
		before = parseShortcutsList(raw)
	}
	fmt.Fprintf(stdout, "shortcuts: opening Shortcuts -- click %s to install %q\n", out.Bold("\"Add Shortcut\""), name)
	if err := openCmd(ctx, path); err != nil {
		return false, fmt.Errorf("open: %w", err)
	}
	fmt.Fprintf(stdout, "shortcuts: waiting for you to add it (up to %s) ", shortcutsPollTimeout)
	res, err := waitForNewShortcut(ctx, name, ShortcutFormatVersion, before, stdout, out)
	if err != nil {
		return false, err
	}
	switch {
	case res.installed:
		fmt.Fprintln(stdout, out.OK(fmt.Sprintf("shortcuts: %q installed (v%d)", name, ShortcutFormatVersion)))
		return true, nil
	case res.unreadable != nil:
		fmt.Fprintln(stderr, errp.Warn(fmt.Sprintf("shortcuts: %q was added, but it didn't answer its version check (%v), so shelp stopped asking (each failed run posts a notification). Delete it in the Shortcuts app and run `shelp setup shortcuts` again.", name, res.unreadable)))
	default:
		fmt.Fprintln(stderr, errp.Warn(fmt.Sprintf("shortcuts: timed out waiting for %q; run `shelp setup shortcuts` again once it's added", name)))
	}
	return false, nil
}

// runShortcutsSetup is the "shortcuts" SetupStep. It installs or updates
// the one shortcut every shortcuts:* provider runs:
//
//   - missing: generate, sign, open (user clicks "Add Shortcut"), wait for
//     it to be listed once and answer the current version marker.
//   - outdated (installedShortcutVersion is older than
//     ShortcutFormatVersion): generate and sign first, then ask the user to
//     delete the old one in the Shortcuts app and poll `shortcuts list` --
//     running nothing -- until it's gone; then open the new one, which
//     imports with a plain "Add Shortcut" since the name is free, and wait
//     as for a missing one (which still copes with "Replace"/"Keep Both"
//     if the name turns out to be taken after all). Deleting first is the
//     only way to end with one shortcut under the name: importing over it
//     keeps both, whichever button the user picks.
//   - current or newer: skip with a message.
//   - unknown version (the check itself failed: Shortcuts Events
//     unavailable, a broken format that fails every run): left alone,
//     with a note to delete it by hand and run setup again.
//
// Before any of that it lists, by name and never running them, the
// per-model shortcuts formats 1-4 installed (legacyShortcuts) and any
// "<name> N" copies an earlier update left, so the user can delete them.
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
	name := sc.shortcutName()
	out, errp := style.For(stdout), style.For(stderr)

	rawList, err := shortcutsListCmd(ctx)
	if err != nil {
		return fmt.Errorf("shortcuts list: %w", err)
	}
	installed := parseShortcutsList(rawList)

	// Listed by name only, never run: an old one may be a broken format
	// that fails (and posts a notification) on every run.
	if legacy := legacyShortcuts(installed, name); len(legacy) > 0 {
		fmt.Fprintln(stdout, out.Warn(fmt.Sprintf("shortcuts: older shelps made one shortcut per model; this one uses a single shortcut, %q, for every shortcuts:* provider. No longer used, so delete them in the Shortcuts app (right-click › Delete): %s", name, quoteAll(legacy))))
	}
	if copies := conflictCopies(installed, name); len(copies) > 0 {
		fmt.Fprintln(stdout, out.Warn(fmt.Sprintf("shortcuts: %s look(s) like a copy left by an earlier update; delete it in the Shortcuts app if you don't use it", quoteAll(copies))))
	}

	signingNotice := func(why string) {
		fmt.Fprintf(stdout, "shortcuts: %s, so signing one with Apple (a round trip to Apple's server)...\n", why)
	}
	signNotice = func(attempt, total int, reason string) {
		fmt.Fprintln(stdout, out.Warn(fmt.Sprintf("shortcuts: signing attempt %d of %d failed (%s); retrying...", attempt, total, reason)))
	}
	defer func() { signNotice = nil }()

	tmpDir, err := os.MkdirTemp("", "shelp-shortcuts-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	current := false
	switch {
	case !installed[name]:
		fmt.Fprintf(stdout, "shortcuts: preparing %q...\n", name)
		path, err := prepareShortcut(ctx, tmpDir, name, signingNotice)
		if err != nil {
			return fmt.Errorf("generate: %w", err)
		}
		if current, err = addShortcut(ctx, name, path, stdout, stderr); err != nil {
			return err
		}
	default:
		// An unreadable version is left alone rather than replaced: the
		// update flow ends by polling the version, and polling a shortcut
		// that fails every run (a v2) would post a notification every poll.
		have, verr := installedShortcutVersion(ctx, name)
		switch {
		case verr != nil:
			fmt.Fprintln(stdout, out.Warn(fmt.Sprintf("shortcuts: %q installed, couldn't check its version (%v); leaving it alone -- if it's a broken shelp shortcut, delete it in Shortcuts and run `shelp setup shortcuts` again", name, verr)))
		case have == ShortcutFormatVersion:
			fmt.Fprintln(stdout, out.OK(fmt.Sprintf("shortcuts: %q already up to date (v%d), skipping", name, ShortcutFormatVersion)))
			current = true
		case have > ShortcutFormatVersion:
			fmt.Fprintln(stdout, out.Warn(fmt.Sprintf("shortcuts: %q is v%d, newer than this shelp (v%d); leaving it alone", name, have, ShortcutFormatVersion)))
			current = true
		default:
			fmt.Fprintln(stdout, out.Warn(fmt.Sprintf("shortcuts: %q is outdated (v%d, current v%d); preparing the new one...", name, have, ShortcutFormatVersion)))
			// Generate (and sign) before asking for the deletion: a signing
			// failure must not cost the user their working shortcut.
			path, err := prepareShortcut(ctx, tmpDir, name, signingNotice)
			if err != nil {
				return fmt.Errorf("generate: %w", err)
			}
			fmt.Fprintf(stdout, "shortcuts: Shortcuts can't update a shortcut in place, so delete the old one first: in the Shortcuts app, right-click %q › %s. shelp then opens the new one for you to add.\n", name, out.Bold("Delete"))
			fmt.Fprintf(stdout, "shortcuts: waiting for %q to be deleted (up to %s) ", name, shortcutsPollTimeout)
			gone, err := waitForShortcutGone(ctx, name, stdout)
			fmt.Fprintln(stdout)
			if err != nil {
				return err
			}
			if !gone {
				fmt.Fprintln(stderr, errp.Warn(fmt.Sprintf("shortcuts: timed out waiting for %q to be deleted; nothing changed -- run `shelp setup shortcuts` again when you're ready", name)))
				break
			}
			if current, err = addShortcut(ctx, name, path, stdout, stderr); err != nil {
				return err
			}
		}
	}

	if current {
		ids := make([]string, len(shortcutVariants))
		for i, v := range shortcutVariants {
			ids[i] = v.id
		}
		fmt.Fprintf(stdout, "shortcuts: %q serves %s; the chatgpt ones also need %s\n", name, strings.Join(ids, ", "), chatGPTExtension)
	}
	return nil
}

func init() {
	RegisterSetup(SetupStep{
		Name: "shortcuts",
		Help: "install shelp's Apple Shortcut for the shortcuts:* providers (opens Shortcuts; you click Add Shortcut)",
		Run:  runShortcutsSetup,
	})
	RegisterNotes(legacyShortcutsNotes)
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

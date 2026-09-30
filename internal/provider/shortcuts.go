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
	return Status{Available: true, Version: p.variant.model}
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

// runShortcutsSetup is the "shortcuts" SetupStep: for each variant in the
// install list not already installed, it generates and signs a shortcut
// into a temp dir, opens it (the user must click "Add Shortcut"), and
// polls until it appears.
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

	for _, key := range wanted {
		v, ok := variantByKey(key)
		if !ok {
			fmt.Fprintf(stderr, "shortcuts: unknown variant %q in install list, skipping\n", key)
			continue
		}
		name := resolveName(v, sc)
		if installed[name] {
			fmt.Fprintf(stdout, "shortcuts: %s: %q already installed, skipping\n", v.id, name)
			continue
		}

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
		ok, err = waitForShortcut(ctx, name, stdout)
		fmt.Fprintln(stdout)
		if err != nil {
			return fmt.Errorf("%s: %w", v.id, err)
		}
		if !ok {
			fmt.Fprintf(stderr, "shortcuts: %s: timed out waiting for %q; run `shelp setup shortcuts` again once it's added\n", v.id, name)
			continue
		}
		fmt.Fprintf(stdout, "shortcuts: %s: %q installed\n", v.id, name)
		installed[name] = true
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

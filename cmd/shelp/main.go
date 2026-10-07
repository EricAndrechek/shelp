// Command shelp turns a task into a shell command, or explains one, using
// whichever model you have: Apple's on-device or cloud models, a local
// server, an API key, or an installed AI CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/clean"
	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/shellinit"
	"github.com/ericandrechek/shelp/internal/style"
)

var version = "dev"

const usage = `shelp: turn a task into a shell command, or explain one.

Usage:
  shelp [flags] <task...>              suggest a command (same as: shelp suggest)
  shelp explain [flags] <command...>   explain a command (alias: -e)
  shelp providers [--json] [--all]     list model providers and whether they work
  shelp providers --reset-cooldowns    clear all recorded rate-limit cooldowns
  shelp setup [shortcuts]              install optional pieces (Apple Shortcuts)
  shelp setup shell [--shell sh] [--bind KEY] [--dry-run]
                                        add shelp's hook to your shell rc file
  shelp init <zsh|bash|fish> [--bind KEY] [--no-line-mode]
                                        print shell integration; add to your rc:
                                         eval "$(shelp init zsh)"
  shelp version

Flags (suggest/explain):
  -p, --provider ids   provider or comma-separated fallback chain
                       (env SHELP_PROVIDER; config "providers")
  --shell name         target shell (default: from shell integration or $SHELL)
  --template name      prompt template (default: ` + prompt.Default + `)
  --context fields     what to tell the model: os,shell,cwd,tools (default: os,shell)
  --json               print {"text","provider","latency_ms"} instead of text
  -v, --verbose        report provider and latency on stderr
  --force              call a provider even if it's in a rate-limit cooldown

Flags (init):
  --bind KEY           bind shelp-widget to KEY (caret notation "^G", or a
                       shell-native escape form); also config "[shell] bind"
  --no-line-mode       disable raw-line Enter interception (default: on);
                       also config "[shell] line_mode = false"

Flags (setup shell):
  --shell sh           zsh, bash, or fish (default: from $SHELL)
  --bind KEY           keybinding to pass through to "shelp init ... --bind"
  --dry-run            print what would change without writing it
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, style.For(os.Stderr).Err("shelp:"), err)
		var ue usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type usageError struct{ error }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return usageError{errors.New("no task given")}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	case "version", "--version":
		fmt.Fprintln(stdout, "shelp", version)
		return nil
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "providers":
		return runProviders(ctx, args[1:], stdout)
	case "setup":
		return runSetup(ctx, args[1:], stdout, stderr)
	case "explain", "-e":
		return runGenerate(ctx, provider.Explain, args[1:], stdout, stderr)
	case "suggest":
		return runGenerate(ctx, provider.Suggest, args[1:], stdout, stderr)
	default:
		return runGenerate(ctx, provider.Suggest, args, stdout, stderr)
	}
}

func loadConfig() (*config.Config, error) { return config.LoadDefault() }

// runInit handles `shelp init <shell> [--bind KEY] [--no-line-mode]`. It
// runs at every shell start (via `eval "$(shelp init zsh)"`), so it stays
// cheap: a config read (file I/O only, no probing) and a template render.
func runInit(args []string, stdout, stderr io.Writer) error {
	usage := fmt.Sprintf("usage: shelp init <%s> [--bind KEY] [--no-line-mode]", strings.Join(shellinit.Shells(), "|"))
	var shellName, bind string
	var noLineMode bool
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--bind":
			i++
			if i >= len(args) {
				return usageError{errors.New("--bind needs a value, e.g. --bind '^G'")}
			}
			bind = args[i]
		case strings.HasPrefix(a, "--bind="):
			bind = strings.TrimPrefix(a, "--bind=")
		case a == "--no-line-mode":
			noLineMode = true
		case a == "-h" || a == "--help":
			fmt.Fprintln(stdout, usage)
			return nil
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usageError{errors.New(usage)}
	}
	shellName = pos[0]

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var shCfg shellinit.Config
	_ = cfg.Section("shell", &shCfg) // malformed config: fall back to defaults

	opts := shellinit.Options{LineMode: shCfg.LineModeEnabled(noLineMode)}
	opts.Bind = bind
	if opts.Bind == "" {
		opts.Bind = shCfg.Bind
	}
	s, err := shellinit.Render(shellName, opts)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, s)
	return nil
}

func runGenerate(ctx context.Context, mode provider.Mode, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(string(mode), flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	var provFlag, shell, tmpl, ctxFields string
	var asJSON, verbose, force bool
	fs.StringVar(&provFlag, "p", "", "")
	fs.StringVar(&provFlag, "provider", "", "")
	fs.StringVar(&shell, "shell", "", "")
	fs.StringVar(&tmpl, "template", "", "")
	fs.StringVar(&ctxFields, "context", "", "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&verbose, "v", false, "")
	fs.BoolVar(&verbose, "verbose", false, "")
	fs.BoolVar(&force, "force", false, "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		return usageError{fmt.Errorf("usage: shelp %s <%s>", mode, map[provider.Mode]string{provider.Suggest: "task", provider.Explain: "command"}[mode])}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if tmpl == "" {
		tmpl = cfg.Template
	}
	t, err := prompt.Get(tmpl)
	if err != nil {
		return err
	}
	fields := prompt.DefaultContext
	if len(cfg.Context) > 0 {
		fields = cfg.Context
	}
	if ctxFields != "" {
		fields = splitList(ctxFields)
	}
	req := prompt.Render(t, mode, input, prompt.Detect(fields, shell))

	chain := chainFor(provFlag, cfg)
	// Explain output streams straight to the terminal when possible.
	var streamed bool
	// On a terminal, explain gets a header and a highlighter; otherwise the
	// text goes out untouched, byte for byte.
	var hl *explainHighlighter
	if mode == provider.Explain && !asJSON && style.For(stdout).On() {
		hl = newExplainHighlighter(stdout, style.For(stdout), input)
	}
	if mode == provider.Explain && !asJSON {
		req.OnChunk = func(s string) {
			streamed = true
			if hl != nil {
				hl.Write(s)
				return
			}
			fmt.Fprint(stdout, s)
		}
	}
	errPal := style.For(stderr)
	res, err := provider.Chain(ctx, cfg, chain, req, force, func(id string, err error) {
		if !verbose {
			return
		}
		var cse *provider.CooldownSkippedError
		if errors.As(err, &cse) {
			// The error text already ends in "; skipping" -- no need for
			// the generic "; trying next provider" suffix below, and the
			// provider was never actually called.
			fmt.Fprintln(stderr, errPal.Dim(fmt.Sprintf("shelp: %v", err)))
			return
		}
		fmt.Fprintln(stderr, errPal.Dim(fmt.Sprintf("shelp: %v; trying next provider", err)))
	})
	if err != nil {
		return err
	}
	// Best-effort, once-per-shelp-version hint that an installed Apple
	// Shortcuts wrapper is outdated (e.g. after `brew upgrade shelp`).
	// Deferred so it runs after every print below, never before -- see
	// CheckShortcutsFreshness's doc comment for the full guarantee (bounded
	// timeout, never blocks/fails the real command).
	defer provider.CheckShortcutsFreshness(ctx, cfg, version, chain, stderr)
	text := clean.Text(res.Text)
	if mode == provider.Suggest {
		text = clean.Command(res.Text)
	}
	if verbose {
		fmt.Fprintln(stderr, errPal.Dim(fmt.Sprintf("shelp: %s in %s", res.ProviderID, res.Latency.Round(10*time.Millisecond))))
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{
			"text": text, "provider": res.ProviderID, "latency_ms": res.Latency.Milliseconds(),
			"first_chunk_ms": res.FirstChunk.Milliseconds(),
		})
	}
	if hl != nil {
		if !streamed {
			hl.Write(text + "\n")
		}
		hl.Flush()
		if streamed && !strings.HasSuffix(res.Text, "\n") {
			fmt.Fprintln(stdout)
		}
		return nil
	}
	if streamed {
		if !strings.HasSuffix(res.Text, "\n") {
			fmt.Fprintln(stdout)
		}
		return nil
	}
	fmt.Fprintln(stdout, text)
	return nil
}

// chainFor resolves the provider chain: flag, then $SHELP_PROVIDER, then
// config, then the built-in default.
func chainFor(flagVal string, cfg *config.Config) []string {
	switch {
	case flagVal != "":
		return splitList(flagVal)
	case os.Getenv("SHELP_PROVIDER") != "":
		return splitList(os.Getenv("SHELP_PROVIDER"))
	case len(cfg.Providers) > 0:
		return cfg.Providers
	}
	return provider.DefaultChain
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func runProviders(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	resetCooldowns := fs.Bool("reset-cooldowns", false, "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if *resetCooldowns {
		if err := cooldown.Reset(); err != nil {
			return fmt.Errorf("reset cooldowns: %w", err)
		}
		fmt.Fprintln(stdout, "shelp: cooldowns reset")
		return nil
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cds := cooldown.Load()
	ps := provider.All(cfg)
	sts := make([]provider.Status, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		// A cooled-down provider is reported as such without probing it --
		// same reasoning as the CLI itself never calling Detect: don't
		// touch a backend that just told us it's rate-limited.
		if _, ok := cooldown.Active(cds, p.ID()); ok {
			continue
		}
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			sts[i] = p.Detect(c)
		}(i, p)
	}
	wg.Wait()

	type row struct {
		ID, Kind, Description, Status, Reason, Version string
		Available                                      bool
	}
	rows := make([]row, len(ps))
	for i, p := range ps {
		if e, ok := cooldown.Active(cds, p.ID()); ok {
			rows[i] = row{
				ID: p.ID(), Kind: string(p.Kind()), Description: p.Description(),
				Status: "cooldown",
				Reason: fmt.Sprintf("until %s: %s", e.Until.Local().Format("15:04"), e.Reason),
			}
			continue
		}
		st := "ok"
		reason := p.Description()
		if !sts[i].Available {
			st, reason = "unavailable", sts[i].Reason
		} else if sts[i].Reason != "" {
			// Available but Detect still has something to say (e.g.
			// shortcuts.go's outdated-shortcut note): show it alongside the
			// description rather than dropping it, since an "ok" row
			// otherwise only ever prints Description.
			reason = fmt.Sprintf("%s -- %s", p.Description(), sts[i].Reason)
		}
		rows[i] = row{
			ID: p.ID(), Kind: string(p.Kind()), Description: p.Description(),
			Status: st, Reason: reason, Version: sts[i].Version, Available: sts[i].Available,
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	pal := style.For(stdout)
	table := [][]string{providerHeader(pal)}
	for _, r := range rows {
		table = append(table, providerRow(pal, r.ID, r.Kind, r.Status, r.Reason))
	}
	return writeTable(stdout, table)
}

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// "shell" takes its own flags (--shell/--bind/--dry-run), unlike the
	// registered provider.SetupStep steps below (which take none), so it's
	// handled as its own subcommand rather than a step.
	if len(args) > 0 && args[0] == "shell" {
		return runSetupShell(ctx, args[1:], stdout)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	steps := provider.SetupSteps()
	if len(steps) == 0 {
		fmt.Fprintln(stdout, "shelp setup: nothing to set up on this system")
		return nil
	}
	ran := false
	for _, s := range steps {
		if len(args) > 0 && args[0] != s.Name {
			continue
		}
		ran = true
		fmt.Fprintln(stdout, style.For(stdout).Bold(style.For(stdout).Accent(fmt.Sprintf("== %s: %s", s.Name, s.Help))))
		if err := s.Run(ctx, cfg, stdout, stderr); err != nil {
			return fmt.Errorf("setup %s: %w", s.Name, err)
		}
	}
	if !ran {
		return usageError{fmt.Errorf("unknown setup step %q", args[0])}
	}
	return nil
}

// runSetupShell handles `shelp setup shell [--shell sh] [--bind KEY]
// [--dry-run]`: it appends (or updates) shelp's hook in the target
// shell's rc file. See internal/shellinit.Setup for the mechanics
// (idempotency, chezmoi/symlink detection, the macOS bash_profile note).
func runSetupShell(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup shell", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var shell, bind string
	var dryRun bool
	fs.StringVar(&shell, "shell", "", "")
	fs.StringVar(&bind, "bind", "", "")
	fs.BoolVar(&dryRun, "dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return usageError{fmt.Errorf("usage: shelp setup shell [--shell zsh|bash|fish] [--bind KEY] [--dry-run]: %w", err)}
	}
	if fs.NArg() > 0 {
		return usageError{errors.New("usage: shelp setup shell [--shell zsh|bash|fish] [--bind KEY] [--dry-run]")}
	}
	res, err := shellinit.Setup(ctx, shellinit.SetupOptions{Shell: shell, Bind: bind, DryRun: dryRun})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, setupShellMessage(style.For(stdout), res, dryRun))
	return nil
}

// setupShellMessage colors `shelp setup shell`'s summary by outcome.
func setupShellMessage(p style.Palette, res shellinit.SetupResult, dryRun bool) string {
	switch {
	case dryRun && (res.Action == "added" || res.Action == "updated"):
		return p.Dim(res.Message)
	case res.Action == "manual" || res.Action == "already-manual":
		return p.Warn(res.Message)
	}
	return p.OK(res.Message)
}

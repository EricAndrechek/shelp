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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		code, show := exitCode(ctx, err)
		if show {
			fmt.Fprintln(os.Stderr, style.For(os.Stderr).Err("shelp:"), err)
		}
		os.Exit(code)
	}
}

// exitCode decides how a failed run ends. Ctrl-C cancels ctx, and whatever
// error the provider then returns ("could not reach ...: interrupt signal
// received") is just the interruption echoed back, so an interrupted run
// exits 130 (128 + SIGINT, as shells report it) without a message -- the
// shell integrations show nothing for it either. A usage error exits 2.
func exitCode(ctx context.Context, err error) (code int, show bool) {
	if ctx.Err() != nil {
		return 130, false
	}
	var ue usageError
	if errors.As(err, &ue) {
		return 2, true
	}
	return 1, true
}

type usageError struct{ error }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout, "")
		return nil
	}
	rest := args[1:]
	switch args[0] {
	case "-h", "--help":
		printHelp(stdout, "")
		return nil
	case "help":
		// Bare "help" is help, and so is "help <command>". Anything else
		// ("shelp help me find big files") is a task.
		if len(rest) == 0 {
			printHelp(stdout, "")
			return nil
		}
		if len(rest) == 1 {
			if _, ok := helpTopic(rest[0]); ok && rest[0] != "" {
				printHelp(stdout, rest[0])
				return nil
			}
			if rest[0] == "-h" || rest[0] == "--help" {
				printHelp(stdout, "help")
				return nil
			}
		}
	case "version", "--version":
		// Likewise "shelp version of node installed" is a task.
		if len(rest) == 0 {
			printVersion(stdout)
			return nil
		}
		if len(rest) == 1 && (rest[0] == "-h" || rest[0] == "--help") {
			printHelp(stdout, "version")
			return nil
		}
	case "init":
		return runInit(rest, stdout, stderr)
	case "completion":
		return runCompletion(rest, stdout, stderr)
	case "__complete":
		return runComplete(rest, stdout)
	case "providers":
		return runProviders(ctx, rest, stdout, stderr)
	case "setup":
		return runSetup(ctx, rest, stdout, stderr)
	case "config":
		// Likewise "shelp config of nginx" is a task.
		if len(rest) == 0 || rest[0] == "chain" || rest[0] == "edit" || rest[0] == "-h" || rest[0] == "--help" {
			return runConfig(ctx, rest, stdout, stderr)
		}
	case "explain", "-e":
		return runGenerate(ctx, provider.Explain, rest, stdout, stderr)
	case "suggest":
		return runGenerate(ctx, provider.Suggest, rest, stdout, stderr)
	}
	return runGenerate(ctx, provider.Suggest, args, stdout, stderr)
}

// flagErr ends a command whose flags did not parse: its help goes to
// stderr, and the error exits 2. -h/--help (flag.ErrHelp) is not an error:
// the help goes to stdout.
func flagErr(err error, name string, stdout, stderr io.Writer) error {
	if errors.Is(err, flag.ErrHelp) {
		printHelp(stdout, name)
		return nil
	}
	printHelp(stderr, name)
	return usageError{err}
}

func loadConfig() (*config.Config, error) { return config.LoadDefault() }

// runInit handles `shelp init <shell> [--bind KEY] [--no-line-mode]`. It
// runs at every shell start (via `eval "$(shelp init zsh)"`), so it stays
// cheap: a config read (file I/O only, no probing) and a template render.
func runInit(args []string, stdout, stderr io.Writer) error {
	var shellName, bind string
	var noLineMode bool
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--bind":
			i++
			if i >= len(args) {
				printHelp(stderr, "init")
				return usageError{errors.New("--bind needs a value, e.g. --bind '^G'")}
			}
			bind = args[i]
		case strings.HasPrefix(a, "--bind="):
			bind = strings.TrimPrefix(a, "--bind=")
		case a == "--no-line-mode":
			noLineMode = true
		case a == "-h" || a == "--help":
			printHelp(stdout, "init")
			return nil
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		printHelp(stderr, "init")
		return usageError{fmt.Errorf("init needs one shell: %s", strings.Join(shellinit.Shells(), ", "))}
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
	fs.SetOutput(io.Discard)
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
	helpName := string(mode)
	if err := fs.Parse(args); err != nil {
		return flagErr(err, helpName, stdout, stderr)
	}
	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		printHelp(stderr, helpName)
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
		hl = newExplainHighlighter(stdout, style.For(stdout), input, style.TermWidth(stdout))
	}
	// The thinking spinner (stderr, terminal only) must be stopped before
	// anything else is written; spMu guards it because OnChunk and the
	// fallback callback may run on provider goroutines.
	var spMu sync.Mutex
	var sp *style.Spinner
	stopSpinner := func() {
		spMu.Lock()
		defer spMu.Unlock()
		sp.Stop()
		sp = nil
	}
	if !asJSON {
		sp = style.StartSpinner(stderr, "thinking…")
	}
	defer stopSpinner()
	if mode == provider.Explain && !asJSON {
		req.OnChunk = func(s string) {
			stopSpinner()
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
		stopSpinner()
		if !asJSON {
			// Keep animating while the next provider runs.
			defer func() {
				spMu.Lock()
				sp = style.StartSpinner(stderr, "thinking…")
				spMu.Unlock()
			}()
		}
		var cse *provider.CooldownSkippedError
		var ose *provider.OfflineSkippedError
		if errors.As(err, &cse) || errors.As(err, &ose) {
			// The error text already ends in "; skipping" -- no need for
			// the generic "; trying next provider" suffix below, and the
			// provider was never actually called.
			fmt.Fprintln(stderr, errPal.Dim(fmt.Sprintf("shelp: %v", err)))
			return
		}
		fmt.Fprintln(stderr, errPal.Dim(fmt.Sprintf("shelp: %v; trying next provider", err)))
	})
	stopSpinner()
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
		// Flush ends the output with exactly one newline.
		hl.Flush()
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

func runProviders(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	resetCooldowns := fs.Bool("reset-cooldowns", false, "")
	all := fs.Bool("all", false, "")
	fs.BoolVar(all, "verbose", false, "")
	fs.BoolVar(all, "v", false, "")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "providers", stdout, stderr)
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
	sts := probeProviders(ctx, ps, cds)

	rows := buildProviderRows(ps, sts, cds)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	pal := style.For(stdout)
	if _, err = io.WriteString(stdout, renderProviders(pal, rows, chainFor("", cfg), chainSource(cfg), *all, style.TermWidth(stdout), provider.Offline())); err != nil {
		return err
	}
	if notes := provider.Notes(ctx, cfg); len(notes) > 0 {
		fmt.Fprintln(stdout)
		for _, n := range notes {
			fmt.Fprintln(stdout, pal.Warn("note: "+n))
		}
	}
	return nil
}

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 && isInteractive(stdout) {
		return runGuidedSetup(ctx, stdout, stderr)
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp(stdout, "setup")
		return nil
	}
	// "shell" takes its own flags (--shell/--bind/--dry-run), unlike the
	// registered provider.SetupStep steps below (which take none), so it's
	// handled as its own subcommand rather than a step.
	if args[0] == "shell" {
		return runSetupShell(ctx, args[1:], stdout, stderr)
	}
	for _, s := range provider.SetupSteps() {
		if s.Name != args[0] {
			continue
		}
		if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
			printHelp(stdout, "setup")
			return nil
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if err := s.Run(ctx, cfg, stdout, stderr); err != nil {
			return fmt.Errorf("setup %s: %w", s.Name, err)
		}
		return nil
	}
	printHelp(stderr, "setup")
	return usageError{fmt.Errorf("unknown setup target %q", args[0])}
}

// runSetupShell handles `shelp setup shell [--shell sh] [--bind KEY]
// [--dry-run]`: it appends (or updates) shelp's hook in the target
// shell's rc file. See internal/shellinit.Setup for the mechanics
// (idempotency, chezmoi/symlink detection, the macOS bash_profile note).
func runSetupShell(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("setup shell", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var shell, bind string
	var dryRun bool
	fs.StringVar(&shell, "shell", "", "")
	fs.StringVar(&bind, "bind", "", "")
	fs.BoolVar(&dryRun, "dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "setup", stdout, stderr)
	}
	if fs.NArg() > 0 {
		printHelp(stderr, "setup")
		return usageError{fmt.Errorf("setup shell takes no arguments, got %q", fs.Arg(0))}
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

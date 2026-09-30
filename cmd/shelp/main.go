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
	"text/tabwriter"
	"time"

	"github.com/ericandrechek/shelp/internal/clean"
	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/shellinit"
)

var version = "dev"

const usage = `shelp: turn a task into a shell command, or explain one.

Usage:
  shelp [flags] <task...>              suggest a command (same as: shelp suggest)
  shelp explain [flags] <command...>   explain a command (alias: -e)
  shelp providers [--json] [--all]     list model providers and whether they work
  shelp providers --reset-cooldowns    clear all recorded rate-limit cooldowns
  shelp setup [shortcuts]              install optional pieces (Apple Shortcuts)
  shelp init <zsh|bash|fish>           print shell integration; add to your rc:
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
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "shelp:", err)
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
		if len(args) != 2 {
			return usageError{fmt.Errorf("usage: shelp init <%s>", strings.Join(shellinit.Shells(), "|"))}
		}
		s, err := shellinit.Script(args[1])
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, s)
		return nil
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

func loadConfig() (*config.Config, error) { return config.Load(config.Path()) }

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
	if mode == provider.Explain && !asJSON {
		req.OnChunk = func(s string) { streamed = true; fmt.Fprint(stdout, s) }
	}
	res, err := provider.Chain(ctx, cfg, chain, req, force, func(id string, err error) {
		if !verbose {
			return
		}
		var cse *provider.CooldownSkippedError
		if errors.As(err, &cse) {
			// The error text already ends in "; skipping" -- no need for
			// the generic "; trying next provider" suffix below, and the
			// provider was never actually called.
			fmt.Fprintf(stderr, "shelp: %v\n", err)
			return
		}
		fmt.Fprintf(stderr, "shelp: %v; trying next provider\n", err)
	})
	if err != nil {
		return err
	}
	text := clean.Text(res.Text)
	if mode == provider.Suggest {
		text = clean.Command(res.Text)
	}
	if verbose {
		fmt.Fprintf(stderr, "shelp: %s in %s\n", res.ProviderID, res.Latency.Round(10*time.Millisecond))
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{
			"text": text, "provider": res.ProviderID, "latency_ms": res.Latency.Milliseconds(),
			"first_chunk_ms": res.FirstChunk.Milliseconds(),
		})
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
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tKIND\tSTATUS\tDETAILS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Kind, r.Status, r.Reason)
	}
	return tw.Flush()
}

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
		fmt.Fprintf(stdout, "== %s: %s\n", s.Name, s.Help)
		if err := s.Run(ctx, cfg, stdout, stderr); err != nil {
			return fmt.Errorf("setup %s: %w", s.Name, err)
		}
	}
	if !ran {
		return usageError{fmt.Errorf("unknown setup step %q", args[0])}
	}
	return nil
}

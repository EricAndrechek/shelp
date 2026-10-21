package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// screen is the terminal the manager draws on. Tests use a fake.
type screen interface {
	// Start enters raw mode and the alternate screen, hides the cursor and
	// starts delivering keys. Stop undoes all of it (and is idempotent),
	// so line output and prompts work in between.
	Start() error
	Stop()
	Size() (cols, rows int)
	Draw(frame string)
	Keys() <-chan term.Key
	Resized() <-chan os.Signal
	// WaitKey reads one key while the screen is stopped ("press any key").
	WaitKey(ctx context.Context) (term.Key, error)
	Close()
}

// openScreen opens the real terminal (stdin for keys, stdout for drawing).
var openScreen = func(stdout io.Writer) (screen, error) {
	out, ok := stdout.(*os.File)
	if !ok || !term.Supported {
		return nil, fmt.Errorf("the provider manager needs a terminal")
	}
	return newTTYScreen(os.Stdin, out), nil
}

// runManager is the interactive provider manager on a terminal; without
// one it prints the chain guide instead.
func runManager(ctx context.Context, stdout, stderr io.Writer) error {
	cfg, err := loadConfigFile()
	if err != nil {
		return err
	}
	if !isInteractive(stdout) {
		printChainGuide(cfg, stdout)
		return nil
	}
	path := config.Path()
	src, err := readConfigText(path)
	if err != nil {
		return err
	}
	m, err := newManager(style.For(stdout), path, src, listProviders)
	if err != nil {
		return err
	}
	_, m.managed = fileState(ctx, path)
	m.env = os.Getenv("SHELP_PROVIDER")
	m.offline = provider.Offline()
	scr, err := openScreen(stdout)
	if err != nil {
		return err
	}
	defer scr.Close()
	err = managerLoop(ctx, m, scr, stdout, stderr)
	if err != nil {
		return err
	}
	pal := style.For(stdout)
	switch {
	case m.dirty():
		fmt.Fprintln(stdout, pal.Dim("changes discarded"))
	case m.lastSaved != "":
		fmt.Fprintln(stdout, m.lastSaved)
	default:
		fmt.Fprintln(stdout, pal.Dim("no changes"))
	}
	return nil
}

// probeRow probes one provider into a row with probe (probeProviders,
// read by the caller: the goroutine running this may outlive the screen);
// a panic in a backend's Detect becomes the row's reason instead of a
// crash with the terminal raw.
func probeRow(ctx context.Context, probe func(context.Context, []provider.Provider, map[string]cooldown.Entry) []provider.Status, p provider.Provider, cds map[string]cooldown.Entry) (row providerRow) {
	defer func() {
		if r := recover(); r != nil {
			row = providerRow{ID: p.ID(), Kind: string(p.Kind()), Description: p.Description(), Status: statusUnavailable, Reason: fmt.Sprintf("check failed: %v", r)}
		}
	}()
	ps := []provider.Provider{p}
	return buildProviderRows(ps, probe(ctx, ps, cds), cds)[0]
}

// managerLoop runs m on scr until it quits. Probes run in the background
// and fill rows in as they finish; keys are never blocked on them. The
// terminal is restored on every way out: a return, a panic (the deferred
// Stop), SIGINT (main's context), SIGTERM/SIGHUP (caught here).
func managerLoop(ctx context.Context, m *manager, scr screen, stdout, stderr io.Writer) error {
	if err := scr.Start(); err != nil {
		return err
	}
	defer scr.Stop()
	sigs := make(chan os.Signal, 1)
	if qs := term.QuitSignals(); len(qs) > 0 {
		signal.Notify(sigs, qs...)
		defer signal.Stop(sigs)
	}
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cds := cooldown.Load()
	results := make(chan providerRow)
	probe := func(ids []string, fresh bool) {
		if fresh { // r: forget what the process memoized (the Shortcuts list)
			provider.ResetDetectCache()
		}
		// Fresh instances: some providers memoize Detect per instance.
		m.rebuildProviders()
		probeFn := probeProviders
		for _, id := range ids {
			p := m.provs[id]
			if p == nil {
				continue
			}
			go func() {
				row := probeRow(probeCtx, probeFn, p, cds)
				select {
				case results <- row:
				case <-probeCtx.Done():
				}
			}()
		}
	}
	// suspend leaves the screen for line output (a save to a file managed
	// elsewhere), then waits for a key.
	suspend := func(f func(pr *prompter) error) error {
		scr.Stop()
		pr := newPrompter(ctx, stdin, stdout)
		ferr := f(pr)
		if err := pr.err(); err != nil {
			return err
		}
		if ferr != nil {
			fmt.Fprintln(stdout, style.For(stdout).Err(ferr.Error()))
		}
		fmt.Fprint(stdout, "\n"+style.For(stdout).Dim("press any key to go back to the list"))
		k, err := scr.WaitKey(ctx)
		fmt.Fprintln(stdout)
		if err != nil {
			return err
		}
		if k.IsCtrl('c') {
			return errInterrupted
		}
		return scr.Start()
	}
	for {
		for _, e := range m.takeEffects() {
			switch e.kind {
			case "probe":
				probe(e.ids, e.fresh)
			case "setup":
				// The one thing that leaves the screen: Shortcuts opens and
				// waits for the user's click. Then straight back to the
				// details, which check the shortcut again.
				scr.Stop()
				pal := style.For(stdout)
				fmt.Fprintln(stdout, pal.Bold("Setting up the Apple Shortcut for "+e.ids[0])+pal.Dim(" · shelp providers comes back when it is done"))
				serr := runShortcutsStep(ctx, m.cfg, stdout, stderr)
				provider.ResetDetectCache()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := scr.Start(); err != nil {
					return err
				}
				probe(m.setupDone(serr), false)
			case "save":
				if err := saveFromManager(ctx, m, suspend, stdout, stderr); err != nil {
					return err
				}
			}
		}
		if m.quit {
			if m.code != 0 {
				return exitStatus{m.code}
			}
			return nil
		}
		w, h := scr.Size()
		scr.Draw(m.View(w, h))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s := <-sigs:
			return exitStatus{term.SignalExitCode(s)}
		case <-scr.Resized():
		case r := <-results:
			m.setProbe(r)
		case k, ok := <-scr.Keys():
			if !ok {
				return nil
			}
			m.Update(k)
		}
	}
}

// saveFromManager writes the manager's pending changes: in place when the
// file is shelp's to write (the result shows in the status line), else on
// the normal screen, where saveOps prints the change and may offer the
// chezmoi route.
func saveFromManager(ctx context.Context, m *manager, suspend func(func(*prompter) error) error, stdout, stderr io.Writer) error {
	ops := m.ops()
	summary := strings.Join(opsSummaryPlain(ops), "; ")
	if m.managed == "" {
		var buf bytes.Buffer
		res, err := saveOps(ctx, ops, nil, &buf, &buf)
		switch {
		case err != nil:
			m.saveFailed("not saved: " + err.Error())
		case res.saved:
			m.markSaved(res.src, summary+" in "+tildePath(m.path))
		default:
			m.saveFailed("not saved")
		}
		return nil
	}
	var res saveResult
	var serr error
	if err := suspend(func(pr *prompter) error {
		res, serr = saveOps(ctx, ops, pr, stdout, stderr)
		return serr
	}); err != nil {
		return err
	}
	switch {
	case serr != nil:
		m.saveFailed("not saved: " + serr.Error())
	case res.saved:
		src, _ := readConfigText(m.path) // what chezmoi apply wrote
		m.markSaved(src, summary+" via chezmoi")
	default:
		m.saveFailed("not saved: " + tildePath(m.path) + " is " + m.managed + "; make the change shown, then quit (q, y)")
	}
	return nil
}

func opsSummaryPlain(ops configOps) []string { return opsSummary(style.Plain(), ops) }

// rebuildProviders re-lists the working config's providers (new instances,
// same IDs), for a probe that must not reuse a memoized Detect.
func (m *manager) rebuildProviders() {
	for _, p := range m.list(m.cfg) {
		if _, ok := m.provs[p.ID()]; ok {
			m.provs[p.ID()] = p
		}
	}
}

// fixLines explains how to make row r work, for the detail page's "To
// make it work". It reuses the provider's Fix where it has one and adds
// what only the manager knows: how to start a local server, where a key
// variable goes, the exact command to run.
func fixLines(p style.Palette, r *mgrRow) []string {
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	switch provider.Kind(r.Kind) {
	case provider.OnDevice:
		switch cmd := backticked(r.Fix); {
		case strings.HasPrefix(cmd, "sudo "):
			add("  Accept Apple's terms for the on-device model, once per Mac. In a terminal, run:",
				"      "+p.Bold(cmd),
				p.Dim("  shelp never runs sudo for you."))
		case r.Fix != "":
			add("  To use Apple's on-device model: " + r.Fix + ".")
		}
		if strings.Contains(r.Fix, "Apple Intelligence") {
			add(p.Dim("  System Settings › Apple Intelligence & Siri; the model downloads in the background after that."))
		}
	case provider.AgentCLI:
		if r.Fix != "" {
			add("  " + strings.ToUpper(r.Fix[:1]) + r.Fix[1:] + ".")
		}
		if strings.Contains(r.Fix, "install") {
			add(p.Dim("  Claude Code: https://claude.com/claude-code"))
		}
		add(p.Dim("  shelp runs it headless with no tools; it counts against your Claude plan."))
	case provider.LocalServer:
		add("  "+localStartHint(r), p.Dim("  Another address or port? Add it as a local server: a in the list, or `shelp config add local NAME --base-url URL`."))
	case provider.API:
		if r.hasRoute && r.route.KeyEnv != "" {
			for _, l := range keyEnvLines(p, r.route.KeyEnv) {
				add("  " + l)
			}
			add(p.Dim("  Then restart shelp (this list) from that shell."))
		} else if r.Fix != "" {
			add("  " + r.Fix)
		}
	case provider.AppleCloud:
		if r.Fix != "" {
			add("  " + strings.ToUpper(r.Fix[:1]) + r.Fix[1:] + ".")
		}
	default:
		if r.Fix != "" {
			add("  " + r.Fix)
		}
	}
	if r.Detail != "" {
		add("", p.Dim("  ("+r.Detail+")"))
	}
	return out
}

// backticked is the first `command` in s ("" if none).
func backticked(s string) string {
	_, rest, ok := strings.Cut(s, "`")
	if !ok {
		return ""
	}
	cmd, _, ok := strings.Cut(rest, "`")
	if !ok {
		return ""
	}
	return cmd
}

// localStartHint says how to start a local server shelp looks for.
func localStartHint(r *mgrRow) string {
	name, url := r.ID, ""
	if r.hasRoute {
		name, url = r.route.Name, r.route.BaseURL
	}
	host := hostOfURL(url)
	switch {
	case r.hasRoute && r.route.Builtin && !r.route.Configured:
		switch name {
		case "lmstudio":
			return "Start LM Studio's server: LM Studio › Developer › Start Server (it listens on " + host + "), with a model loaded."
		case "ollama":
			return "Start Ollama: open the Ollama app, or run `ollama serve` (it listens on " + host + ")."
		case "llamacpp":
			return "Start llama.cpp's server: `llama-server -m <model>.gguf --port 8080` (it listens on " + host + ")."
		}
	}
	if url == "" {
		return "Start the server."
	}
	return "Start the OpenAI-compatible server at " + url + "; shelp asks its /models for what it serves."
}

// runProvidersInteractive is `shelp providers -i`: the manager, which
// needs a terminal.
func runProvidersInteractive(ctx context.Context, stdout, stderr io.Writer) error {
	if !isInteractive(stdout) {
		return usageError{fmt.Errorf("providers -i needs a terminal; without one use `shelp config chain ID,ID`, `shelp config add ...`, `shelp config remove NAME` or `shelp config edit` (`shelp config chain` lists the IDs)")}
	}
	return runManagerFn(ctx, stdout, stderr)
}

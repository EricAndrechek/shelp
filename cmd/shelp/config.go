package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/shellinit"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// Seams: tests replace these so nothing reaches a real backend, rc file,
// editor, Shortcut or terminal.
var (
	// stdin is what interactive prompts read.
	stdin io.Reader = os.Stdin
	// isInteractive reports whether prompts make sense: stdout and stdin
	// are both terminals.
	isInteractive = func(stdout io.Writer) bool {
		return style.IsTerminal(stdout) && style.IsTerminal(os.Stdin)
	}
	listProviders = provider.All
	// probeProviders runs Detect on ps (skipping cooled-down ones).
	probeProviders = realProbe
	// runEditor runs argv (editor + file) attached to the terminal.
	runEditor = func(ctx context.Context, argv []string) error {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	// Guided setup's pieces.
	setupGOOS        = runtime.GOOS
	hookInstalledFn  = shellinit.HookInstalled
	runShellSetupFn  = runSetupShell
	shortcutsNeedFn  = shortcutsNeedSetup
	runShortcutsStep = runShortcutsSetupStep
	// runManagerFn opens the interactive provider manager (manager_run.go).
	runManagerFn = runManager
)

// realProbe Detects every provider concurrently with a 10s limit each. A
// cooled-down provider is not probed (same reasoning as `shelp providers`:
// don't touch a backend that just said it is rate-limited).
func realProbe(ctx context.Context, ps []provider.Provider, cds map[string]cooldown.Entry) []provider.Status {
	sts := make([]provider.Status, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
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
	return sts
}

// loadConfigFile loads the file at config.Path(); unlike loadConfig, a
// missing file is fine even for $SHELP_CONFIG (config edit/chain create it).
func loadConfigFile() (*config.Config, error) { return config.Load(config.Path()) }

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return p
}

// fileState describes the config file: "missing", "exists", or why it
// should not be written directly (managedBy != "").
func fileState(ctx context.Context, path string) (state, managedBy string) {
	managedBy = shellinit.ManagedElsewhere(ctx, path)
	_, err := os.Lstat(path)
	switch {
	case err != nil:
		state = "missing"
	default:
		state = "exists"
	}
	return state, managedBy
}

// runConfig handles `shelp config [chain|edit|add|remove]`.
func runConfig(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return showConfig(ctx, stdout)
	}
	switch args[0] {
	case "-h", "--help":
		printHelp(stdout, "config")
		return nil
	case "chain":
		return runConfigChain(ctx, args[1:], stdout, stderr)
	case "edit":
		return runConfigEdit(ctx, args[1:], stdout, stderr)
	case "add":
		return runConfigAdd(ctx, args[1:], stdout, stderr)
	case "remove":
		return runConfigRemove(ctx, args[1:], stdout, stderr)
	}
	printHelp(stderr, "config")
	return usageError{fmt.Errorf("unknown config subcommand %q", args[0])}
}

// showConfig prints the file's path and status, then each effective
// setting and where it came from.
func showConfig(ctx context.Context, stdout io.Writer) error {
	cfg, err := loadConfigFile()
	if err != nil {
		return err
	}
	p := style.For(stdout)
	path := config.Path()
	state, managed := fileState(ctx, path)
	if managed != "" {
		state += ", " + managed
	}
	row := func(k, v, src string) {
		line := p.Dim(fmt.Sprintf("%-10s", k)) + " " + v
		if src != "" {
			line += "  " + p.Dim("("+src+")")
		}
		fmt.Fprintln(stdout, line)
	}
	row("file", p.Bold(tildePath(path)), state)
	if os.Getenv("SHELP_CONFIG") != "" {
		row("", p.Dim("path from $SHELP_CONFIG"), "")
	}
	fmt.Fprintln(stdout)
	row("chain", chainWithLabels(p, cfg), chainSource(cfg))
	if rs := provider.ConfiguredRoutes(cfg); len(rs) > 0 {
		var names []string
		for _, r := range rs {
			n := r.Name
			if r.Local {
				n += " (local)"
			}
			if r.Label != "" {
				n += " " + config.Quote(r.Label)
			}
			names = append(names, n)
		}
		row("routes", strings.Join(names, ", "), "config")
	}
	pick := func(v, def string) (string, string) {
		if v != "" {
			return v, "config"
		}
		return def, "default"
	}
	t, src := pick(cfg.Template, prompt.Default)
	row("template", t, src)
	ctxF, src := strings.Join(cfg.Context, ", "), "config"
	if len(cfg.Context) == 0 {
		ctxF, src = strings.Join(prompt.DefaultContext, ", "), "default"
	}
	row("context", ctxF, src)
	cd, src := pick(cfg.Cooldown, strings.TrimSuffix(cooldown.DefaultDuration.String(), "0s"))
	row("cooldown", cd, src)
	var sc struct {
		Name string `toml:"name"`
	}
	_ = cfg.Section("shortcuts", &sc)
	name, src := pick(strings.TrimSpace(sc.Name), "shelp")
	row("shortcut", name, src)
	var sh shellinit.Config
	_ = cfg.Section("shell", &sh)
	bind, src := pick(sh.Bind, "none")
	row("bind", bind, src)
	row("line mode", strconv.FormatBool(sh.LineModeEnabled(false)), map[bool]string{true: "config", false: "default"}[sh.LineMode != nil])
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, p.Dim("manage providers, routes and the chain: shelp providers -i (or shelp config chain)   edit the file: shelp config edit   all settings: shelp help config"))
	return nil
}

// chainWithLabels is the effective chain, each provider that has a label
// from the config followed by it.
func chainWithLabels(p style.Palette, cfg *config.Config) string {
	labels := map[string]string{}
	for _, pr := range listProviders(cfg) {
		if r, ok := provider.RouteOf(pr); ok && r.Label != "" {
			labels[pr.ID()] = r.Label
		}
	}
	ids := chainFor("", cfg)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id
		if l := labels[id]; l != "" {
			out[i] += " " + p.Dim("("+l+")")
		}
	}
	return strings.Join(out, " → ")
}

// ---- prompts ----

// prompter asks questions. On a terminal a yes/no question reads one key
// in raw mode, so Esc answers no and Ctrl-C quits; otherwise (tests, piped
// input) it reads lines from one shared bufio.Reader, since a second one
// would swallow the first's buffered input. Ctrl-C ends any prompt: the
// answer is no, and err() then returns errInterrupted (exit 130, no
// message) for the caller to return.
type prompter struct {
	ctx         context.Context
	in          *bufio.Reader
	out         io.Writer
	key         func(stop <-chan struct{}) (term.Key, bool, error) // nil: line mode
	interrupted bool
}

func newPrompter(ctx context.Context, in io.Reader, out io.Writer) *prompter {
	p := &prompter{ctx: ctx, in: bufio.NewReader(in), out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) && style.IsTerminal(out) {
		fd := int(f.Fd())
		p.key = func(stop <-chan struct{}) (term.Key, bool, error) {
			st, err := term.MakeRaw(fd)
			if err != nil {
				return term.Key{}, false, err
			}
			defer term.Restore(fd, st)
			return term.ReadKey(fd, stop)
		}
	}
	return p
}

// err is errInterrupted after Ctrl-C (or the context's error once it is
// done), else nil.
func (p *prompter) err() error {
	if p.interrupted {
		return errInterrupted
	}
	return p.ctx.Err()
}

// line prints q and reads one line (trimmed). ok is false at EOF with
// nothing read, on Ctrl-C (the context ends), and for an answer with an
// Esc in it.
func (p *prompter) line(q string) (s string, ok bool) {
	fmt.Fprint(p.out, q)
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := p.in.ReadString('\n')
		ch <- res{s, err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-p.ctx.Done():
		fmt.Fprintln(p.out)
		p.interrupted = true
		return "", false
	}
	if r.err != nil && r.s == "" {
		fmt.Fprintln(p.out)
		return "", false
	}
	if strings.ContainsRune(r.s, 0x1b) {
		return "\x1b", false // Esc: cancel
	}
	return strings.TrimSpace(r.s), true
}

// yesNo asks q with a [Y/n] or [y/N] suffix; Enter and EOF choose def,
// Esc and Ctrl-C answer no.
func (p *prompter) yesNo(q string, def bool) bool {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	if p.key != nil {
		return p.yesNoKey(q+suffix, def)
	}
	for {
		s, ok := p.line(q + suffix)
		if !ok {
			return def && !p.interrupted && s != "\x1b"
		}
		switch strings.ToLower(s) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

func (p *prompter) yesNoKey(q string, def bool) bool {
	fmt.Fprint(p.out, q)
	for {
		k, ok, err := p.key(p.ctx.Done())
		if !ok || err != nil {
			fmt.Fprintln(p.out)
			p.interrupted = p.interrupted || p.ctx.Err() != nil
			return false
		}
		ans := def
		switch {
		case k.IsCtrl('c'):
			fmt.Fprintln(p.out)
			p.interrupted = true
			return false
		case k.Code == term.KeyEsc, k.Is('n'), k.Is('N'):
			ans = false
		case k.Is('y'), k.Is('Y'):
			ans = true
		case k.Code == term.KeyEnter:
		default:
			continue
		}
		fmt.Fprintln(p.out, map[bool]string{true: "yes", false: "no"}[ans])
		return ans
	}
}

// ---- config chain ----

func parseIDs(args []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.Join(args, " "), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		f = strings.ToLower(f)
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func runConfigChain(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		printHelp(stdout, "config")
		return nil
	}
	cfg, err := loadConfigFile()
	if err != nil {
		return err
	}
	ids := parseIDs(args)
	if len(ids) > 0 {
		return setChain(ctx, cfg, ids, stdout, stderr)
	}
	if !isInteractive(stdout) {
		printChainGuide(cfg, stdout)
		return nil
	}
	return runManagerFn(ctx, stdout, stderr)
}

// setChain validates ids against the known providers, warns about ones
// that are not usable right now, and saves.
func setChain(ctx context.Context, cfg *config.Config, ids []string, stdout, stderr io.Writer) error {
	ps := listProviders(cfg)
	byID := map[string]provider.Provider{}
	known := make([]string, len(ps))
	for i, p := range ps {
		byID[p.ID()] = p
		known[i] = p.ID()
	}
	var bad []string
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			continue
		}
		msg := fmt.Sprintf("unknown provider %q", id)
		if m := closeMatches(id, known); len(m) > 0 {
			msg += "; did you mean " + strings.Join(m, ", ") + "?"
		}
		bad = append(bad, msg)
	}
	if len(bad) > 0 {
		return usageError{errors.New(strings.Join(bad, "\n") + "\n(see `shelp providers` for every ID)")}
	}
	chosen := make([]provider.Provider, len(ids))
	for i, id := range ids {
		chosen[i] = byID[id]
	}
	warnUnavailable(ctx, chosen, stderr)
	var pr *prompter
	if isInteractive(stdout) {
		pr = newPrompter(ctx, stdin, stdout)
	}
	_, err := saveOps(ctx, configOps{chain: ids}, pr, stdout, stderr)
	return err
}

func warnUnavailable(ctx context.Context, chosen []provider.Provider, stderr io.Writer) {
	pal := style.For(stderr)
	sts := probeProviders(ctx, chosen, cooldown.Load())
	for i, st := range sts {
		if st.Available {
			continue
		}
		why := st.Reason
		if why == "" {
			why = "unavailable"
		}
		msg := fmt.Sprintf("warning: %s is not available right now (%s); saving it anyway", chosen[i].ID(), why)
		if st.Fix != "" {
			msg += "\n         " + st.Fix
		}
		fmt.Fprintln(stderr, pal.Warn(msg))
	}
}

// closeMatches suggests known IDs near id: same family or a substring
// either way, or within two edits.
func closeMatches(id string, known []string) []string {
	var out []string
	fam, _, _ := strings.Cut(id, ":")
	for _, k := range known {
		kf, _, _ := strings.Cut(k, ":")
		if strings.Contains(k, id) || strings.Contains(id, k) || (fam != "" && fam == kf) || editDistance(id, k) <= 2 {
			out = append(out, k)
		}
		if len(out) == 5 {
			break
		}
	}
	return out
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

// ---- config edit ----

func runConfigEdit(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			printHelp(stdout, "config")
			return nil
		}
		printHelp(stderr, "config")
		return usageError{fmt.Errorf("config edit takes no arguments, got %q", args[0])}
	}
	path := config.Path()
	state, managed := fileState(ctx, path)
	if managed == "managed by chezmoi" {
		fmt.Fprintf(stdout, "%s is managed by chezmoi; edit it there so the change sticks:\n  chezmoi edit --apply %s\n", tildePath(path), tildePath(path))
		return nil
	}
	if state == "missing" {
		if err := config.WriteAtomic(path, []byte(config.Starter)); err != nil {
			return err
		}
		fmt.Fprintln(stderr, "created "+tildePath(path))
	}
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	argv := strings.Fields(ed)
	if len(argv) == 0 {
		argv = []string{"vi"}
	}
	return runEditor(ctx, append(argv, path))
}

// ---- guided setup ----

// shortcutsNeedSetup reports whether the Apple Shortcut is missing or
// outdated, and a short reason. A provider that is unavailable for another
// reason (not macOS 27, ...) is not something setup can fix.
func shortcutsNeedSetup(ctx context.Context, cfg *config.Config) (bool, string) {
	p, err := provider.Get(cfg, "shortcuts:apple")
	if err != nil {
		return false, ""
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st := p.Detect(c)
	if st.Available || !strings.Contains(st.Fix, "shelp setup shortcuts") {
		return false, ""
	}
	return true, st.Reason
}

func runShortcutsSetupStep(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) error {
	for _, s := range provider.SetupSteps() {
		if s.Name == "shortcuts" {
			return s.Run(ctx, cfg, stdout, stderr)
		}
	}
	return errors.New("no shortcuts setup step registered")
}

// runGuidedSetup is `shelp setup` on a terminal: shell hook, Apple
// Shortcut, chain; each step is skippable.
func runGuidedSetup(ctx context.Context, stdout, stderr io.Writer) error {
	pal := style.For(stdout)
	pr := newPrompter(ctx, stdin, stdout)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	heading := func(s string) { fmt.Fprintln(stdout, "\n"+pal.Bold(pal.Accent(s))) }

	heading("Shell hook")
	if installed, rc, err := hookInstalledFn(""); err != nil {
		fmt.Fprintln(stdout, pal.Warn(err.Error()))
	} else if installed {
		fmt.Fprintln(stdout, pal.OK("✓")+" already set up ("+tildePath(rc)+")")
	} else if pr.yesNo("Add shelp's hook to "+tildePath(rc)+"?", true) && pr.err() == nil {
		if err := runShellSetupFn(ctx, nil, stdout, stderr); err != nil {
			fmt.Fprintln(stdout, pal.Err("shell hook: "+err.Error()))
		}
	} else if err := pr.err(); err != nil {
		return err
	} else {
		fmt.Fprintln(stdout, pal.Dim("skipped; run `shelp setup shell` any time"))
	}

	if setupGOOS == "darwin" {
		heading("Apple Shortcut")
		if need, why := shortcutsNeedFn(ctx, cfg); !need {
			fmt.Fprintln(stdout, pal.OK("✓")+" nothing to do")
		} else if pr.yesNo("The shortcut is "+why+". Set it up now (it needs one click in Shortcuts)?", true) && pr.err() == nil {
			if err := runShortcutsStep(ctx, cfg, stdout, stderr); err != nil {
				fmt.Fprintln(stdout, pal.Err("shortcut: "+err.Error()))
			}
		} else if err := pr.err(); err != nil {
			return err
		} else {
			fmt.Fprintln(stdout, pal.Dim("skipped; run `shelp setup shortcuts` any time"))
		}
	}

	heading("Fallback chain")
	cur := chainFor("", cfg)
	fmt.Fprintf(stdout, "%s  %s\n", strings.Join(cur, " → "), pal.Dim("("+chainSource(cfg)+")"))
	if pr.yesNo("Change it (and add routes) in the provider manager?", false) {
		if err := runManagerFn(ctx, stdout, stderr); err != nil {
			return err
		}
	} else if err := pr.err(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "\n"+pal.Dim("done; `shelp config` shows your settings"))
	return nil
}

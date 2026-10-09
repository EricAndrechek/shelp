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

// runConfig handles `shelp config [chain|edit]`.
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
	row("chain", strings.Join(chainFor("", cfg), " → "), chainSource(cfg))
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
	fmt.Fprintln(stdout, p.Dim("change the chain: shelp config chain   edit the file: shelp config edit   all settings: shelp help config"))
	return nil
}

// ---- prompts ----

// prompter reads line answers. One bufio.Reader is shared by every prompt
// of a run, since a second one would swallow the first's buffered input.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out}
}

// line prints q and reads one line (trimmed). ok is false at EOF with
// nothing read.
func (p *prompter) line(q string) (s string, ok bool) {
	fmt.Fprint(p.out, q)
	s, err := p.in.ReadString('\n')
	if err != nil && s == "" {
		fmt.Fprintln(p.out)
		return "", false
	}
	return strings.TrimSpace(s), true
}

// yesNo asks q with a [Y/n] or [y/N] suffix; Enter and EOF choose def.
func (p *prompter) yesNo(q string, def bool) bool {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	for {
		s, ok := p.line(q + suffix)
		if !ok {
			return def
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
		p := style.For(stdout)
		fmt.Fprintf(stdout, "chain  %s  %s\n", strings.Join(chainFor("", cfg), " → "), p.Dim("("+chainSource(cfg)+")"))
		fmt.Fprintln(stdout, "set it with: shelp config chain fm,claude:haiku   (see `shelp providers` for IDs)")
		return nil
	}
	return pickChain(ctx, cfg, newPrompter(stdin, stdout), stdout, stderr)
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
	return saveChain(ctx, ids, stdout, stderr)
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

// saveChain writes providers = ids to the config file, or, when the file is
// managed elsewhere, says exactly how to make the change by hand.
func saveChain(ctx context.Context, ids []string, stdout, stderr io.Writer) error {
	pal := style.For(stdout)
	if v := os.Getenv("SHELP_PROVIDER"); v != "" {
		fmt.Fprintln(stderr, style.For(stderr).Warn("warning: $SHELP_PROVIDER is set ("+v+") and overrides the config file; unset it for this chain to take effect"))
	}
	path := config.Path()
	line := config.ProvidersLine(ids)
	if _, managed := fileState(ctx, path); managed != "" {
		edit := path
		how := "chezmoi edit " + tildePath(path)
		if strings.HasPrefix(managed, "a symlink") {
			if t, err := filepath.EvalSymlinks(path); err == nil {
				edit = t
			}
			how = "edit " + tildePath(edit)
		}
		fmt.Fprintf(stdout, "%s is %s, so shelp will not write it.\n", tildePath(path), managed)
		fmt.Fprintf(stdout, "To set the chain, run:\n  %s\nand put this line above any [table] (replacing an existing providers line):\n  %s\n", how, line)
		return nil
	}
	if err := config.SaveProviders(path, ids); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s %s\n", pal.OK("saved"), strings.Join(ids, " → "))
	fmt.Fprintln(stdout, pal.Dim("in "+tildePath(path)))
	return nil
}

// pickChain is the interactive chain picker.
func pickChain(ctx context.Context, cfg *config.Config, pr *prompter, stdout, stderr io.Writer) error {
	pal := style.For(stdout)
	ps := listProviders(cfg)
	cds := cooldown.Load()
	sts := probeProviders(ctx, ps, cds)
	rows := buildProviderRows(ps, sts, cds)
	current := cfg.Providers
	if len(current) == 0 {
		current = provider.DefaultChain
	}
	pos := map[string]int{}
	for i, id := range current {
		pos[id] = i + 1
	}
	idw := 0
	for _, r := range rows {
		if w := style.Width(r.ID); w > idw {
			idw = w
		}
	}
	width := style.TermWidth(stdout)
	syms := make([]string, len(rows))
	texts := make([]string, len(rows))
	tw := 0
	for i, r := range rows {
		switch r.Status {
		case statusOK:
			syms[i], texts[i] = pal.OK("✓"), r.Label
			if texts[i] == "" {
				texts[i] = r.Description
			}
		case statusCooldown:
			syms[i], texts[i] = pal.Warn("◷"), pal.Warn("cooldown "+r.Reason)
		default:
			reason := r.Reason
			if reason == "" {
				reason = "unavailable"
			}
			syms[i], texts[i] = pal.Dim("·"), pal.Dim(reason)
		}
		if w := style.Width(texts[i]); w > tw {
			tw = w
		}
	}
	for i, r := range rows {
		l := fmt.Sprintf("%3d  %s %s  %s", i+1, syms[i], style.PadRight(r.ID, idw), texts[i])
		if n, ok := pos[r.ID]; ok {
			l = fmt.Sprintf("%3d  %s %s  %s   %s", i+1, syms[i], style.PadRight(r.ID, idw),
				style.PadRight(texts[i], tw), pal.Dim(fmt.Sprintf("chain #%d", n)))
		}
		if width > 0 {
			l = style.Truncate(l, width-1)
		}
		fmt.Fprintln(stdout, l)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "%s  %s\n", pal.Dim("current"), strings.Join(current, " → "))

	var ids []string
	for {
		s, ok := pr.line("New order (numbers, e.g. 3 1 5; Enter keeps the current one): ")
		if !ok || s == "" {
			fmt.Fprintln(stdout, pal.Dim("chain unchanged"))
			return nil
		}
		var err error
		if ids, err = resolvePicks(s, rows); err != nil {
			fmt.Fprintln(stdout, pal.Err(err.Error()))
			continue
		}
		break
	}
	fmt.Fprintf(stdout, "%s  %s\n", pal.Dim("new"), strings.Join(ids, " → "))
	warnFromRows(ids, rows, stderr)
	return saveChain(ctx, ids, stdout, stderr)
}

// warnFromRows warns about chosen providers the picker already saw as not
// usable (no second probe).
func warnFromRows(ids []string, rows []providerRow, stderr io.Writer) {
	pal := style.For(stderr)
	for _, id := range ids {
		for _, r := range rows {
			if r.ID != id || r.Status == statusOK {
				continue
			}
			why := r.Reason
			if why == "" {
				why = "unavailable"
			}
			msg := fmt.Sprintf("warning: %s is not available right now (%s); saving it anyway", id, why)
			if r.Fix != "" {
				msg += "\n         " + r.Fix
			}
			fmt.Fprintln(stderr, pal.Warn(msg))
		}
	}
}

// resolvePicks turns "3 1 5" (numbers, or provider IDs, comma or space
// separated) into provider IDs in that order, without repeats.
func resolvePicks(s string, rows []providerRow) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, f := range parseIDs([]string{s}) {
		id := f
		if n, err := strconv.Atoi(f); err == nil {
			if n < 1 || n > len(rows) {
				return nil, fmt.Errorf("%d is not in the list (1-%d)", n, len(rows))
			}
			id = rows[n-1].ID
		} else {
			found := false
			for _, r := range rows {
				found = found || r.ID == f
			}
			if !found {
				return nil, fmt.Errorf("%q is not a number or a provider ID", f)
			}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("pick at least one")
	}
	return ids, nil
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
	pr := newPrompter(stdin, stdout)
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
	} else if pr.yesNo("Add shelp's hook to "+tildePath(rc)+"?", true) {
		if err := runShellSetupFn(ctx, nil, stdout, stderr); err != nil {
			fmt.Fprintln(stdout, pal.Err("shell hook: "+err.Error()))
		}
	} else {
		fmt.Fprintln(stdout, pal.Dim("skipped; run `shelp setup shell` any time"))
	}

	if setupGOOS == "darwin" {
		heading("Apple Shortcut")
		if need, why := shortcutsNeedFn(ctx, cfg); !need {
			fmt.Fprintln(stdout, pal.OK("✓")+" nothing to do")
		} else if pr.yesNo("The shortcut is "+why+". Set it up now (it needs one click in Shortcuts)?", true) {
			if err := runShortcutsStep(ctx, cfg, stdout, stderr); err != nil {
				fmt.Fprintln(stdout, pal.Err("shortcut: "+err.Error()))
			}
		} else {
			fmt.Fprintln(stdout, pal.Dim("skipped; run `shelp setup shortcuts` any time"))
		}
	}

	heading("Fallback chain")
	cur := chainFor("", cfg)
	fmt.Fprintf(stdout, "%s  %s\n", strings.Join(cur, " → "), pal.Dim("("+chainSource(cfg)+")"))
	if pr.yesNo("Change it?", false) {
		fmt.Fprintln(stdout)
		cfg, err := loadConfigFile()
		if err != nil {
			return err
		}
		if err := pickChain(ctx, cfg, pr, stdout, stderr); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "\n"+pal.Dim("done; `shelp config` shows your settings"))
	return nil
}

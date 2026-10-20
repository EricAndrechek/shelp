package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
)

// Seams for the chezmoi offer: tests never run the real chezmoi.
var (
	// chezmoiSourcePath returns the chezmoi source file of target.
	chezmoiSourcePath = func(ctx context.Context, target string) (string, error) {
		if _, err := exec.LookPath("chezmoi"); err != nil {
			return "", err
		}
		out, err := exec.CommandContext(ctx, "chezmoi", "source-path", target).Output()
		return strings.TrimSpace(string(out)), err
	}
	// chezmoiApply runs `chezmoi apply target` on the terminal (it may ask
	// before overwriting a target changed by hand).
	chezmoiApply = func(ctx context.Context, target string) error {
		cmd := exec.CommandContext(ctx, "chezmoi", "apply", target)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	// envLookup reads the environment (key variables: set or not, never
	// the value).
	envLookup = os.LookupEnv
)

// configOps is one edit of the config file: a new chain, routes to add,
// routes (by name) to remove. Every writer of the file -- `config chain`,
// `config add`/`remove`, the provider manager -- goes through it, so a
// file managed elsewhere is handled the same way everywhere.
type configOps struct {
	chain   []string // nil leaves the providers line alone
	adds    []provider.CustomRoute
	removes []string
}

func (o configOps) empty() bool { return o.chain == nil && len(o.adds) == 0 && len(o.removes) == 0 }

// routeKey names a configured route for comparisons: table + name.
func routeKey(local bool, name string) string {
	if local {
		return "local/" + name
	}
	return "endpoint/" + name
}

func routeKeys(cfg *config.Config) []string {
	var out []string
	for _, r := range provider.ConfiguredRoutes(cfg) {
		out = append(out, routeKey(r.Local, r.Name))
	}
	sort.Strings(out)
	return out
}

// applyRoutes edits src's [[openai.*]] entries only.
func (o configOps) applyRoutes(src string) string {
	out := src
	for _, name := range o.removes {
		for _, table := range []string{"openai.endpoint", "openai.local"} {
			bs := config.ArrayTableBlocks(out, table)
			for i := len(bs) - 1; i >= 0; i-- {
				if blockRouteName(bs[i].Text) == name {
					out = config.RemoveBlock(out, bs[i])
				}
			}
		}
	}
	for _, r := range o.adds {
		out = config.AppendBlock(out, r.Block())
	}
	return out
}

// blockRouteName is the route name a lone [[openai.*]] block declares.
func blockRouteName(text string) string {
	cfg, err := config.Parse(text)
	if err != nil {
		return ""
	}
	rs := provider.ConfiguredRoutes(cfg)
	if len(rs) != 1 {
		return ""
	}
	return rs[0].Name
}

// apply returns src with ops made. The result must parse and hold exactly
// the routes and chain asked for, or it is an error and nothing should be
// written: a hand-written file shelp's line edits can't handle (a quoted
// table name, `endpoint = [...]` inline) is reported, never mangled.
func (o configOps) apply(src string) (string, error) {
	before, err := config.Parse(src)
	if err != nil {
		return "", err
	}
	want := map[string]int{}
	for _, k := range routeKeys(before) {
		want[k]++
	}
	for _, name := range o.removes {
		delete(want, routeKey(false, name))
		delete(want, routeKey(true, name))
	}
	for _, r := range o.adds {
		want[routeKey(r.Local, r.Name)]++
	}
	out := o.applyRoutes(src)
	if o.chain != nil {
		out = config.SetProviders(out, o.chain)
	}
	after, err := config.Parse(out)
	if err != nil {
		return "", fmt.Errorf("the edited file would not parse (%v); make the change with `shelp config edit`", err)
	}
	got := map[string]int{}
	for _, k := range routeKeys(after) {
		got[k]++
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return "", errors.New("could not make this change safely in the file as written; make it with `shelp config edit`")
	}
	if o.chain != nil && !slices.Equal(after.Providers, o.chain) && !(len(o.chain) == 0 && len(after.Providers) == 0) {
		return "", errors.New("could not set the providers line safely; make the change with `shelp config edit`")
	}
	return out, nil
}

// describe says, step by step, how to make ops by hand.
func (o configOps) describe(src string) []string {
	var out []string
	if o.chain != nil {
		out = append(out, "put this line above any [table] (replacing an existing providers line):", "    "+config.ProvidersLine(o.chain))
	}
	for _, name := range o.removes {
		for _, r := range provider.ConfiguredRoutes(mustParse(src)) {
			if r.Name == name {
				out = append(out, fmt.Sprintf("delete the [[%s]] entry with name = %s", r.Table(), config.Quote(name)))
			}
		}
	}
	for _, r := range o.adds {
		out = append(out, "add this entry at the end:")
		for _, l := range strings.Split(strings.TrimSuffix(r.Block(), "\n"), "\n") {
			out = append(out, "    "+l)
		}
	}
	return out
}

func mustParse(src string) *config.Config {
	c, err := config.Parse(src)
	if err != nil {
		return &config.Config{}
	}
	return c
}

// readConfigText returns the config file's text ("" when it is missing).
func readConfigText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// saveResult is what saveOps did.
type saveResult struct {
	saved bool   // the change is in the file (or in chezmoi's source, applied)
	src   string // the file's new text when saved directly
}

// saveOps writes ops to the config file and reports it on stdout. A file
// managed elsewhere is never written directly: the change is printed with
// the commands to make it. When it is chezmoi's and its source is a plain
// file, an interactive run (pr non-nil) is offered (y/N) to make the
// change in the source and run `chezmoi apply`.
func saveOps(ctx context.Context, ops configOps, pr *prompter, stdout, stderr io.Writer) (saveResult, error) {
	pal := style.For(stdout)
	if ops.chain != nil {
		if v := os.Getenv("SHELP_PROVIDER"); v != "" {
			fmt.Fprintln(stderr, style.For(stderr).Warn("warning: $SHELP_PROVIDER is set ("+v+") and overrides the config file; unset it for this chain to take effect"))
		}
	}
	path := config.Path()
	src, err := readConfigText(path)
	if err != nil {
		return saveResult{}, err
	}
	out, err := ops.apply(src)
	if err != nil {
		return saveResult{}, err
	}
	if _, managed := fileState(ctx, path); managed != "" {
		return saveManaged(ctx, ops, src, path, managed, pr, stdout)
	}
	if err := config.WriteAtomic(path, []byte(out)); err != nil {
		return saveResult{}, err
	}
	for _, l := range opsSummary(pal, ops) {
		fmt.Fprintln(stdout, l)
	}
	fmt.Fprintln(stdout, pal.Dim("in "+tildePath(path)))
	return saveResult{saved: true, src: out}, nil
}

// opsSummary is one line per change made.
func opsSummary(pal style.Palette, ops configOps) []string {
	var out []string
	for _, name := range ops.removes {
		out = append(out, pal.OK("removed")+" "+name)
	}
	for _, r := range ops.adds {
		out = append(out, pal.OK("added")+" "+r.Name+" "+pal.Dim("("+strings.Join(r.IDs(), ", ")+")"))
	}
	if ops.chain != nil {
		c := strings.Join(ops.chain, " → ")
		if len(ops.chain) == 0 {
			c = "(empty: the default, " + strings.Join(provider.DefaultChain, " → ") + ")"
		}
		out = append(out, pal.OK("saved")+" "+c)
	}
	return out
}

func saveManaged(ctx context.Context, ops configOps, src, path, managed string, pr *prompter, stdout io.Writer) (saveResult, error) {
	pal := style.For(stdout)
	chez := !strings.HasPrefix(managed, "a symlink")
	edit := "chezmoi edit " + tildePath(path)
	if !chez {
		target := path
		if t, err := filepath.EvalSymlinks(path); err == nil {
			target = t
		}
		edit = "edit " + tildePath(target)
	}
	fmt.Fprintf(stdout, "%s is %s, so shelp will not write it.\n", tildePath(path), managed)
	fmt.Fprintf(stdout, "To make the change, run:\n  %s\nand\n", edit)
	for _, l := range ops.describe(src) {
		fmt.Fprintln(stdout, "  "+l)
	}
	if !chez {
		return saveResult{}, nil
	}
	fmt.Fprintf(stdout, "then apply it:\n  chezmoi apply %s\n", tildePath(path))
	if pr == nil {
		return saveResult{}, nil
	}
	source, err := chezmoiSourcePath(ctx, path)
	if err != nil || source == "" {
		return saveResult{}, nil
	}
	if why := chezmoiSourceKind(source); why != "" {
		fmt.Fprintln(stdout, pal.Dim("(its chezmoi source is "+why+", so make the change there by hand)"))
		return saveResult{}, nil
	}
	q := fmt.Sprintf("Its chezmoi source, %s, is a plain file. Make the change there and run `chezmoi apply %s`?", tildePath(source), tildePath(path))
	if !pr.yesNo(q, false) {
		return saveResult{}, pr.err()
	}
	text, err := os.ReadFile(source)
	if err != nil {
		return saveResult{}, err
	}
	out, err := ops.apply(string(text))
	if err != nil {
		return saveResult{}, fmt.Errorf("%s: %w", tildePath(source), err)
	}
	if err := config.WriteAtomic(source, []byte(out)); err != nil {
		return saveResult{}, err
	}
	fmt.Fprintln(stdout, pal.OK("updated")+" "+tildePath(source))
	if err := chezmoiApply(ctx, path); err != nil {
		return saveResult{}, fmt.Errorf("chezmoi apply %s: %w (the source is updated; run it again)", tildePath(path), err)
	}
	fmt.Fprintln(stdout, pal.OK("applied")+" "+tildePath(path))
	for _, l := range opsSummary(pal, ops) {
		fmt.Fprintln(stdout, l)
	}
	return saveResult{saved: true}, nil
}

// chezmoiSourceKind says why a chezmoi source file is not one shelp can
// edit like the target ("" when it can): a template, an encrypted file,
// a modify_/create_ script and the like render to something else.
func chezmoiSourceKind(source string) string {
	base := filepath.Base(source)
	switch {
	case strings.HasSuffix(base, ".tmpl"):
		return "a template"
	case strings.Contains(base, "encrypted_") || strings.HasSuffix(base, ".age") || strings.HasSuffix(base, ".asc"):
		return "encrypted"
	}
	for _, p := range []string{"modify_", "create_", "symlink_", "remove_", "run_"} {
		if strings.HasPrefix(base, p) {
			return "a chezmoi " + strings.TrimSuffix(p, "_") + " file"
		}
	}
	fi, err := os.Lstat(source)
	if err != nil || !fi.Mode().IsRegular() {
		return "not a regular file"
	}
	return ""
}

// ---- config add / remove ----

const addUsage = `usage: shelp config add endpoint NAME (--preset P | --base-url URL) [--key-env VAR] --model M [--model M2 ...] [--label TEXT]
       shelp config add local NAME --base-url URL [--label TEXT]`

func addUsageErr(format string, args ...any) error {
	return usageError{fmt.Errorf(format+"\n"+addUsage, args...)}
}

// parseAddArgs reads `config add` arguments: two positionals (kind, name)
// and flags in any order, as --flag value or --flag=value.
func parseAddArgs(args []string) (r provider.CustomRoute, keySet bool, err error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		switch name {
		case "--preset", "--base-url", "--key-env", "--model", "--label":
			if !hasVal {
				if i+1 >= len(args) {
					return r, false, addUsageErr("%s needs a value", name)
				}
				i++
				val = args[i]
			}
			switch name {
			case "--preset":
				r.Preset = strings.ToLower(strings.TrimSpace(val))
			case "--base-url":
				r.BaseURL = strings.TrimSpace(val)
			case "--key-env":
				r.KeyEnv, keySet = strings.TrimSpace(val), true
			case "--model":
				for _, m := range strings.Split(val, ",") {
					if m = strings.TrimSpace(m); m != "" {
						r.Models = append(r.Models, m)
					}
				}
			case "--label":
				r.Label = strings.TrimSpace(val)
			}
		case "--api-key", "--key", "--token":
			// Never echo the value: it is a secret.
			return r, false, addUsageErr("shelp never takes an API key: put `export NAME=<key>` in your shell rc or env file and pass NAME with --key-env")
		default:
			if strings.HasPrefix(a, "-") {
				return r, false, addUsageErr("unknown flag %s", name)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return r, false, addUsageErr("config add needs a kind (endpoint or local) and a name")
	}
	r.Name = strings.ToLower(strings.TrimSpace(pos[1]))
	switch pos[0] {
	case "endpoint":
		if r.Preset == "" && !keySet && r.Name != "" {
			r.KeyEnv = provider.DefaultKeyEnv(r.Name)
		}
	case "local":
		r.Local = true
		if r.Preset != "" || keySet || len(r.Models) > 0 {
			return r, false, addUsageErr("a local server takes --base-url and --label only (its models are discovered)")
		}
	default:
		return r, false, addUsageErr("unknown kind %q: endpoint or local", pos[0])
	}
	return r, keySet, nil
}

func runConfigAdd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) || slices.ContainsFunc(args, isHelpFlag) {
		if len(args) == 0 {
			printHelp(stderr, "config")
			return usageError{errors.New(addUsage)}
		}
		printHelp(stdout, "config")
		return nil
	}
	r, _, err := parseAddArgs(args)
	if err != nil {
		return err
	}
	cfg, err := loadConfigFile()
	if err != nil {
		return err
	}
	if err := r.Validate(cfg); err != nil {
		return usageError{err}
	}
	pal := style.For(stdout)
	ids := r.IDs()
	ops := configOps{adds: []provider.CustomRoute{r}}
	chain := cfg.Providers
	if len(chain) == 0 {
		chain = provider.DefaultChain
	}
	newChain := append(slices.Clone(chain), ids...)
	var pr *prompter
	if isInteractive(stdout) {
		pr = newPrompter(ctx, stdin, stdout)
		describeRoute(pal, r, stdout)
		if pr.yesNo(fmt.Sprintf("Also put %s at the end of your chain?", strings.Join(ids, ", ")), true) {
			ops.chain = newChain
		}
		if err := pr.err(); err != nil {
			return err
		}
	}
	res, err := saveOps(ctx, ops, pr, stdout, stderr)
	if err != nil {
		return err
	}
	if pr == nil {
		describeRoute(pal, r, stdout)
	}
	if ops.chain == nil || !res.saved {
		fmt.Fprintln(stdout, "use it: "+pal.Bold("shelp config chain "+strings.Join(newChain, ",")))
	}
	return nil
}

// describeRoute prints a route's provider IDs and, for a hosted one,
// whether its key variable is set (never its value).
func describeRoute(pal style.Palette, r provider.CustomRoute, stdout io.Writer) {
	where := "cloud"
	if r.Local {
		where = "local"
	}
	for _, id := range r.IDs() {
		l := "  " + id + "  " + pal.Dim(where)
		if r.Label != "" {
			l += "  " + r.Label
		}
		fmt.Fprintln(stdout, l)
	}
	if r.Local {
		if provider.IsBuiltinLocal(r.Name) {
			fmt.Fprintln(stdout, pal.Dim("  replaces the built-in "+r.Name+" at "+provider.BuiltinLocalURL(r.Name)))
		}
		fmt.Fprintln(stdout, pal.Dim("  it uses the server's first loaded model; once `shelp providers` has seen the server, each model is listed as "+r.Name+":<model>"))
		return
	}
	for _, l := range keyEnvLines(pal, r.EffectiveKeyEnv()) {
		fmt.Fprintln(stdout, "  "+l)
	}
}

// keyEnvLines says whether the key variable is set and, if not, where to
// set it. It never reads out the value.
func keyEnvLines(pal style.Palette, env string) []string {
	if env == "" {
		return []string{pal.Dim("no key: requests are sent without one")}
	}
	if v, ok := envLookup(env); ok && v != "" {
		return []string{pal.OK("✓") + " $" + env + " is set in this shell"}
	}
	return []string{
		pal.Warn("✗ $" + env + " is not set in this shell."),
		pal.Dim("  Add `export " + env + "=…` to your shell rc (e.g. ~/.zshrc) or env file and open a new shell;"),
		pal.Dim("  shelp reads the key from there each time and never stores it."),
	}
}

func runConfigRemove(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && isHelpFlag(args[0]) {
		printHelp(stdout, "config")
		return nil
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		printHelp(stderr, "config")
		return usageError{errors.New("usage: shelp config remove NAME   (an endpoint or local server added with `shelp config add`)")}
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))
	cfg, err := loadConfigFile()
	if err != nil {
		return err
	}
	var names []string
	found := false
	for _, r := range provider.ConfiguredRoutes(cfg) {
		names = append(names, r.Name)
		found = found || r.Name == name
	}
	if !found {
		msg := fmt.Sprintf("no endpoint or local server named %q in %s", name, tildePath(config.Path()))
		switch {
		case provider.IsBuiltinLocal(name):
			msg = fmt.Sprintf("%s is a built-in local server, not something in your config; to stop using it, leave it out of your chain", name)
		case len(names) > 0:
			msg += "; the config has: " + strings.Join(names, ", ")
		}
		return usageError{errors.New(msg)}
	}
	ops := configOps{removes: []string{name}}
	var dropped []string
	if len(cfg.Providers) > 0 && !provider.IsBuiltinLocal(name) {
		var keep []string
		for _, id := range cfg.Providers {
			if id == name || strings.HasPrefix(id, name+":") {
				dropped = append(dropped, id)
				continue
			}
			keep = append(keep, id)
		}
		if len(dropped) > 0 {
			ops.chain = append([]string{}, keep...)
		}
	}
	var pr *prompter
	if isInteractive(stdout) {
		pr = newPrompter(ctx, stdin, stdout)
	}
	res, err := saveOps(ctx, ops, pr, stdout, stderr)
	if err != nil {
		return err
	}
	if res.saved && len(dropped) > 0 {
		fmt.Fprintln(stdout, style.For(stdout).Dim("also taken out of your chain: "+strings.Join(dropped, ", ")))
	}
	return nil
}

// ---- config chain, without a terminal ----

// printChainGuide is `shelp config chain` with no IDs and no terminal: the
// chain, every provider ID (local or cloud), and the commands that change
// things. It probes nothing.
func printChainGuide(cfg *config.Config, stdout io.Writer) {
	p := style.For(stdout)
	fmt.Fprintf(stdout, "chain  %s  %s\n\n", strings.Join(chainFor("", cfg), " → "), p.Dim("("+chainSource(cfg)+")"))
	ps := listProviders(cfg)
	sort.SliceStable(ps, func(i, j int) bool { return managerKindRank(ps[i].Kind()) < managerKindRank(ps[j].Kind()) })
	idw := 0
	for _, pr := range ps {
		idw = max(idw, style.Width(pr.ID()))
	}
	fmt.Fprintln(stdout, "providers  "+p.Dim("(local: runs on this machine, works offline; cloud: needs the internet)"))
	for _, pr := range ps {
		fmt.Fprintf(stdout, "  %s  %s  %s\n", style.PadRight(pr.ID(), idw), style.PadRight(whereBadge(p, pr.Kind()), 5), provider.LabelOf(pr))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "`shelp providers` shows which of these work here. To change things:")
	for _, l := range [][2]string{
		{"shelp config chain ID,ID,...", "set the chain, in the order to try"},
		{"shelp config add endpoint NAME --preset P --model M", "add a hosted endpoint (its key stays in an env var)"},
		{"shelp config add local NAME --base-url URL", "add a local OpenAI-compatible server"},
		{"shelp config remove NAME", "remove one you added"},
		{"shelp config edit", "edit the file"},
	} {
		fmt.Fprintf(stdout, "  %s  %s\n", style.PadRight(l[0], 52), p.Dim(l[1]))
	}
}

// whereBadge is "local" (works offline) or "cloud" (needs the internet),
// from provider.Kind.NeedsNetwork.
func whereBadge(p style.Palette, k provider.Kind) string {
	if k.NeedsNetwork() {
		return p.Accent("cloud")
	}
	return p.OK("local")
}

// managerKindRank orders kinds for the manager and the chain guide:
// what runs on this machine first, then Apple's cloud, APIs, agent CLIs.
func managerKindRank(k provider.Kind) int {
	switch k {
	case provider.OnDevice:
		return 0
	case provider.LocalServer:
		return 1
	case provider.AppleCloud:
		return 2
	case provider.API:
		return 3
	case provider.AgentCLI:
		return 4
	}
	return 5
}

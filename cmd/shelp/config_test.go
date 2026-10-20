package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/term"
)

type fakeProv struct {
	id, label string
	kind      provider.Kind
	st        provider.Status
}

func (f fakeProv) ID() string          { return f.id }
func (f fakeProv) Description() string { return f.label }
func (f fakeProv) Kind() provider.Kind { return f.kind }
func (f fakeProv) Detect(context.Context) provider.Status {
	return f.st
}
func (f fakeProv) Generate(context.Context, provider.Request) (string, error) {
	panic("tests never generate")
}

// env is an isolated world: temp HOME and config path, fake providers and
// probes, a scripted stdin, PATH without chezmoi.
type env struct {
	home, cfgPath string
}

func isolateCfg(t *testing.T, stdinText string, interactive bool) env {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": "", "SHELP_CONFIG": "", "SHELP_PROVIDER": "nosuch:model",
		"SHELP_HOOKS": "", "ZDOTDIR": "", "SHELL": "/bin/zsh", "PATH": "/usr/bin:/bin",
		"XDG_CACHE_HOME": filepath.Join(home, "cache"), "VISUAL": "", "EDITOR": "",
	} {
		t.Setenv(k, v)
	}
	os.Unsetenv("SHELP_PROVIDER") // most tests want it unset; the warning test sets it
	setColorEnv(t, "")
	fakes := []provider.Provider{
		fakeProv{"shortcuts:apple-pro", "Apple Intelligence Pro", provider.AppleCloud, provider.Status{Available: true}},
		fakeProv{"fm", "Apple on-device", provider.OnDevice, provider.Status{Available: true}},
		fakeProv{"claude:haiku", "haiku", provider.AgentCLI, provider.Status{Reason: "not installed", Fix: "install the claude CLI"}},
		fakeProv{"lmstudio:qwen", "qwen", provider.LocalServer, provider.Status{Reason: "not running (127.0.0.1:1234)"}},
	}
	oldList, oldProbe, oldIn, oldInt := listProviders, probeProviders, stdin, isInteractive
	listProviders = func(*config.Config) []provider.Provider { return fakes }
	probeProviders = func(ctx context.Context, ps []provider.Provider, _ map[string]cooldown.Entry) []provider.Status {
		out := make([]provider.Status, len(ps))
		for i, p := range ps {
			out[i] = p.Detect(ctx)
		}
		return out
	}
	stdin = strings.NewReader(stdinText)
	isInteractive = func(io.Writer) bool { return interactive }
	// A task must never reach a real backend from these tests: only a chain
	// of nosuch:model may get through.
	realTask := runTask
	runTask = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		if os.Getenv("SHELP_PROVIDER") != "nosuch:model" {
			t.Errorf("%q reached the task path without SHELP_PROVIDER=nosuch:model", args)
			return errors.New("test guard: no model calls")
		}
		return realTask(ctx, args, stdout, stderr)
	}
	t.Cleanup(func() {
		listProviders, probeProviders, stdin, isInteractive, runTask = oldList, oldProbe, oldIn, oldInt, realTask
	})
	return env{home: home, cfgPath: filepath.Join(home, ".config", "shelp", "config.toml")}
}

func fakeChezmoi(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = source-path ]; then exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "chezmoi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConfigShow(t *testing.T) {
	e := isolateCfg(t, "", false)
	write(t, e.cfgPath, "providers = [\"fm\"]\ntemplate = \"v2\"\n[shell]\nbind = \"^G\"\n")
	out, _, err := runOut(t, "config")
	t.Logf("shelp config:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"~/.config/shelp/config.toml", "exists", "fm  (config)", "v2  (config)", "^G  (config)", "30m  (default)", "true"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
	// A missing file is reported as such.
	os.Remove(e.cfgPath)
	out, _, _ = runOut(t, "config")
	if !strings.Contains(out, "missing") || !strings.Contains(out, "(default)") {
		t.Errorf("missing file output:\n%s", out)
	}
}

func TestConfigShowManaged(t *testing.T) {
	e := isolateCfg(t, "", false)
	write(t, e.cfgPath, "")
	fakeChezmoi(t)
	out, _, _ := runOut(t, "config")
	if !strings.Contains(out, "managed by chezmoi") {
		t.Errorf("output:\n%s", out)
	}
}

func TestConfigChainSaves(t *testing.T) {
	e := isolateCfg(t, "", false)
	write(t, e.cfgPath, "# my config\ntemplate = \"v2\" # keep\n\n# shell stuff\n[shell]\nbind = \"^G\"\n")
	out, errOut, err := runOut(t, "config", "chain", "fm,", "shortcuts:apple-pro", "fm")
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" || !strings.Contains(out, "saved fm → shortcuts:apple-pro") {
		t.Errorf("got (%q, %q)", out, errOut)
	}
	want := "# my config\ntemplate = \"v2\" # keep\n\nproviders = [\"fm\", \"shortcuts:apple-pro\"]\n\n# shell stuff\n[shell]\nbind = \"^G\"\n"
	if got := read(t, e.cfgPath); got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	// Replacing it again leaves everything else alone.
	if _, _, err := runOut(t, "config", "chain", "claude:haiku"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.cfgPath); !strings.Contains(got, `providers = ["claude:haiku"]`) || strings.Count(got, "providers") != 1 || !strings.HasSuffix(got, "bind = \"^G\"\n") {
		t.Errorf("file:\n%s", got)
	}
}

func TestConfigChainCreatesFile(t *testing.T) {
	e := isolateCfg(t, "", false)
	if _, _, err := runOut(t, "config", "chain", "fm"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.cfgPath); got != "providers = [\"fm\"]\n" {
		t.Errorf("file %q", got)
	}
}

func TestConfigChainHonorsSHELPCONFIG(t *testing.T) {
	isolateCfg(t, "", false)
	p := filepath.Join(t.TempDir(), "x", "c.toml")
	t.Setenv("SHELP_CONFIG", p)
	if _, _, err := runOut(t, "config", "chain", "fm"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "providers = [\"fm\"]\n" {
		t.Errorf("file %q", got)
	}
}

func TestConfigChainUnknown(t *testing.T) {
	e := isolateCfg(t, "", false)
	_, _, err := runOut(t, "config", "chain", "fm", "claude:haikuu", "lmstudoi:qwen")
	if err == nil {
		t.Fatal("want an error")
	}
	t.Logf("error:\n%v", err)
	for _, w := range []string{`unknown provider "claude:haikuu"`, "did you mean claude:haiku", `"lmstudoi:qwen"`, "lmstudio:qwen"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error lacks %q: %v", w, err)
		}
	}
	if _, serr := os.Stat(e.cfgPath); serr == nil {
		t.Error("file written despite the error")
	}
}

func TestConfigChainUnavailableWarns(t *testing.T) {
	e := isolateCfg(t, "", false)
	out, errOut, err := runOut(t, "config", "chain", "claude:haiku", "fm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "warning: claude:haiku is not available right now (not installed)") || !strings.Contains(errOut, "install the claude CLI") {
		t.Errorf("stderr %q", errOut)
	}
	if !strings.Contains(out, "saved") || !strings.Contains(read(t, e.cfgPath), "claude:haiku") {
		t.Errorf("not saved: %q", out)
	}
}

func TestConfigChainEnvWarns(t *testing.T) {
	isolateCfg(t, "", false)
	t.Setenv("SHELP_PROVIDER", "nosuch:model")
	_, errOut, err := runOut(t, "config", "chain", "fm")
	if err != nil || !strings.Contains(errOut, "$SHELP_PROVIDER is set (nosuch:model) and overrides") {
		t.Errorf("got (%q, %v)", errOut, err)
	}
}

func TestConfigChainChezmoiManaged(t *testing.T) {
	e := isolateCfg(t, "", false)
	orig := "template = \"v2\"\n"
	write(t, e.cfgPath, orig)
	fakeChezmoi(t)
	out, _, err := runOut(t, "config", "chain", "fm", "claude:haiku")
	t.Logf("managed save:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"managed by chezmoi", "will not write", "chezmoi edit ~/.config/shelp/config.toml", `providers = ["fm", "claude:haiku"]`} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
	if got := read(t, e.cfgPath); got != orig {
		t.Errorf("managed file was modified: %q", got)
	}
}

func TestConfigChainSymlinkNotWritten(t *testing.T) {
	e := isolateCfg(t, "", false)
	target := filepath.Join(t.TempDir(), "dotfiles", "shelp.toml")
	write(t, target, "template = \"v2\"\n")
	if err := os.MkdirAll(filepath.Dir(e.cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, e.cfgPath); err != nil {
		t.Fatal(err)
	}
	out, _, err := runOut(t, "config", "chain", "fm")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(target)
	if !strings.Contains(out, "a symlink") || !strings.Contains(out, "edit "+real) || !strings.Contains(out, `providers = ["fm"]`) {
		t.Errorf("output:\n%s", out)
	}
	if fi, _ := os.Lstat(e.cfgPath); fi.Mode()&os.ModeSymlink == 0 || read(t, target) != "template = \"v2\"\n" {
		t.Error("symlink or target was changed")
	}
}

func TestConfigChainNotTerminalPrintsCurrent(t *testing.T) {
	isolateCfg(t, "", false)
	out, _, err := runOut(t, "config", "chain")
	if err != nil || !strings.Contains(out, "shortcuts:apple-pro → fm") || !strings.Contains(out, "shelp config chain") {
		t.Errorf("got (%q, %v)", out, err)
	}
}

func TestConfigChainInteractive(t *testing.T) {
	e := isolateCfg(t, "", true)
	// The cursor starts on #1 (shortcuts:apple-pro): J moves it down, j
	// steps onto the first other provider (claude:haiku, needs setup),
	// space adds it as #3 (the cursor follows), K moves it up, s saves.
	scr := newFakeScreen(keys("J", "j", " ", "K", "s", "q")...)
	withScreen(t, scr)
	out, errOut, err := runWithTimeout(t, "config", "chain")
	t.Logf("first settled frame:\n%s", scr.frames[1])
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.cfgPath); got != `providers = ["fm", "claude:haiku", "shortcuts:apple-pro"]`+"\n" {
		t.Errorf("file %q", got)
	}
	if !strings.Contains(out, "saved fm → claude:haiku → shortcuts:apple-pro") || errOut != "" {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
	if scr.started || scr.stops != scr.starts {
		t.Errorf("screen left started: %d starts, %d stops", scr.starts, scr.stops)
	}
}

func TestConfigChainInteractiveQuitKeeps(t *testing.T) {
	for _, quit := range []any{"q", kEsc} {
		e := isolateCfg(t, "", true)
		withScreen(t, newFakeScreen(keys(quit)...))
		out, _, err := runWithTimeout(t, "config", "chain")
		if err != nil || !strings.Contains(out, "no changes") {
			t.Errorf("%v: got (%q, %v)", quit, out, err)
		}
		if _, serr := os.Stat(e.cfgPath); serr == nil {
			t.Errorf("%v: file written", quit)
		}
	}
}

// Ctrl-C quits at once, unsaved changes and all: exit 130, no message.
func TestConfigChainInteractiveCtrlC(t *testing.T) {
	e := isolateCfg(t, "", true)
	scr := newFakeScreen(keys("J", term.CtrlC)...)
	withScreen(t, scr)
	out, _, err := runWithTimeout(t, "config", "chain")
	if code, show := exitCode(context.Background(), err); code != 130 || show {
		t.Errorf("exit %d (show %v), err %v", code, show, err)
	}
	if out != "" {
		t.Errorf("printed %q", out)
	}
	if _, serr := os.Stat(e.cfgPath); serr == nil {
		t.Error("file written")
	}
	if scr.started {
		t.Error("screen left started")
	}
}

func TestConfigEdit(t *testing.T) {
	e := isolateCfg(t, "", false)
	t.Setenv("EDITOR", "myed -w")
	var got []string
	old := runEditor
	runEditor = func(_ context.Context, argv []string) error { got = argv; return nil }
	t.Cleanup(func() { runEditor = old })
	_, errOut, err := runOut(t, "config", "edit")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "myed -w "+e.cfgPath || !strings.Contains(errOut, "created") {
		t.Errorf("editor argv %v, stderr %q", got, errOut)
	}
	if s := read(t, e.cfgPath); s != config.Starter {
		t.Errorf("starter not written: %q", s)
	}
	if c, err := config.Load(e.cfgPath); err != nil || len(c.Providers) != 0 {
		t.Errorf("starter must parse and be inert: %v", err)
	}
	// $VISUAL wins; existing files are left alone; default is vi.
	write(t, e.cfgPath, "x = 1\n")
	t.Setenv("VISUAL", "vis")
	runOut(t, "config", "edit")
	if strings.Join(got, " ") != "vis "+e.cfgPath || read(t, e.cfgPath) != "x = 1\n" {
		t.Errorf("argv %v", got)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	runOut(t, "config", "edit")
	if got[0] != "vi" {
		t.Errorf("argv %v", got)
	}
}

func TestConfigEdit_Chezmoi(t *testing.T) {
	e := isolateCfg(t, "", false)
	write(t, e.cfgPath, "")
	fakeChezmoi(t)
	runEditor = func(context.Context, []string) error { t.Error("editor ran on a chezmoi-managed file"); return nil }
	out, _, err := runOut(t, "config", "edit")
	if err != nil || !strings.Contains(out, "chezmoi edit --apply ~/.config/shelp/config.toml") {
		t.Errorf("got (%q, %v)", out, err)
	}
}

func TestConfigSubcommandErrorsAndTasks(t *testing.T) {
	isolateCfg(t, "", false)
	for _, args := range [][]string{{"config", "-h"}, {"help", "config"}, {"config", "chain", "-h"}} {
		if out, _, err := runOut(t, args...); err != nil || out != commandHelp["config"] {
			t.Errorf("%v: got (%q, %v)", args, out, err)
		}
	}
	// "config <something else>" is a task, not a subcommand: it reaches
	// the (nonexistent) provider, not a usage error.
	t.Setenv("SHELP_PROVIDER", "nosuch:model")
	if _, _, err := runOut(t, "config", "of", "nginx"); err == nil || strings.Contains(err.Error(), "subcommand") {
		t.Errorf("want a provider error, got %v", err)
	}
}

// ---- guided setup ----

type setupFakes struct {
	shellRan, shortcutsRan bool
	installed              bool
	shortcutNeed           bool
}

func guidedFakes(t *testing.T, f *setupFakes) {
	t.Helper()
	oi, os_, on, orr, og := hookInstalledFn, runShellSetupFn, shortcutsNeedFn, runShortcutsStep, setupGOOS
	setupGOOS = "darwin"
	hookInstalledFn = func(string) (bool, string, error) {
		return f.installed, filepath.Join(os.Getenv("HOME"), ".zshrc"), nil
	}
	runShellSetupFn = func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		f.shellRan = true
		io.WriteString(stdout, "added shelp to ~/.zshrc\n")
		return nil
	}
	shortcutsNeedFn = func(context.Context, *config.Config) (bool, string) {
		return f.shortcutNeed, "outdated (format v4, current v5)"
	}
	runShortcutsStep = func(_ context.Context, _ *config.Config, stdout, _ io.Writer) error {
		f.shortcutsRan = true
		io.WriteString(stdout, "shortcut installed\n")
		return nil
	}
	t.Cleanup(func() {
		hookInstalledFn, runShellSetupFn, shortcutsNeedFn, runShortcutsStep, setupGOOS = oi, os_, on, orr, og
	})
}

func TestGuidedSetupAllSteps(t *testing.T) {
	e := isolateCfg(t, "\n\ny\n", true)
	f := &setupFakes{shortcutNeed: true}
	guidedFakes(t, f)
	withScreen(t, newFakeScreen(keys("J", "s", "q")...))
	out, _, err := runOut(t, "setup")
	t.Logf("shelp setup (guided):\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !f.shellRan || !f.shortcutsRan {
		t.Errorf("steps ran: shell=%v shortcuts=%v", f.shellRan, f.shortcutsRan)
	}
	for _, w := range []string{"Add shelp's hook to ~/.zshrc? [Y/n] ", "The shortcut is outdated (format v4, current v5). Set it up now", "Change it (and add routes) in the provider manager? [y/N] ", "saved fm → shortcuts:apple-pro"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
	if got := read(t, e.cfgPath); got != "providers = [\"fm\", \"shortcuts:apple-pro\"]\n" {
		t.Errorf("file %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".zshrc")); err == nil {
		t.Error("an rc file was written")
	}
}

func TestGuidedSetupSkipsAndAlreadyDone(t *testing.T) {
	e := isolateCfg(t, "n\nn\n\n", true)
	f := &setupFakes{shortcutNeed: true}
	guidedFakes(t, f)
	out, _, err := runOut(t, "setup")
	if err != nil || f.shellRan || f.shortcutsRan {
		t.Fatalf("err %v, ran %+v", err, f)
	}
	if !strings.Contains(out, "skipped; run `shelp setup shell`") || !strings.Contains(out, "skipped; run `shelp setup shortcuts`") {
		t.Errorf("output:\n%s", out)
	}
	if _, serr := os.Stat(e.cfgPath); serr == nil {
		t.Error("config written")
	}

	// Hook installed, shortcut fine, not macOS: only the chain remains.
	stdin = strings.NewReader("\n")
	f2 := &setupFakes{installed: true}
	guidedFakes(t, f2)
	setupGOOS = "linux"
	out, _, err = runOut(t, "setup")
	if err != nil || strings.Contains(out, "Apple Shortcut") || !strings.Contains(out, "already set up") {
		t.Errorf("got (%q, %v)", out, err)
	}
}

func TestSetupNotTerminalPrintsHelp(t *testing.T) {
	isolateCfg(t, "", false)
	guidedFakes(t, &setupFakes{})
	out, _, err := runOut(t, "setup")
	if err != nil || out != commandHelp["setup"] {
		t.Errorf("got (%q, %v)", out, err)
	}
}

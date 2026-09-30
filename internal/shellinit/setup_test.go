package shellinit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolate points HOME (and clears ZDOTDIR/XDG_CONFIG_HOME so RCFile's
// fallbacks kick in predictably) at a fresh temp dir, and restricts PATH
// to dir plus the bare minimum so exec.LookPath("chezmoi") reliably
// misses unless a test adds a fake one.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	return home
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestSetup_FreshRC(t *testing.T) {
	home := isolate(t)
	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Action != "added" {
		t.Errorf("Action = %q, want %q", res.Action, "added")
	}
	rc := filepath.Join(home, ".zshrc")
	if res.RCFile != rc {
		t.Errorf("RCFile = %q, want %q", res.RCFile, rc)
	}
	content := readFile(t, rc)
	if !strings.Contains(content, blockStart) || !strings.Contains(content, blockEnd) {
		t.Errorf("rc missing markers:\n%s", content)
	}
	if !strings.Contains(content, `eval "$(shelp init zsh)"`) {
		t.Errorf("rc missing init line:\n%s", content)
	}
}

func TestSetup_FreshRC_WithExistingContent(t *testing.T) {
	home := isolate(t)
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("# my existing config\nexport FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(context.Background(), SetupOptions{Shell: "zsh"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	content := readFile(t, rc)
	if !strings.HasPrefix(content, "# my existing config\nexport FOO=bar\n") {
		t.Errorf("existing content not preserved:\n%s", content)
	}
	if !strings.Contains(content, blockStart) {
		t.Errorf("block not appended:\n%s", content)
	}
}

func TestSetup_Idempotent_NoBindNoOp(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	if _, err := Setup(ctx, SetupOptions{Shell: "zsh"}); err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	res2, err := Setup(ctx, SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("second Setup: %v", err)
	}
	if res2.Action != "unchanged" {
		t.Errorf("Action = %q, want %q", res2.Action, "unchanged")
	}
}

func TestSetup_Idempotent_BindUpdatesBlockOnly(t *testing.T) {
	home := isolate(t)
	ctx := context.Background()
	if _, err := Setup(ctx, SetupOptions{Shell: "zsh"}); err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	rc := filepath.Join(home, ".zshrc")
	before := readFile(t, rc)

	res2, err := Setup(ctx, SetupOptions{Shell: "zsh", Bind: "^G"})
	if err != nil {
		t.Fatalf("second Setup: %v", err)
	}
	if res2.Action != "updated" {
		t.Errorf("Action = %q, want %q", res2.Action, "updated")
	}
	after := readFile(t, rc)
	if strings.Count(after, blockStart) != 1 {
		t.Errorf("expected exactly one managed block, got:\n%s", after)
	}
	if !strings.Contains(after, `--bind '^G'`) {
		t.Errorf("updated block missing --bind:\n%s", after)
	}
	if after == before {
		t.Errorf("block was not actually updated")
	}
}

func TestSetup_ExistingManualLine_NotTouched(t *testing.T) {
	home := isolate(t)
	rc := filepath.Join(home, ".zshrc")
	original := "# hand-written\neval \"$(shelp init zsh)\"\n"
	if err := os.WriteFile(rc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Action != "already-manual" {
		t.Errorf("Action = %q, want %q", res.Action, "already-manual")
	}
	if got := readFile(t, rc); got != original {
		t.Errorf("rc file was modified:\nwant: %q\ngot:  %q", original, got)
	}
}

func TestSetup_SymlinkedRC_NotTouched(t *testing.T) {
	home := isolate(t)
	target := filepath.Join(t.TempDir(), "real-zshrc")
	if err := os.WriteFile(target, []byte("# dotfiles-managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	if err := os.Symlink(target, rc); err != nil {
		t.Fatal(err)
	}
	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Action != "manual" {
		t.Errorf("Action = %q, want %q", res.Action, "manual")
	}
	if !strings.Contains(res.Message, "symlink") {
		t.Errorf("message should mention symlink: %q", res.Message)
	}
	if !strings.Contains(res.Message, `eval "$(shelp init zsh)"`) {
		t.Errorf("message should print the exact line to add: %q", res.Message)
	}
	if got := readFile(t, target); got != "# dotfiles-managed\n" {
		t.Errorf("symlink target was modified:\n%s", got)
	}
}

func TestSetup_ChezmoiManaged_NotTouched(t *testing.T) {
	home := isolate(t)
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("# chezmoi source\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A fake `chezmoi` on PATH that exits 0 for `source-path <rc>` (and
	// non-zero for anything else), simulating "this file is managed".
	fakeDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = source-path ]; then exit 0; fi\nexit 1\n"
	fakePath := filepath.Join(fakeDir, "chezmoi")
	if err := os.WriteFile(fakePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":/usr/bin:/bin")

	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Action != "manual" {
		t.Errorf("Action = %q, want %q", res.Action, "manual")
	}
	if !strings.Contains(res.Message, "chezmoi") {
		t.Errorf("message should mention chezmoi: %q", res.Message)
	}
	if got := readFile(t, rc); got != "# chezmoi source\n" {
		t.Errorf("chezmoi-managed rc was modified:\n%s", got)
	}
}

func TestSetup_ChezmoiNotOnPATH_SkipsCheck(t *testing.T) {
	home := isolate(t) // PATH here has no chezmoi at all
	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Action != "added" {
		t.Errorf("Action = %q, want %q (chezmoi absent should not block editing)", res.Action, "added")
	}
	rc := filepath.Join(home, ".zshrc")
	if _, err := os.Stat(rc); err != nil {
		t.Errorf("rc file should have been written: %v", err)
	}
}

func TestSetup_DryRun_NoWrite(t *testing.T) {
	home := isolate(t)
	rc := filepath.Join(home, ".zshrc")
	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh", DryRun: true})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !strings.Contains(res.Message, "dry run") {
		t.Errorf("message should say dry run: %q", res.Message)
	}
	if _, err := os.Stat(rc); !os.IsNotExist(err) {
		t.Errorf("dry-run must not create the rc file (err=%v)", err)
	}
}

func TestSetup_DryRun_DoesNotUpdateExistingBlock(t *testing.T) {
	home := isolate(t)
	ctx := context.Background()
	if _, err := Setup(ctx, SetupOptions{Shell: "zsh"}); err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	rc := filepath.Join(home, ".zshrc")
	before := readFile(t, rc)

	res, err := Setup(ctx, SetupOptions{Shell: "zsh", Bind: "^G", DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Setup: %v", err)
	}
	if !strings.Contains(res.Message, "dry run") {
		t.Errorf("message should say dry run: %q", res.Message)
	}
	after := readFile(t, rc)
	if before != after {
		t.Errorf("dry-run modified the rc file")
	}
}

func TestSetup_DetectShellFromEnv(t *testing.T) {
	isolate(t)
	t.Setenv("SHELL", "/usr/local/bin/fish")
	res, err := Setup(context.Background(), SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Shell != "fish" {
		t.Errorf("Shell = %q, want %q", res.Shell, "fish")
	}
}

func TestSetup_BashRCFile(t *testing.T) {
	home := isolate(t)
	res, err := Setup(context.Background(), SetupOptions{Shell: "bash"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := filepath.Join(home, ".bashrc")
	if res.RCFile != want {
		t.Errorf("RCFile = %q, want %q", res.RCFile, want)
	}
	if !strings.Contains(res.Line, `eval "$(shelp init bash)"`) {
		t.Errorf("Line = %q", res.Line)
	}
}

func TestSetup_BashProfileNote_DarwinOnly(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bash_profile note only applies on darwin")
	}
	home := isolate(t) // no .bash_profile in this fresh HOME
	res, err := Setup(context.Background(), SetupOptions{Shell: "bash"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !strings.Contains(res.Message, "bash_profile") {
		t.Errorf("expected a note about ~/.bash_profile not sourcing ~/.bashrc, got: %q", res.Message)
	}
	_ = home
}

func TestSetup_FishRCFile(t *testing.T) {
	home := isolate(t)
	res, err := Setup(context.Background(), SetupOptions{Shell: "fish"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := filepath.Join(home, ".config", "fish", "config.fish")
	if res.RCFile != want {
		t.Errorf("RCFile = %q, want %q", res.RCFile, want)
	}
	if !strings.Contains(res.Line, "shelp init fish | source") {
		t.Errorf("Line = %q", res.Line)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("fish config.fish should have been created (with parent dirs): %v", err)
	}
}

func TestSetup_ZDOTDIR(t *testing.T) {
	isolate(t)
	zdotdir := t.TempDir()
	t.Setenv("ZDOTDIR", zdotdir)
	res, err := Setup(context.Background(), SetupOptions{Shell: "zsh"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := filepath.Join(zdotdir, ".zshrc")
	if res.RCFile != want {
		t.Errorf("RCFile = %q, want %q (ZDOTDIR should win over HOME)", res.RCFile, want)
	}
}

func TestSetup_UnknownShell(t *testing.T) {
	isolate(t)
	if _, err := Setup(context.Background(), SetupOptions{Shell: "powershell"}); err == nil {
		t.Fatal("want error for unknown shell")
	}
}

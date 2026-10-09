package shellinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Marked block shelp appends to an rc file, and looks for on a later run
// to stay idempotent.
const (
	blockStart = "# >>> shelp >>>"
	blockEnd   = "# <<< shelp <<<"
)

// SetupOptions controls `shelp setup shell`.
type SetupOptions struct {
	Shell  string // "" detects from $SHELL
	Bind   string // "" omits --bind from the generated init line
	DryRun bool
}

// SetupResult describes what Setup did (or would do), for the CLI to
// report on stdout.
type SetupResult struct {
	Shell   string
	RCFile  string
	Action  string // "added", "updated", "unchanged", "manual", "already-manual", "active"
	Line    string // the init line that was/would be (in)serted
	Message string // human-readable summary, ready to print
}

// DetectShell returns flagShell if set, else the basename of $SHELL.
func DetectShell(flagShell string) (string, error) {
	if s := strings.ToLower(strings.TrimSpace(flagShell)); s != "" {
		return s, nil
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		return "", errors.New("can't detect your shell: $SHELL is unset; pass --shell zsh|bash|fish")
	}
	return filepath.Base(sh), nil
}

// RCFile returns the rc file shell's integration line belongs in,
// honoring $ZDOTDIR (zsh) and $XDG_CONFIG_HOME (fish) the same way the
// shells themselves do -- this is also what lets tests point Setup at a
// throwaway rc file without touching the real one, by setting HOME/
// ZDOTDIR/XDG_CONFIG_HOME.
func RCFile(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("shellinit: %w", err)
	}
	switch shell {
	case "zsh":
		dir := os.Getenv("ZDOTDIR")
		if dir == "" {
			dir = home
		}
		return filepath.Join(dir, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "fish":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		return filepath.Join(dir, "fish", "config.fish"), nil
	default:
		return "", fmt.Errorf("no shell integration for %q (have: %s)", shell, strings.Join(Shells(), ", "))
	}
}

// initLine is the eval/source line the managed block wraps.
func initLine(shell, bind string) (string, error) {
	switch shell {
	case "zsh", "bash":
		cmd := "shelp init " + shell
		if bind != "" {
			cmd += " --bind " + shellQuote(bind)
		}
		return `eval "$(` + cmd + `)"`, nil
	case "fish":
		cmd := "shelp init fish"
		if bind != "" {
			cmd += " --bind " + shellQuote(bind)
		}
		return cmd + " | source", nil
	default:
		return "", fmt.Errorf("no shell integration for %q (have: %s)", shell, strings.Join(Shells(), ", "))
	}
}

// shellQuote single-quotes s for embedding in a generated rc-file line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Setup implements `shelp setup shell`: detect the shell, find its rc
// file, and append (or update) a marked block that hooks shelp in. It
// never edits an rc file that's a symlink or managed by a dotfiles tool
// (chezmoi) -- it prints the line to add by hand instead, and returns a
// nil error either way (that's a successful outcome, not a failure).
func Setup(ctx context.Context, opts SetupOptions) (SetupResult, error) {
	shell, err := DetectShell(opts.Shell)
	if err != nil {
		return SetupResult{}, err
	}
	rc, err := RCFile(shell)
	if err != nil {
		return SetupResult{}, err
	}
	line, err := initLine(shell, opts.Bind)
	if err != nil {
		return SetupResult{}, err
	}
	res := SetupResult{Shell: shell, RCFile: rc, Line: line}

	// The integration exports SHELP_HOOKS when it loads, so a hook that
	// lives somewhere other than the rc file itself (a sourced snippet, a
	// dotfiles module) still counts as set up.
	if opts.Bind == "" && hookActive(shell) {
		res.Action = "active"
		res.Message = fmt.Sprintf("shelp is already hooked into %s (its integration is loaded in this shell); nothing to do", shell)
		return res, nil
	}

	if reason := ManagedElsewhere(ctx, rc); reason != "" {
		res.Action = "manual"
		res.Message = fmt.Sprintf("%s is %s -- add this line yourself:\n  %s", rc, reason, line)
		return res, nil
	}

	data, err := os.ReadFile(rc)
	if err != nil && !os.IsNotExist(err) {
		return SetupResult{}, fmt.Errorf("shellinit: read %s: %w", rc, err)
	}
	content := string(data)

	if br, ok := findBlock(content); ok {
		if opts.Bind == "" {
			res.Action = "unchanged"
			res.Message = fmt.Sprintf("%s is already set up for %s (%s); pass --bind to change its keybinding", rc, shell, rc)
			return res, nil
		}
		newContent := content[:br.start] + block(line) + content[br.end:]
		res.Action = "updated"
		res.Message = fmt.Sprintf("updated the shelp block in %s", rc)
		if opts.DryRun {
			res.Message = "[dry run] would update the shelp block in " + rc + ":\n  " + line
			return res, nil
		}
		if err := writeFile(rc, newContent); err != nil {
			return SetupResult{}, err
		}
		return res, nil
	}

	if strings.Contains(content, "shelp init") {
		res.Action = "already-manual"
		res.Message = fmt.Sprintf("%s already has a `shelp init` line (not one shelp added) -- leaving it alone", rc)
		return res, nil
	}

	var newContent string
	switch {
	case content == "":
		newContent = block(line)
	case strings.HasSuffix(content, "\n"):
		newContent = content + block(line)
	default:
		newContent = content + "\n" + block(line)
	}
	res.Action = "added"
	res.Message = fmt.Sprintf("added shelp to %s", rc)
	if opts.DryRun {
		res.Message = "[dry run] would add to " + rc + ":\n" + block(line)
		return res, nil
	}
	if err := writeFile(rc, newContent); err != nil {
		return SetupResult{}, err
	}
	if shell == "bash" && runtime.GOOS == "darwin" {
		if note := bashProfileNote(); note != "" {
			res.Message += "\n" + note
		}
	}
	return res, nil
}

func block(line string) string {
	return blockStart + "\n" + line + "\n" + blockEnd + "\n"
}

type blockRange struct{ start, end int }

// findBlock locates shelp's marked block in content, if present. end
// covers the markers and a single trailing newline, so content[start:end]
// can be sliced out and replaced wholesale.
func findBlock(content string) (blockRange, bool) {
	si := strings.Index(content, blockStart)
	if si == -1 {
		return blockRange{}, false
	}
	rest := content[si:]
	ei := strings.Index(rest, blockEnd)
	if ei == -1 {
		return blockRange{}, false
	}
	end := si + ei + len(blockEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return blockRange{start: si, end: end}, true
}

// ManagedElsewhere reports why rc should not be edited directly -- a
// symlink (stow/yadm-style dotfiles), or a chezmoi source (skipped
// entirely when chezmoi isn't on PATH) -- or "" when it's fine to edit.
func ManagedElsewhere(ctx context.Context, rc string) string {
	if fi, err := os.Lstat(rc); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(rc)
		if target != "" {
			return fmt.Sprintf("a symlink (-> %s)", target)
		}
		return "a symlink"
	}
	if _, err := exec.LookPath("chezmoi"); err == nil {
		cmd := exec.CommandContext(ctx, "chezmoi", "source-path", rc)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if cmd.Run() == nil {
			return "managed by chezmoi"
		}
	}
	return ""
}

// bashProfileNote checks whether ~/.bash_profile sources ~/.bashrc, since
// macOS's Terminal.app starts a login shell (reading .bash_profile, not
// .bashrc) by default. It never edits .bash_profile -- only reports.
func bashProfileNote() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	profile := filepath.Join(home, ".bash_profile")
	data, err := os.ReadFile(profile)
	if err != nil {
		if os.IsNotExist(err) {
			return "note: ~/.bash_profile doesn't exist, so login bash (macOS Terminal's default) won't read ~/.bashrc -- add:\n  [ -r ~/.bashrc ] && . ~/.bashrc"
		}
		return ""
	}
	if strings.Contains(string(data), ".bashrc") {
		return ""
	}
	return "note: ~/.bash_profile doesn't source ~/.bashrc, so login bash (macOS Terminal's default) won't see this -- add:\n  [ -r ~/.bashrc ] && . ~/.bashrc"
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("shellinit: %w", err)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode()
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("shellinit: write %s: %w", path, err)
	}
	return nil
}

// hookActive reports whether this process inherited SHELP_HOOKS naming
// shell, i.e. `shelp init <shell>` ran in the shell that launched us.
func hookActive(shell string) bool {
	for _, h := range strings.Fields(os.Getenv("SHELP_HOOKS")) {
		if h == shell {
			return true
		}
	}
	return false
}

// HookInstalled reports whether shelp's hook is already set up for shell
// ("" detects from $SHELL): loaded in this shell (SHELP_HOOKS), or a
// `shelp init` line in its rc file. rc is that file's path.
func HookInstalled(shell string) (installed bool, rc string, err error) {
	shell, err = DetectShell(shell)
	if err != nil {
		return false, "", err
	}
	rc, err = RCFile(shell)
	if err != nil {
		return false, "", err
	}
	if hookActive(shell) {
		return true, rc, nil
	}
	data, _ := os.ReadFile(rc)
	return strings.Contains(string(data), "shelp init"), rc, nil
}

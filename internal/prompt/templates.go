package prompt

import (
	"strings"

	"github.com/ericandrechek/shelp/internal/provider"
)

// Default is the template the CLI uses. Change it only on benchmark
// evidence; the README table records why.
const Default = "v1"

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

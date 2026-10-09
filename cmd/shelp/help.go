package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/shellinit"
	"github.com/ericandrechek/shelp/internal/style"
)

// Every help text is plain: a one-line description, then sections whose
// headings are unindented single capitalized words. printHelp adds the
// logo (top level only) and colors on a terminal.

const usage = `shelp turns a task into a shell command, or explains one.

Usage
  shelp <task…>              suggest a command
  shelp explain <command…>   explain one
  shelp <command> [flags]

Commands
  explain    explain a command (alias -e)
  providers  list model providers and whether they work
  setup      set up the shell hook or the Apple Shortcut
  init       print the shell integration for your rc file
  version    print the version (also --version)
  help       show help for a command

Flags
  -p, --provider IDS   providers to try, in order (default: config "providers")
  -v, --verbose        show which provider answered, and how fast
  -h, --help           show help

Run "shelp help <command>" for a command's flags.
`

const generateFlags = `
Flags
  -p, --provider IDS   providers to try, in order, e.g. fm,claude:haiku
                       (env SHELP_PROVIDER; default: config "providers")
  --shell NAME         target shell (default: shell integration, else $SHELL)
  --template NAME      prompt template (default: ` + prompt.Default + `)
  --context FIELDS     what to tell the model: os,shell,cwd,tools
                       (default: os,shell)
  --json               print {"text","provider","latency_ms"} instead of text
  -v, --verbose        show which provider answered, and how fast
  --force              call a provider even if it is in a rate-limit cooldown
  -h, --help           show help
`

// commandHelp maps a command name to its help text.
var commandHelp = map[string]string{
	"suggest": `shelp suggest: turn a task into a shell command.

Usage
  shelp <task…>
  shelp suggest [flags] <task…>

Prints only the command. Words that do not start with a command name
(or a flag) are the task, so "suggest" is optional.
` + generateFlags,

	"explain": `shelp explain: explain a shell command.

Usage
  shelp explain [flags] <command…>
  shelp -e [flags] <command…>
` + generateFlags,

	"providers": `shelp providers: list model providers and whether they work.

Usage
  shelp providers [flags]

Flags
  --all, -v, --verbose  include providers that are not set up
  --json                machine-readable output
  --reset-cooldowns     clear all recorded rate-limit cooldowns
  -h, --help            show help
`,

	"setup": `shelp setup: set up the shell hook or the Apple Shortcut.

Usage
  shelp setup shell [flags]   add shelp's hook to your shell rc file
  shelp setup shortcuts       install the Apple Shortcut the shortcuts:*
                              providers run

Flags (setup shell)
  --shell NAME   zsh, bash, or fish (default: from $SHELL)
  --bind KEY     also bind shelp-widget to KEY, e.g. '^G'
  --dry-run      print what would change without writing it
  -h, --help     show help
`,

	"init": `shelp init: print the shell integration for your rc file.

Usage
  shelp init <` + strings.Join(shellinit.Shells(), "|") + `> [flags]

Add this to your rc file (or run "shelp setup shell" to do it for you):
  eval "$(shelp init zsh)"

Flags
  --bind KEY       bind shelp-widget to KEY: caret notation like "^G", or a
                   shell-native escape (also config "[shell] bind")
  --no-line-mode   do not intercept Enter on lines starting with "shelp "
                   (also config "[shell] line_mode = false")
  -h, --help       show help
`,

	"version": `shelp version: print the version.

Usage
  shelp version
  shelp --version
`,

	"help": `shelp help: show help for a command.

Usage
  shelp help [command]
  shelp <command> -h

Commands
  suggest, explain, providers, setup, init, version, help
`,
}

// helpTopic returns the help text for a command name, and false if the
// name is not one. "-e" is explain's alias; "" is the top level.
func helpTopic(name string) (string, bool) {
	switch name {
	case "":
		return usage, true
	case "-e":
		name = "explain"
	}
	s, ok := commandHelp[name]
	return s, ok
}

// printHelp writes the help for name ("" is the top level) to w. On a
// terminal the top level gets the logo and every help gets colored
// headings; anywhere else the text is exactly as written above.
func printHelp(w io.Writer, name string) {
	text, ok := helpTopic(name)
	if !ok {
		text = usage
		name = ""
	}
	p := style.For(w)
	if !p.On() {
		fmt.Fprint(w, text)
		return
	}
	if name == "" {
		fmt.Fprint(w, style.Logo(p)+"\n\n")
	}
	fmt.Fprint(w, styleHelp(p, text))
}

func printUsage(w io.Writer) { printHelp(w, "") }

// isHeading reports whether a help line is a section heading: unindented,
// a capitalized first word, and nothing but letters and spaces after it,
// optionally followed by a parenthesized qualifier ("Flags (setup shell)").
func isHeading(l string) bool {
	if l == "" || !unicode.IsUpper(rune(l[0])) {
		return false
	}
	if i := strings.Index(l, " ("); i >= 0 {
		if !strings.HasSuffix(l, ")") {
			return false
		}
		l = l[:i]
	}
	for _, r := range l {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// styleHelp colors a help text's section headings and the command name
// leading its first line, changing nothing else.
func styleHelp(p style.Palette, u string) string {
	lines := strings.Split(u, "\n")
	for i, l := range lines {
		switch {
		case i == 0 && strings.HasPrefix(l, "shelp"):
			rest := strings.TrimPrefix(l, "shelp")
			if j := strings.Index(rest, ":"); j >= 0 { // "shelp explain: ..."
				lines[i] = p.Bold(p.Accent("shelp"+rest[:j+1])) + rest[j+1:]
			} else {
				lines[i] = p.Bold(p.Accent("shelp")) + rest
			}
		case isHeading(l):
			lines[i] = p.Bold(p.Accent(l))
		}
	}
	return strings.Join(lines, "\n")
}

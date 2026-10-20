package shellinit

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed completions/*.tmpl
var completionScripts embed.FS

// CompletionCommand is a subcommand offered by tab completion.
type CompletionCommand struct{ Name, Desc string }

// CompletionCommands are the subcommands completion offers at the first
// word, in order. `suggest` is left out on purpose (it is implicit) but
// its flags are known: see completionFlags.
var CompletionCommands = []CompletionCommand{
	{"explain", "explain a command"},
	{"providers", "list model providers and whether they work"},
	{"history", "show your recent requests, answers and models"},
	{"setup", "set up the shell hook or the Apple Shortcut"},
	{"init", "print the shell integration for your rc file"},
	{"version", "print the version"},
	{"help", "show help for a command"},
	{"config", "show or edit the config file"},
	{"completion", "print the tab-completion script for a shell"},
}

// valueFlagOrder lists the flags that take a value. Their values complete
// by name in the scripts: providers (-p), shells (--shell), templates,
// context fields; --bind is free text.
var valueFlagOrder = []string{"-p", "--provider", "--shell", "--template", "--context", "--bind"}

// completionFlags is what each command's flags complete to. "" is the
// top level (the suggest flags plus -e and --version); "setup shell" is
// the flags of `shelp setup shell`, "config add" of `shelp config add`
// (whose subcommands are endpoint and local; config's own are chain,
// edit, add and remove). A test checks these against help.go.
var completionFlags = map[string][]string{
	"":            {"-p", "--provider", "--shell", "--template", "--context", "--json", "-v", "--verbose", "--force", "-h", "--help", "-e", "--version"},
	"suggest":     {"-p", "--provider", "--shell", "--template", "--context", "--json", "-v", "--verbose", "--force", "-h", "--help"},
	"explain":     {"-p", "--provider", "--shell", "--template", "--context", "--json", "-v", "--verbose", "--force", "-h", "--help"},
	"providers":   {"--all", "-v", "--verbose", "--json", "--reset-cooldowns", "-i", "--interactive", "-h", "--help"},
	"history":     {"-n", "--all", "-s", "--search", "--provider", "--json", "-h", "--help"},
	"setup":       {"-h", "--help"},
	"setup shell": {"--shell", "--bind", "--dry-run", "-h", "--help"},
	"init":        {"--bind", "--no-line-mode", "-h", "--help"},
	"completion":  {"-h", "--help"},
	"config":      {"-h", "--help"},
	"config add":  {"--preset", "--base-url", "--key-env", "--model", "--label", "-h", "--help"},
	"help":        {"-h", "--help"},
	"version":     {"-h", "--help"},
}

// completionSubcommands are the positional subcommands of a command, which
// complete at its first argument. Value-typed arguments (the shells of
// init and completion, provider IDs, templates) are not here: the scripts
// know those by kind. To give a new command subcommands, add it here; the
// scripts ask the binary (`shelp __complete subcommands CMD`) at Tab time.
var completionSubcommands = map[string][]string{
	"setup":  {"shell", "shortcuts"},
	"config": {"chain", "edit"},
}

// CompletionSubcommands returns the subcommands completion offers for a
// command.
func CompletionSubcommands(cmd string) []string { return completionSubcommands[cmd] }

// CompletionFlagsFor resolves a command path as `shelp __complete flags`
// receives it ("", "explain", "setup shell", "help config"): the flags of
// the longest prefix of words that has an entry, so a positional argument
// (`help config`) falls back to its command. nil when even the first word
// is no command: a task completes nothing.
func CompletionFlagsFor(words []string) []string {
	if len(words) == 0 {
		return completionFlags[""]
	}
	for n := len(words); n >= 1; n-- {
		if f, ok := completionFlags[strings.Join(words[:n], " ")]; ok {
			return f
		}
	}
	return nil
}

// CompletionFlags returns the flags completion offers for a command ("" is
// the top level, "setup shell" the setup shell flags).
func CompletionFlags(cmd string) []string { return completionFlags[cmd] }

type completionView struct {
	// Commands is the fallback command list, used only when the binary
	// does not answer `__complete commands` (a shelp from before it did).
	Commands []CompletionCommand
	// ValueFlags is the shell `case` pattern of every value-taking flag
	// ("-p|--provider|..."); ValueFlagWords the same space-separated.
	ValueFlags     string
	ValueFlagWords string
	Shells         string
	Context        string
	Names          string // fallback command names, space-separated
}

func join(s []string) string { return strings.Join(s, " ") }

// Completion renders shell's tab-completion script: the text `shelp
// completion <shell>` prints and `shelp init <shell>` embeds.
func Completion(shell string) (string, error) {
	shell = strings.ToLower(strings.TrimSpace(shell))
	data, err := completionScripts.ReadFile("completions/" + shell + ".tmpl")
	if err != nil {
		return "", fmt.Errorf("no completion for %q (have: %s)", shell, strings.Join(Shells(), ", "))
	}
	v := completionView{
		Commands:       CompletionCommands,
		ValueFlags:     strings.Join(valueFlagOrder, "|"),
		ValueFlagWords: join(valueFlagOrder),
		Shells:         join(Shells()),
		Context:        "os shell cwd tools",
	}
	var names []string
	for _, c := range CompletionCommands {
		names = append(names, c.Name)
	}
	v.Names = join(names)
	t, err := template.New(shell).Delims("⟦", "⟧").Funcs(template.FuncMap{"sq": shellQuote, "fq": fishQuote}).Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("shellinit: parse %s completion: %w", shell, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, v); err != nil {
		return "", fmt.Errorf("shellinit: render %s completion: %w", shell, err)
	}
	return b.String(), nil
}

// fishQuote single-quotes s for fish, where only \' and \\ are escapes.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

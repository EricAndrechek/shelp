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
  providers  list model providers and whether they work (-i: manage them)
  history    show your recent requests, answers and models
  setup      guided setup: shell hook, Apple Shortcut, provider chain
  config     show or change settings (the provider chain, routes, the file)
  init       print the shell integration for your rc file
  completion print the tab-completion script for a shell
  version    print the version (also --version)
  help       show help for a command

Flags
  -p, --provider IDS   providers to try, in order (default: config "providers")
  -v, --verbose        show which provider answered, and how fast
  -h, --help           show help

Run "shelp help <command>" for a command's flags, "shelp help config" for every setting.
`

const generateFlags = `
Flags
  -p, --provider IDS   providers to try, in order, e.g. fm,claude:haiku
                       (env SHELP_PROVIDER; default: config "providers")
  --shell NAME         target shell (default: shell integration, else $SHELL)
  --template NAME      prompt template (default: ` + prompt.Default + `)
  --context FIELDS     what to tell the model: os,shell,cwd,tools
                       (default: os,shell)
  --json               print {"text","provider","latency_ms",...} instead of text
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
  -i, --interactive     open the provider manager: reorder the chain, set
                        up providers, add your own routes (needs a terminal;
                        see "shelp help config")
  -h, --help            show help

How they differ (scores are from shelp's benchmark)
  fm            on this Mac: offline, private, fastest, and the weakest
                (31-36%).
  Apple Intelligence, Apple Intelligence Pro
                both run on Apple's Private Cloud Compute; Pro is the larger
                model (98%). Apple sets a daily usage limit.
  ChatGPT       OpenAI's model, through Apple's ChatGPT extension, which
                must be enabled; it asks once to share your request.
  -web          lets the model look things up online. It helps questions about
                current things, not shell commands, and is slower.
  claude        Anthropic models through your Claude Code login, run headless
                with no tools; it counts against your plan (Sonnet ~98%,
                Haiku ~84%).
  local servers, API endpoints
                whatever model you run or pay for.
`,

	"history": `shelp history: show your recent requests, answers and models.

Usage
  shelp history [flags]    the last 20 entries, newest last
  shelp history show N     one entry in full (N as numbered in the list)
  shelp history clear      delete the history (asks first; --yes skips that)
  shelp history path       print the history file's location

Each suggest or explain call is appended to a local file: the time, the
mode, the input, the answer, the provider that gave it, how long it took and
(when the backend reports it) tokens. Nothing else is stored: no directory,
no environment. The file is readable only by you.

Flags
  -n N               how many entries to list (default 20)
  --all              list every entry
  -s, --search TEXT  only entries whose input or answer contains TEXT
                     (case-insensitive)
  --provider ID      only entries answered by this provider
  --json             print the raw records, one JSON object per line
  -h, --help         show help

Settings (config "[history]")
  enabled = false    do not record anything
  max_entries = 1000 keep this many entries (older ones are dropped)
  SHELP_HISTORY=0 in the environment turns recording off for that command.
`,

	"setup": `shelp setup: set up the shell hook or the Apple Shortcut.

Usage
  shelp setup                 on a terminal, a guided walk through the shell
                              hook, the Apple Shortcut (macOS) and the
                              provider chain; each step can be skipped
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

	"completion": `shelp completion: print the tab-completion script for a shell.

Usage
  shelp completion <` + strings.Join(shellinit.Shells(), "|") + `>

The shell hook ("shelp init") and Homebrew already set this up. Otherwise:
  zsh:   shelp completion zsh > "${fpath[1]}/_shelp"
  bash:  shelp completion bash > ~/.local/share/bash-completion/completions/shelp
  fish:  shelp completion fish > ~/.config/fish/completions/shelp.fish

Flags
  -h, --help   show help
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
  suggest, explain, providers, history, setup, config, init, completion, version, help
`,

	"config": `shelp config: show or change settings.

Usage
  shelp config                 the config file and the effective settings
  shelp config chain [IDS]     set the provider chain: IDS (comma or space
                               separated) saves it; with none, on a terminal,
                               open the provider manager (below), and
                               otherwise list every ID and these commands
  shelp config add endpoint NAME (--preset P | --base-url URL) --model M
                               add a hosted OpenAI-compatible endpoint
  shelp config add local NAME --base-url URL
                               add a local OpenAI-compatible server
  shelp config add ... --replace
                               replace the endpoint or local server NAME
                               in place (or add it if there is none)
  shelp config remove NAME     remove an endpoint or local server you added
  shelp config edit            open the file in $VISUAL / $EDITOR / vi,
                               creating it with a commented starter

Flags (config add)
  --preset P       fills in the base URL and the key variable: openai,
                   openrouter, groq, gemini, anthropic, xai, mistral,
                   deepseek, cerebras
  --base-url URL   the API's base URL (needed without a preset)
  --key-env VAR    the NAME of the environment variable that holds the key
                   (default: the preset's, else NAME_API_KEY; "" for none);
                   shelp never takes or stores a key itself
  --model M        a model ID as the API names it; repeat, or comma-separate
  --label TEXT     how it reads in lists, e.g. "Haiku via OpenRouter"
  --replace        NAME is already in the config: replace it where it is,
                   keeping its comments and any other keys; provider IDs
                   that go away are taken out of the chain
  -h, --help       show help

Provider manager (shelp providers -i, or shelp config chain on a terminal)
  Your chain on top, numbered in the order it is tried, then every other
  provider: available, needing setup, unavailable. "local" runs on this
  machine and works offline; "cloud" needs the internet. The bottom line
  says what the keys do on the row under the cursor; ? shows them all.
  ↑↓ (or k/j) move; space puts a provider in the chain or takes it out;
  shift+↑↓ (or K/J, +/-) move a chain entry; → (or enter, l) opens a
  provider's details: where it runs, its status, its route and how to make
  it work (enter there runs the Apple Shortcut's setup), ← (or esc) goes
  back and ↑↓ show the next one; a adds a route; e edits one you added, in
  place (renamed or dropped IDs follow in the chain); x removes one; s
  saves; q or esc quits, asking first about unsaved changes; ctrl+c quits
  at once without saving.

chain, add and remove change only their own lines and leave the rest of the
file as it is. If the file is managed by chezmoi or is a symlink, shelp does
not write it and prints the change instead; on a terminal, when chezmoi's
source for it is a plain file, it offers to make the change there and run
"chezmoi apply".

File
  $SHELP_CONFIG                         if set, it must exist
  $XDG_CONFIG_HOME/shelp/config.toml    else this, if XDG_CONFIG_HOME is set
  ~/.config/shelp/config.toml           else this; missing means defaults
  Every key and table is optional. Each provider family reads only its own
  table.

Keys
  providers = ["shortcuts:apple-pro", "claude:sonnet", "fm"]
      Fallback chain, tried in order; the first success wins. -p and
      $SHELP_PROVIDER override it. Default: ["shortcuts:apple-pro", "fm"].
  template = "` + prompt.Default + `"
      Prompt template: v1 (minimal), v2 (rules), v3 (v2 + few-shot), v4
      (v2 with Markdown explanations). Default: ` + prompt.Default + `.
  context = ["os", "shell"]
      What to tell the model: os, shell, cwd, tools. cwd and tools are
      opt-in because they leave the machine with a cloud provider.
  cooldown = "30m"
      How long a rate-limited provider is skipped (a Go duration). Default 30m.

[shortcuts]
  name = "shelp"
      The Apple Shortcut every shortcuts:* provider runs. Default "shelp".

[shell]
  bind = "^G"
      Key bound to shelp-widget. "shelp init --bind" wins.
  line_mode = true
      Intercept Enter on lines starting with "shelp ". Default true.

[history]
  enabled = true
      Record each request ("shelp help history"). Default true.
  max_entries = 1000
      How many entries to keep. Default 1000.

[[openai.endpoint]]   (repeatable; hosted, key-authenticated)
  name = "openrouter"
      Prefix of this endpoint's provider IDs, "<name>:<model>".
  preset = "openrouter"
      Fills base_url and api_key_env: openai, openrouter, groq, gemini,
      anthropic, xai, mistral, deepseek, cerebras.
  base_url = "https://llm.example.com/v1"
      Required without a preset; overrides one.
  api_key_env = "WORK_LLM_API_KEY"
      Name of the environment variable holding the key.
  models = ["gpt-4o-mini"]
      One provider ID per entry.
  temperature = 0.2
      Sampling temperature. Default 0.
  label = "Haiku via OpenRouter"
      How its providers read in lists: as is for a one-model endpoint,
      "<label> · <model>" for each model of several.
  labels = { "anthropic/claude-haiku-4.5" = "Haiku via OpenRouter" }
      A label per model; it wins over label.

[[openai.local]]   (repeatable; local OpenAI-compatible servers)
  name = "fm-serve"
  base_url = "http://127.0.0.1:11535/v1"
      Adds to or overrides the built-ins: lmstudio :1234, ollama :11434,
      llamacpp :8080.
  label = "fm serve"
      How the server reads in lists.

[agentcli.claude]
  models = ["haiku", "opus"]
      Replaces the built-in variants. A built-in id like "haiku" keeps its
      flag value; any other string is passed to --model as is.
  disabled = true
      Drop the claude providers even when the binary is installed.

Environment
  SHELP_PROVIDER    provider chain, like -p (overrides config "providers")
  SHELP_CONFIG      path of the config file
  SHELP_HISTORY     0 turns request history off
  XDG_STATE_HOME    where history.jsonl lives (default ~/.local/state)
  NO_COLOR          turn off colors
  CLICOLOR_FORCE    keep colors when output is not a terminal
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
		if !unicode.IsLetter(r) && r != ' ' {
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

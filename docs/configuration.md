# Configuration and reference

Everything here is optional. `shelp help <command>` has the same
information for each command.

## The config file

shelp reads `$SHELP_CONFIG`, else `$XDG_CONFIG_HOME/shelp/config.toml`, else
`~/.config/shelp/config.toml`. A missing file means the defaults. A
`$SHELP_CONFIG` that points at a missing file is an error.

`shelp config` shows the file and the settings in effect. `shelp config
edit` opens it in `$VISUAL` or `$EDITOR`, creating a commented starter if
there is none. The commands below change only their own lines and keep
your comments.

```toml
# Providers to try, in order. The first that answers wins.
# Overridden by -p/--provider, then $SHELP_PROVIDER.
# Default: ["shortcuts:apple-pro", "fm"]
providers = ["shortcuts:apple-pro", "claude:sonnet", "fm"]

# Prompt template: v4 (default), v2, v1 or v3.
template = "v4"

# What the model is told about your environment. "cwd" and "tools" are
# opt-in because they leave your machine with a cloud provider.
context = ["os", "shell"]

# How long a rate-limited provider is skipped. Default: 30m.
cooldown = "30m"

# A hosted OpenAI-compatible endpoint. `preset` fills in base_url and
# api_key_env: openai, openrouter, groq, gemini, anthropic, xai, mistral,
# deepseek, cerebras. Without one, set both yourself.
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"
models = ["anthropic/claude-haiku-4.5", "meta-llama/llama-3.1-70b-instruct"]
label = "OpenRouter"                          # optional
labels = { "anthropic/claude-haiku-4.5" = "Haiku via OpenRouter" }

[[openai.endpoint]]
name = "work"
base_url = "https://llm.internal.example.com/v1"
api_key_env = "WORK_LLM_API_KEY"
models = ["gpt-4o-mini"]
temperature = 0.2                             # default 0

# A local OpenAI-compatible server, in addition to the built-in ones:
# lmstudio (:1234), ollama (:11434), llamacpp (:8080).
[[openai.local]]
name = "fm-serve"
base_url = "http://127.0.0.1:11535/v1"
label = "fm serve"

# The Apple Shortcut every shortcuts:* provider runs.
[shortcuts]
name = "shelp"

# The claude CLI. `models` replaces the built-in haiku and sonnet;
# `disabled = true` hides it even when installed.
[agentcli.claude]
models = ["haiku", "sonnet", "opus"]

[shell]
bind = "^G"           # a key for shelp-widget
line_mode = true      # raw line mode, see below

[history]
enabled = true
max_entries = 1000
```

A route's provider IDs are `<name>:<model>`, such as
`openrouter:anthropic/claude-haiku-4.5`. Its label is shown in lists:
`label` as is for a one-model route, or `<label> · <model>` for each of
several; an entry in `labels` wins for its model.

## Your own routes

```sh
shelp config add endpoint openrouter --preset openrouter \
  --model anthropic/claude-haiku-4.5 --label "Haiku via OpenRouter"
shelp config add endpoint work --base-url https://llm.internal.example.com/v1 \
  --key-env WORK_LLM_API_KEY --model gpt-4o-mini
shelp config add local fm-serve --base-url http://127.0.0.1:11535/v1
shelp config add endpoint work ... --replace    # change an existing route
shelp config remove work
```

shelp never asks for or stores an API key. `--key-env` names the
environment variable that holds it; the default is the preset's variable
(such as `OPENROUTER_API_KEY`), else `NAME_API_KEY`, and `--key-env ""`
sends no key. shelp tells you whether the variable is set, never its value.

`add` appends the route to the file. On a terminal it offers to add the
new providers to your chain; otherwise it prints the `shelp config chain`
command that would. `--replace` edits a route in place, and `remove`
deletes it. Either way, provider IDs that no longer exist leave your chain.

If the config file is a symlink or managed by chezmoi, shelp doesn't write
it. It prints the change and the commands to apply it, and on a terminal
it offers to edit chezmoi's source file and run `chezmoi apply` for you.

## The provider manager

`shelp providers -i`, `shelp config -i`, or `shelp config chain` with no
IDs opens a full-screen list: your chain at the top in the order it is
tried, then every other provider. Each row says whether it is local (runs
on your machine and works offline) or cloud, and whether it works here.
The bottom line shows the keys for the current row; `?` lists them all.

| Key | Action |
|---|---|
| ↑ ↓ | move |
| space | add to the end of the chain, or remove |
| shift+↑ ↓ | move a chain entry |
| → or enter | details: where it runs, why it doesn't work, how to fix it |
| `a`, `e`, `x` | add, edit or remove one of your routes |
| `r` | check every provider again |
| `s` | save |
| `q`, esc | quit (asks first if there are unsaved changes) |
| ctrl+c | quit without saving |

Nothing is written until you press `s`. Without a terminal, `shelp config
chain` prints the provider IDs and the commands to use instead.

## Shell integration

`shelp setup shell` adds one line to your shell's rc file (`~/.zshrc`,
`~/.bashrc` or `~/.config/fish/config.fish`) inside a marked block. It
won't add a second one, and it won't edit an rc file that is a symlink or
managed by chezmoi; it prints the line for you instead. To add it by hand:

```sh
eval "$(shelp init zsh)"     # ~/.zshrc
eval "$(shelp init bash)"    # ~/.bashrc
shelp init fish | source     # ~/.config/fish/config.fish
```

On macOS, bash login shells read `~/.bash_profile`, so make sure it
sources `~/.bashrc`.

The hook defines a `shelp` function that puts the suggested command on
your next prompt line instead of printing it.

**Raw line mode** (on by default): when you press Enter on a line that
starts with `shelp` followed by a task, the hook sends the rest of the
line to shelp exactly as typed, so apostrophes, globs, `$vars` and quotes
need no escaping. The suggestion replaces the line, and the line you typed
goes into your history. Lines with flags first (`shelp -p fm ...`) and
shelp's own commands (`shelp providers`) run normally. Turn it off with
`shelp init <shell> --no-line-mode` or `[shell] line_mode = false`. When
it's off, quote tasks that contain an apostrophe, and in bash and fish
quote tasks that contain `*`, `?` or `[`.

**Widget**: `shelp-widget` turns the text on your command line into a
command in place. Bind it with `shelp setup shell --bind '^G'`,
`shelp init <shell> --bind '^G'`, or `[shell] bind`.

## Tab completion

The shell hook and the Homebrew formula set up completion for commands,
flags and provider IDs. Without them:

```sh
shelp completion zsh > "${fpath[1]}/_shelp"
shelp completion bash > ~/.local/share/bash-completion/completions/shelp
shelp completion fish > ~/.config/fish/completions/shelp.fish
```

## Rate limits and offline

When a provider reports a rate limit or exhausted quota, shelp skips it
until the backend's `Retry-After` or the `cooldown` setting passes. This
matters for the Apple Shortcut, because every failed Shortcuts run posts a
macOS notification. `shelp providers` shows the cooldown;
`shelp providers --reset-cooldowns` clears it, and `--force` calls a
provider anyway.

With no network route, shelp skips cloud providers and tries only local
ones, so keep one (`fm`, a local server) in your chain. A network that is
connected but has no internet isn't detected and falls back to timeouts.

## History

```sh
shelp history                  # the last 20
shelp history -s tar -n 50     # search inputs and answers
shelp history --provider fm --all
shelp history show 12          # one entry in full
shelp history --json           # one JSON record per line
shelp history clear
```

The file is `$XDG_STATE_HOME/shelp/history.jsonl`
(`~/.local/state/shelp/history.jsonl` by default), readable only by you.
Each record has the time, your input, the answer, the provider, the
latency, token counts when the provider reports them, and any providers
that failed first. Your working directory and environment are never
saved. shelp keeps the newest `max_entries` records. Cancelled requests
aren't recorded; `SHELP_HISTORY=0` turns recording off for one shell.

## Colors

Output is colored only on a terminal. `NO_COLOR=1` turns colors off and
`CLICOLOR_FORCE=1` forces them on. The suggested command, `--json` output
and `shelp init` are never colored.

# shelp

shelp turns a task into a shell command, or explains one, using whatever
model you already have on your machine: Apple's on-device model (`fm`),
Apple's cloud models via Shortcuts (Apple Intelligence Pro, ChatGPT),
OpenAI-compatible endpoints (hosted with an API key, or local servers like
LM Studio, Ollama, or `llama-server`), or installed AI CLIs. It tries
providers in order and falls back automatically, so it keeps working when
one backend is offline, unlicensed, or rate-limited. It's the successor to
the `ghcs`/`ghce` shell functions some of us grew up with.

```console
$ shelp find all files over 100MB in my home dir
find ~ -type f -size +100M

$ shelp explain 'lsof -nP -iTCP:8080 -sTCP:LISTEN'
Lists processes with a listening TCP socket on port 8080; -n and -P skip
DNS and service-name lookups for speed.
```

Status: early, local-only (no remote for this repo yet).

## Install

```sh
make install                 # -> ~/.local/bin/shelp
eval "$(shelp init zsh)"     # add to ~/.zshrc
eval "$(shelp init bash)"    # add to ~/.bashrc, or:
shelp init fish | source     # add to ~/.config/fish/config.fish

# optional: keep the old ghcs/ghce muscle memory
alias ghcs=shelp
alias ghce='shelp explain'
```

The shell integration wraps `shelp` in a function (zsh/bash/fish, in the
style of zoxide/starship) so a suggested command lands on your prompt line
ready to edit or run — shelp never executes anything itself.

## Usage

```console
$ shelp find all files over 100MB in my home dir       # suggest (default)
$ shelp explain 'tar -xzvf a.tgz'                       # explain (alias: -e)
$ shelp -p fm,shortcuts:apple-pro list 5 recent commits # explicit fallback chain
$ shelp --template v2 -v print yesterday as YYYY-MM-DD  # -v: provider+latency on stderr
$ shelp providers                                       # what works on this machine
$ shelp providers --json                                # same, machine-readable
$ shelp providers --reset-cooldowns                     # clear recorded rate-limit cooldowns
$ shelp setup shortcuts                                 # install the Apple Shortcuts this needs
```

Flags for `suggest`/`explain`: `-p`/`--provider` (or `$SHELP_PROVIDER`, or
config `providers`), `--shell`, `--template`, `--context os,shell,cwd,tools`,
`--json`, `-v`/`--verbose`, `--force` (call a provider even if it's
currently in a rate-limit cooldown). Run `shelp -h` for the full usage.

## Providers

| ID pattern | kind | needs | setup |
|---|---|---|---|
| `fm` | on-device | macOS 27+, Apple Intelligence enabled | `sudo fm license` (once, machine-wide) |
| `shortcuts:apple`, `shortcuts:apple-web`, `shortcuts:apple-pro`, `shortcuts:apple-pro-web`, `shortcuts:chatgpt`, `shortcuts:chatgpt-web` | apple-cloud | macOS, the Shortcuts app + `shortcuts` CLI | `shelp setup shortcuts` generates and opens each `.shortcut`; click "Add Shortcut" once per shortcut (no import API exists) |
| `<endpoint-name>:<model>`, e.g. `openai:gpt-4o-mini`, `openrouter:anthropic/claude-3.5-haiku` | api | an `[[openai.endpoint]]` config entry and its API key env var | add the config entry below, `export <KEY>_API_KEY=...` |
| `lmstudio:<model>`, `ollama:<model>`, `llamacpp:<model>`, or a name from `[[openai.local]]` | local-server | a local OpenAI-compatible server running on the expected port | start the server (LM Studio, `ollama serve`, `llama-server`, `fm serve`, ...) |
| `claude:haiku`, `claude:sonnet`, `gemini:default`, `codex:default`, `copilot:default` | agent-cli | the CLI installed and logged in (`claude`, `gemini`, `codex`, `copilot`) | log in with each CLI's own command (`claude /login`, `gemini` once interactively, `codex login`, `copilot login`) |

`shelp providers` probes every registered provider and prints which are
`ok` vs `unavailable` (and why) on this machine right now.

## Configuration

`~/.config/shelp/config.toml` (or `$SHELP_CONFIG`, or
`$XDG_CONFIG_HOME/shelp/config.toml`). Every key and table is optional;
each provider family decodes only its own table, so unrelated tables are
ignored.

```toml
# Fallback chain shelp tries in order; first success wins. Overridden by
# -p/--provider and $SHELP_PROVIDER. Default: ["shortcuts:apple-pro", "fm"]
providers = ["shortcuts:apple-pro", "openrouter:anthropic/claude-3.5-haiku", "fm"]

# Prompt template: "v1" (default), "v2" (rules), "v3" (few-shot). See
# internal/prompt/templates.go and bench/README.md for how they compare.
template = "v1"

# What to tell the model about your environment. cwd/tools are opt-in --
# they leave the machine once a cloud provider is in the chain. Default:
# ["os", "shell"].
context = ["os", "shell"]

# How long a provider is skipped for after it reports a rate limit / usage
# limit / quota error (see "Rate limits and cooldowns" below). Any
# time.ParseDuration string. Default: "30m".
cooldown = "30m"

# Hosted, key-authenticated OpenAI-compatible endpoints. `preset` fills in
# base_url + api_key_env for a well-known host; set them yourself for
# anything else (e.g. a self-hosted gateway).
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"   # openai | openrouter | groq | gemini | anthropic |
                         # xai | mistral | deepseek | cerebras
models = ["anthropic/claude-3.5-haiku", "meta-llama/llama-3.1-70b-instruct"]

[[openai.endpoint]]
name = "work-llm"
base_url = "https://llm.internal.example.com/v1"
api_key_env = "WORK_LLM_API_KEY"
models = ["gpt-4o-mini"]
temperature = 0.2

# Local OpenAI-compatible servers, in addition to the built-in ones
# (lmstudio :1234, ollama :11434, llamacpp :8080).
[[openai.local]]
name = "fm-serve"
base_url = "http://127.0.0.1:11535/v1"   # `fm serve --port 11535`

[shortcuts]
names = { "apple-pro" = "Ask AI" }   # reuse an already-installed shortcut
install = ["apple-pro", "chatgpt"]   # what `shelp setup shortcuts` creates

# Headless AI CLIs: one optional sub-table per CLI. `models` overrides that
# CLI's built-in variant ids/flag values (a name matching a built-in id
# like "haiku" reuses its flag value; anything else is passed through
# verbatim as both the id and the -m/--model value). `disabled` drops a
# CLI's providers even when it's installed.
[agentcli.claude]
models = ["haiku", "opus"]   # add opus alongside the built-in haiku/sonnet

[agentcli.copilot]
disabled = true   # e.g. to keep it out of the default fallback chain
```

## Benchmark

shelp ships a benchmark harness, `shelp-bench` (see
[bench/README.md](bench/README.md) for the full command reference, case
format, and scoring rules), that runs every case in `bench/cases/*.toml`
against whichever providers and prompt templates you choose, applies
automatic checks (syntax, dangerous commands, GNU-isms on macOS, ...), and
produces a graded markdown report.

```sh
make bench-build
./bin/shelp-bench run --providers available
./bin/shelp-bench report --readme README.md
```

If a provider rate-limits mid-run, the runner trips a circuit breaker for
it: remaining trials for that provider are skipped (not written, so a
later `--resume <dir>` fills them in) instead of hammering it hundreds
more times. `--min-interval <duration>` paces consecutive calls to the
same provider to help stay under a rate limit in the first place. See
[bench/README.md](bench/README.md) for details and the `run.json`/
`results.jsonl` schema.

<!-- bench:start -->
_Results pending._
<!-- bench:end -->

## Rate limits and cooldowns

Any provider can be rate-limited or out of quota, not just missing or
offline -- Apple's Shortcuts "Use Model" backends, OpenAI-compatible
endpoints (HTTP 429), and the agent CLIs (`claude`, `gemini`, `codex`,
`copilot`) all report this distinctly from a plain failure. When shelp
sees one, it remembers: the provider ID goes into a small cooldown store
(`$XDG_CACHE_HOME/shelp/cooldown.json`, `~/.cache/shelp/` by default) for
30 minutes by default (the `cooldown` config key, or the backend's own
`Retry-After` when it gives one), and the fallback chain skips it --
without calling it again -- until the cooldown expires. This matters most
for Apple Shortcuts: **every failed Shortcuts run posts a macOS
notification**, so retrying a rate-limited shortcut on every `shelp`
invocation would spam notifications on top of wasting the call.

```console
$ shelp -v -p shortcuts:apple-pro,fm list 5 recent commits
shelp: shortcuts:apple-pro rate-limited until 14:32 (usage limit); skipping
shelp: fm in 0.7s
git log --oneline -5
```

`shelp providers` shows a cooled-down provider as STATUS `cooldown` with
the reason and expiry time, without probing it. `shelp providers
--reset-cooldowns` clears the whole store. `--force` calls a provider even
while it's cooled down -- including when it's the only ID you named with
`-p`, which is otherwise still skipped by default.

## Privacy

By default shelp tells the model only your OS and shell (`context =
["os", "shell"]`); the `cwd` and `tools` context fields are opt-in
(`--context os,shell,cwd,tools`, or the config's `context` list) because
they leave the machine once a cloud provider is in the chain. Apple's
on-device `fm` never sends anything off your Mac. Any cloud provider —
Shortcuts' Apple Intelligence Pro / ChatGPT, an `[[openai.endpoint]]`, or
one of the agent CLIs (`claude`, `gemini`, `codex`, `copilot`, each
talking to its own cloud account) — receives your full prompt: the task
or command you gave it, plus whatever context fields are enabled.

## Caveats

- Apple's `fm` license forbids "programmatic access... except as expressly
  permitted," and Apple hasn't clarified whether third-party tools like
  shelp fall under that permission. Use at your own risk.
- Each `shortcuts:*` provider needs its `.shortcut` imported by hand: one
  click on "Add Shortcut" per shortcut. `shelp setup shortcuts` generates
  and signs the shortcut and opens it for you, but can't click through
  the import dialog itself — there's no API for that.
- The `claude`/`gemini`/`codex`/`copilot` agent-cli providers spend your
  subscription's usage quota on every call, same as using that CLI
  directly, and are noticeably slower than the other backends: about 2-6s
  per call (they're full coding agents starting up in headless mode, not
  a lightweight API request).

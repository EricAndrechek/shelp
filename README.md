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
$ shelp what's my ip and is it behind a NAT
dig +short myip.opendns.com @resolver1.opendns.com

$ shelp explain 'lsof -nP -iTCP:8080 -sTCP:LISTEN'
$ lsof -nP -iTCP:8080 -sTCP:LISTEN

  Lists the processes listening on TCP port 8080.

  • -n — skip DNS lookups
  • -P — show port numbers, not service names
  • -iTCP:8080 — only TCP sockets on port 8080
  • -sTCP:LISTEN — only sockets in the LISTEN state

  man: lsof
```

(On a terminal, explanations are short Markdown rendered with color and
wrapping; command names link to their man pages. Piped, you get the raw
text.)

Status: early, local-only (no remote for this repo yet).

## Install

```sh
# Homebrew (local tap while the repo has no public remote)
brew tap ericandrechek/shelp ~/Code/public-projects/shelp
brew install ericandrechek/shelp/shelp

# or from a checkout
make install                 # -> ~/.local/bin/shelp
```

Then hook it into your shell and see what works:

```sh
shelp setup shell            # add the right eval/source line to your rc file
# or by hand:
eval "$(shelp init zsh)"     # add to ~/.zshrc
eval "$(shelp init bash)"    # add to ~/.bashrc, or:
shelp init fish | source     # add to ~/.config/fish/config.fish

shelp providers              # which models work on this machine
shelp setup shortcuts        # optional: Apple Intelligence Pro via Shortcuts
sudo fm license              # optional: Apple's on-device model (macOS 27+)
```

`shelp setup shell` (see "Shell setup" below) detects your shell, finds
its rc file, and appends a marked block; it's idempotent and never edits
an rc file that's a symlink or managed by a dotfiles tool like chezmoi (it
prints the line to add by hand instead). The shell integration wraps
`shelp` in a function (zsh/bash/fish, in the style of zoxide/starship) so
a suggested command lands on your prompt line ready to edit or run —
shelp never executes anything itself.

## Usage

```console
$ shelp find all files over 100MB in my home dir       # suggest (default)
$ shelp explain 'tar -xzvf a.tgz'                       # explain (alias: -e)
$ shelp -p fm,shortcuts:apple-pro list 5 recent commits # explicit fallback chain
$ shelp --template v2 -v print yesterday as YYYY-MM-DD  # -v: provider+latency on stderr
$ shelp providers                                       # what works on this machine
$ shelp providers --json                                # same, machine-readable
$ shelp providers --reset-cooldowns                     # clear recorded rate-limit cooldowns
$ shelp setup shortcuts                                 # install the Apple Shortcut this needs
$ shelp help                                            # short overview (also -h, --help)
$ shelp help providers                                  # one command's flags (also: shelp providers -h)
```

`shelp help` lists the commands; `shelp help <command>` (or `shelp <command>
-h`) shows that command's flags. `help` and `version` followed by more words
are a task, so `shelp help me find big files` still works.

**Raw line mode (default on).** Once hooked into your shell, pressing
Enter on a line that starts with `shelp ` (a task, or a `shelp
explain`/`shelp -e` target — not a flag or one of `providers setup init
version help`) sends the REST OF THE LINE to shelp byte-for-byte: no
shell parsing, so an apostrophe (`shelp what's my ip`), a `*.log` glob, a
literal `$HOME`, spaces, quotes, and backslashes all reach the model
exactly as typed — the line is never left dangling on an open quote. The
suggested command replaces the line in place (cursor at the end, not
executed — press Enter again to run it) and the line you actually typed
is added to history; explaining streams the answer below the prompt and
returns you to an empty line. In zsh a spinner (`⠋ shelp: thinking…`)
runs after the line while shelp works, and Ctrl-C cancels the call and
leaves the line you typed in place to edit. Everything else — `shelp providers`, `shelp
-p fm task`, and running `shelp task` from a script or `zsh -c` — behaves
exactly as before.

Turn raw line mode off with `shelp init <shell> --no-line-mode` or config
`[shell] line_mode = false`. With it off (or when it doesn't apply), the
task goes through normal shell parsing: in zsh the integration still runs
`shelp` under `noglob`, so `shelp delete all *.log files` reaches the
model as typed; bash and fish expand globs before any function sees them,
so quote tasks there that contain `*`, `?` or `[`, and in any shell quote
tasks containing an apostrophe (`shelp "what's my ip"`).

**Widget keybinding.** `shelp-widget` turns whatever's currently on your
command line into a suggested command in place — handy when you've
already typed something shelp should rewrite, or when raw line mode is
off. Bind it at install time with `shelp init <shell> --bind KEY` (KEY in
caret notation, e.g. `^G`, or a shell-native escape form like zsh's
`\eg`) or config `[shell] bind = "^G"`; a flag passed to `shelp init`
wins over the config value.

Flags for `suggest`/`explain`: `-p`/`--provider` (or `$SHELP_PROVIDER`, or
config `providers`), `--shell`, `--template`, `--context os,shell,cwd,tools`,
`--json`, `-v`/`--verbose`, `--force` (call a provider even if it's
currently in a rate-limit cooldown). Run `shelp -h` for the full usage.

## Shell setup

```console
$ shelp setup shell                # detect $SHELL, add the block to its rc file
$ shelp setup shell --shell zsh    # be explicit about which shell
$ shelp setup shell --bind '^G'    # also bind shelp-widget to Ctrl-G
$ shelp setup shell --dry-run      # show what would change, write nothing
```

It picks the rc file (`~/.zshrc`, honoring `$ZDOTDIR`; `~/.bashrc`, and on
macOS checks whether `~/.bash_profile` sources it — a login shell
otherwise never reads it — printing a note rather than editing
`~/.bash_profile` itself; `~/.config/fish/config.fish`, honoring
`$XDG_CONFIG_HOME`) and appends a marked block:

```sh
# >>> shelp >>>
eval "$(shelp init zsh)"
# <<< shelp <<<
```

Idempotent: if the rc file already has a `shelp init` line anywhere
(inside or outside the block), it doesn't add a second one; passing
`--bind` when a managed block already exists updates just that block.
It never edits an rc file that's a symlink or that `chezmoi source-path`
recognizes as managed (the chezmoi check is skipped entirely when
chezmoi isn't on PATH) — either way it prints the exact line to add to
your dotfiles yourself and exits cleanly.

## Tab completion

Subcommands, flags, provider IDs (`-p`), template names and the other
flag values complete with Tab in zsh, bash and fish. A task is free text,
so nothing completes after its first word. It is on automatically with the
shell hook (`shelp init`, `shelp setup shell`) and with the Homebrew
formula. Otherwise generate the script yourself:

```console
$ shelp completion zsh > "${fpath[1]}/_shelp"
$ shelp completion bash > ~/.local/share/bash-completion/completions/shelp
$ shelp completion fish > ~/.config/fish/completions/shelp.fish
```

## Providers

| ID pattern | kind | needs | setup |
|---|---|---|---|
| `fm` | on-device | macOS 27+, Apple Intelligence enabled | `sudo fm license` (once, machine-wide) |
| `shortcuts:apple`, `shortcuts:apple-web`, `shortcuts:apple-pro`, `shortcuts:apple-pro-web`, `shortcuts:chatgpt`, `shortcuts:chatgpt-web` | apple-cloud | macOS, the Shortcuts app + `shortcuts` CLI; for the chatgpt ones, Apple's ChatGPT extension (System Settings › Siri) | `shelp setup shortcuts` generates and opens one `.shortcut`, "shelp", that serves all six; click "Add Shortcut" once (no import API exists) |
| `<endpoint-name>:<model>`, e.g. `openai:gpt-4o-mini`, `openrouter:anthropic/claude-3.5-haiku` | api | an `[[openai.endpoint]]` config entry and its API key env var | add the config entry below, `export <KEY>_API_KEY=...` |
| `lmstudio:<model>`, `ollama:<model>`, `llamacpp:<model>`, or a name from `[[openai.local]]` | local-server | a local OpenAI-compatible server running on the expected port | start the server (LM Studio, `ollama serve`, `llama-server`, `fm serve`, ...) |
| `claude:haiku`, `claude:sonnet` | agent-cli | `claude` installed and logged in | `claude /login` |

`shelp providers` probes every registered provider and prints which are
`ok` vs `unavailable` (and why) on this machine right now.

Only `claude` is shipped among the AI-agent-CLI backends; gemini, codex,
Antigravity, and Copilot CLI were all evaluated and dropped -- see
"Dropped backends" below.

## Configuration

`~/.config/shelp/config.toml` (or `$SHELP_CONFIG`, or
`$XDG_CONFIG_HOME/shelp/config.toml`). A missing file at the default
location just means the defaults; a `$SHELP_CONFIG` that names a missing
file is an error. Every key and table is optional; each provider family
decodes only its own table, so unrelated tables are ignored.

You don't have to edit it by hand: `shelp config` shows the file and your
effective settings, `shelp config chain` picks the provider chain from a
numbered list (or `shelp config chain fm,claude:haiku` sets it directly),
and `shelp config edit` opens the file. Saving only touches the `providers`
line; if the file is managed by chezmoi or is a symlink, shelp prints what
to change instead of writing it. On a terminal, plain `shelp setup` walks
through the shell hook, the Apple Shortcut and the chain.

```toml
# Fallback chain shelp tries in order; first success wins. Overridden by
# -p/--provider and $SHELP_PROVIDER. Default: ["shortcuts:apple-pro", "fm"]
# (free backends only). Adding claude:sonnet gives the best accuracy after
# Apple's cloud model, at the cost of your Claude plan's usage.
providers = ["shortcuts:apple-pro", "claude:sonnet", "fm"]

# Prompt template: "v4" (default: v2, with Markdown explanations), "v2"
# (rules), "v1" (minimal), "v3" (v2 + few-shot). See internal/prompt/templates.go; the Benchmark section below
# has the comparison.
template = "v4"

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

# The one Apple Shortcut every shortcuts:* provider runs (the request
# names the model), and that `shelp setup shortcuts` installs.
[shortcuts]
name = "shelp"   # the default

# Headless AI CLIs: one optional sub-table per CLI (today, just claude --
# see "Dropped backends" for why gemini/codex/Antigravity/Copilot CLI
# aren't shipped). `models` overrides that CLI's built-in variant
# ids/flag values (a name matching a built-in id like "haiku" reuses its
# flag value; anything else is passed through verbatim as both the id and
# the -m/--model value). `disabled` drops this CLI's providers even when
# it's installed.
[agentcli.claude]
models = ["haiku", "opus"]   # add opus alongside the built-in haiku/sonnet

# Shell integration `shelp init` renders. Read at every shell start, so
# keep it fast -- it's just a config read.
[shell]
bind = "^G"           # keybinding for shelp-widget; --bind on `shelp init` wins
line_mode = true      # raw-line Enter interception; default true
```

### Colors

shelp colors its human-facing output only on a terminal: the providers
status, setup progress, explain highlighting, the logo on `shelp version`
and `--help`, and the shell integration's "thinking" status.
`NO_COLOR=1` turns it off; `CLICOLOR_FORCE=1` forces it on. The suggested
command, `--json`, and `shelp init` output are never colored.

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
| Provider | Template | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% | Unavailable% | Suggest p50/p90 | Explain first-chunk p50 | Explain total p50 | Cold p50 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| claude:haiku | v2 | 112 | 100% | 84% | 77% | 95% | 0% | 1976ms/2353ms | 2520ms | 2971ms | 1857ms |
| claude:sonnet | v2 | 112 | 100% | 97% | 96% | 100% | 0% | 2345ms/2983ms | 3260ms | 3729ms | 2260ms |
| fm | v2 | 112 | 100% | 36% | 29% | 100% | 0% | 586ms/1002ms | 385ms | 2392ms | - |
| shortcuts:apple-pro | v2 | 112 | 28% | 98% | 97% | 100% | 72% | 1295ms/1508ms | 1717ms | 1717ms | - |

_2026-09-30 · 81 cases · up to 1 sample(s) per trial_
<!-- bench:end -->

**How it's measured.** 81 cases (`bench/cases/*.toml`): 67 "suggest" tasks
and 14 "explain" commands across files, text processing, processes and
networking, git, OS-specific system tasks, archives/media, and shell syntax,
with many targeting macOS/BSD vs Linux/GNU traps (`sed -i ''`, `date -v` vs
`-d`, `ps --sort`, `du -d`) and zsh/bash/fish differences. Each case runs in
every shell/OS combination it lists (112 trials per provider and template).
Every distinct answer was graded 0/1/2 against the case's rubric by Claude
(Opus 5.5), with suspicious claims checked by actually running them on this
Mac (e.g. `sips -s format jpg` fails, `mv File file` works on APFS);
identical answers share one grade, and answers matching a case's reference
list are graded 2 automatically. "Mean score" is the average grade as a
percentage of 2. Grades live in `bench/grades.jsonl`; `bench/RESULTS.md` is
the full report across every run, including dropped backends.

**Findings (2026-09-30, M4 Pro, macOS 27.0):**

- **Apple Intelligence Pro via Shortcuts is the best fit when available:**
  98% at ~1.3s, pure completion, free. Its catch is a usage limit: ~100
  calls in quick succession, then "You have reached the usage limit for this
  model" for more than 1.5 hours (the reset window is unpublished). Only 31
  of its 112 trials ran before the limit, so treat its score as indicative.
  shelp's cooldown then skips it instead of retrying.
- **Claude Sonnet (`claude:sonnet`) is the best fallback:** 97-98% at
  ~2.1-2.3s end to end, on your Claude plan's usage.
- **Claude Haiku is not worth it here.** With Claude Code's default hidden
  thinking it scored 89% but took ~6.6s; with thinking off it's ~1.8s but
  drops to 77% as plain text (and occasionally wrapped commands in prose) or
  84% with schema-enforced JSON output, which is what shelp now uses.
- **Apple's on-device model (`fm`) is fast and private but weak:** 31-36%,
  ~0.35s warm. It invents flags and tools (`battctl`, `jpegmini`, `ip` on
  macOS) and falls for most BSD/GNU traps. Keep it as the offline fallback.
- **Prompt template v2 is the default** because it matched or beat v1
  everywhere: Apple Intelligence Pro 94% -> 98%, Claude Sonnet's
  format-clean rate 91% -> 100%, fm 31% -> 36%. v3 (v2 plus worked
  examples) added nothing over v2 for any model, and small models copy the
  examples' shape. The default template is now v4, which is v2 for suggest
  and only changes explain's format (short Markdown that shelp renders on a
  terminal), so these suggest numbers still apply.
- **Telling the model which tools are installed (`--context ...,tools`)
  made fm worse** (36% -> 32%): it reached for the listed tools where they
  didn't fit. It stays opt-in.
- Dropped backends, for reference (see "Dropped backends"): Codex CLI
  scored ~95% and Copilot CLI ~94% at 4-5s before being dropped for harness
  control reasons, not quality. Codex exhausted a free ChatGPT plan's
  monthly allowance after 255 calls.

End-to-end latency (process start to printed command, `shelp-bench
latency`, warm p50 of 5): fm 0.35s (1.25s cold), claude:haiku 1.8s,
claude:sonnet 2.1s. Apple Intelligence Pro measured ~1.3s per call in the
benchmark run (it was rate-limited when the latency pass ran).

Caveats: one sample per trial; the grader is itself a Claude model, so a
bias toward Claude-style answers can't be ruled out, although every
rubric names concrete pass/fail criteria and every disputed call was
tested; the Linux cases are graded on the command's correctness for the
stated OS, not executed on Linux.

## Offline

If the machine has no network route, shelp skips network providers
(Shortcuts, hosted APIs, `claude`) without calling them and goes straight to
local ones (`fm`, LM Studio, Ollama...); keep one in your chain. `--force`
does not override this. A connected-but-no-internet network is not detected
and falls back to normal provider timeouts.

## Rate limits and cooldowns

Any provider can be rate-limited or out of quota, not just missing or
offline -- Apple's Shortcuts "Use Model" backends, OpenAI-compatible
endpoints (HTTP 429), and the agent-CLI backend (`claude`) all report
this distinctly from a plain failure. When shelp
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
the agent-CLI backend (`claude`, talking to its own cloud account) —
receives your full prompt: the task or command you gave it,
plus whatever context fields are enabled.

## Provider purity

Every provider shelp ships must behave as a single plain chat completion:
shelp's system prompt plus one user message in, one text answer out. No
tools, no file/project access, no memory or instructions loaded from the
user's own config, no multi-turn agent loop, and no hidden "thinking"
unless the user opts in (nothing does, today). `fm`, the Shortcuts
backends, and every OpenAI-compatible endpoint are pure completions by
construction — there's no agent harness in the way. The one agent-CLI
backend shelp ships (`claude`) is a full coding agent by default, and
stays in the fallback chain only because there's a verified, flag-only,
*structural* way to strip every optional capability back down to that
same one-completion shape — "structural" meaning shelp can prove no tool
call is possible at all (`--tools ""`), not merely deny specific tools by
name (see "Dropped backends" for a backend that only offered the latter,
and was dropped for it):

| provider | tools possible? | hidden input tokens (measured) | thinking/reasoning | turns |
|---|---|---|---|---|
| `fm`, `shortcuts:*`, `openai:*`/local servers | no (no agent harness at all) | none | none | 1 |
| `claude:*` | no (`--tools ""`, `--strict-mcp-config`) | ~392-463 input tokens (haiku/sonnet, `--setting-sources ""`; no CLAUDE.md/memory) | none (`MAX_THINKING_TOKENS=0`; haiku otherwise burns ~318 output tokens/3.3s on hidden thinking for a trivial prompt) | 1 (`--output-format json`: `num_turns: 1`, `permission_denials: []`) |

Measured live 2026-09-30 (`claude -p --model haiku/sonnet --output-format
json` with `MAX_THINKING_TOKENS=0` and shelp's other claude flags):
haiku — 15 output tokens, 0 thinking tokens, 392 input tokens, 520ms API
time, `num_turns: 1`; sonnet — 13 output tokens, 0 thinking tokens, 463
input tokens, 1038ms API time, `num_turns: 1` (sonnet doesn't think by
default regardless, so the env var is just harmless insurance there).

## Dropped backends

Four agent CLIs were evaluated and deliberately left unwired, rather than
hardened, because none of them can be reduced to a single plain completion
through documented flags:

- **Copilot CLI** (`copilot`) — no structural way to disable its built-in
  tools was found: `copilot help permissions` documents a bare
  `--available-tools` (no value) as disabling every tool, but this was
  verified live to be FALSE — the model still ran its built-in `bash` tool
  and executed a real shell command. Only a *permission-layer*
  `--deny-tool shell` stopped it (a denylist, not a structural absence of
  tools) — best-effort, not a guarantee: a future release could add a
  built-in capability not covered by `--available-tools`/
  `--disable-builtin-mcps`/`--deny-tool`, and shelp would have no way to
  know. Any backend whose harness can run tools shelp can't structurally
  control is dropped, full stop — this is that rule's first casualty.
- **Codex CLI** (`codex`) — its own agent system prompt is baked in and
  can't be replaced (unlike claude's `--system-prompt`), and even in the
  most restrictive sandbox it can still run read-only shell commands.
  Reconsider if a future release adds a genuine plain-completion mode.
- **Antigravity CLI** (`agy`) — an agent by design: it tries to run tools
  even in `--mode plan` for anything except a trivial prompt (the
  "auto-denied" no-output failure mode this repo used to work around), and
  it stores every conversation under `~/.gemini/antigravity-cli/` on disk
  regardless of what flags are passed.
- **Gemini CLI** (`gemini`) — deprecated by Google in favor of Antigravity;
  not worth hardening a tool its own vendor is retiring.

## Caveats

- Apple's `fm` license forbids "programmatic access... except as expressly
  permitted," and Apple hasn't clarified whether third-party tools like
  shelp fall under that permission. Use at your own risk.
- The `shortcuts:*` providers all run one Apple Shortcut, "shelp", which
  you import by hand: one click on "Add Shortcut". `shelp setup
  shortcuts` opens it for you, but can't click through the import dialog
  itself — there's no API for that. Shortcuts only imports signed
  shortcut files, and signing is a round trip to Apple's server that now
  and then stalls, so shelp ships its shortcut already signed (an
  Apple-issued certificate with nothing personal in it); it signs one at
  setup only if that copy can't be used, retrying stalled attempts. Which model
  answers (Apple Intelligence, Apple Intelligence Pro, ChatGPT, each with
  or without web knowledge) is up to the provider you pick; the
  `shortcuts:chatgpt*` ones also need Apple's ChatGPT extension.
- **Updating the shortcut**: when shelp changes the shortcut it generates
  (a new `ShortcutFormatVersion`), the installed one doesn't update
  itself. shelp notices on its own — `shelp providers` shows the
  `shortcuts:*` providers as unavailable with `outdated shortcut (format
  vN, current vM)`, and a normal `shelp` call prints a one-line stderr
  hint the first time it runs after a new shelp version — so re-run
  `shelp setup shortcuts`. Shortcuts can't replace a shortcut in place
  (importing over a name keeps both, whichever button you click), so
  setup asks you to **delete the old "shelp" first** (right-click ›
  Delete in the Shortcuts app), waits until it's gone, then opens the new
  one for a plain "Add Shortcut". (After a `brew upgrade shelp`, this is
  the same command to reach for — see the Formula's caveats.)
- **Shortcuts from shelp 0.2.6 and earlier** were one per model, named
  `shelp · <Model>` / `shelp · <Model> + Web`. They're no longer used:
  `shelp setup shortcuts` and `shelp providers` list any you still have
  so you can delete them in the Shortcuts app. shelp never runs them —
  the ones from shelp 0.2.0-0.2.2 fail every run ("Please choose a value
  for each parameter in this action.") with a notification.
- The `claude` agent-cli provider spends your account's usage quota on
  every call, same as using the CLI directly (a paid subscription), and is
  noticeably slower than the other backends: about 1-2s per call even
  hardened down to a plain completion (it's a full coding agent starting
  up in headless mode, not a lightweight API request).

## Development

```sh
make build          # bin/shelp
make test           # Go tests
make test-shell     # pty tests of the shell integrations (zsh, bash >= 4,
                    # fish: native if installed, else in Docker; uses uv/pyte)
make ci             # exactly what CI runs: vet, build, shell suite, go test -race
make sign-shortcut  # re-sign the embedded Apple Shortcut (after changing it)
```

`.github/workflows/ci.yml` only installs those prerequisites on macOS and
Linux and runs `make ci`, so a green local `make ci` is a green CI run.

## License

MIT, see [LICENSE](LICENSE).

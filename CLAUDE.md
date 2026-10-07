# shelp

Turn a task into a shell command (`shelp find files over 100MB`), or explain
one (`shelp explain 'lsof -nP -iTCP:8080'`), using whatever model the user
has: Apple's on-device model (`fm`), Apple's cloud models via Shortcuts,
OpenAI-compatible endpoints (hosted with a key, or local: LM Studio, Ollama,
llama-server), or an installed AI CLI (claude -- gemini, codex,
Antigravity, and Copilot CLI were all evaluated and dropped, see "Dropped
backends" in README.md and the agentcli PURITY note below).
Successor to the `ghcs`/`ghce` shell functions in Eric's dotfiles.

Local-only repo for now (no remote). Module `github.com/ericandrechek/shelp`.

## Layout

- `cmd/shelp/` — the CLI. Subcommands: suggest (default), explain, providers,
  setup, init, version. Suggest prints ONLY the command on stdout.
- `internal/provider/` — `Provider` interface, family registry, fallback
  `Chain`, timing `Call` (`provider.go`), plus the `RunCmd` exec helper
  (`execx.go`). One file per family: `fm.go` (Apple on-device), `openai.go`
  (OpenAI-compatible hosted endpoints + local servers, +`openai_test.go`),
  `shortcuts.go` (the `shortcuts:*` providers, +`shortcuts_test.go`),
  `shortcutgen.go` (builds/signs the "Use Model" `.shortcut` plist,
  +`shortcutgen_test.go`, `testdata/shortcuts/reference.json`), and
  `agentcli.go` (the `claude` headless-CLI provider, +`agentcli_test.go`,
  `testdata/agentcli/claude` — a fake CLI script driven entirely by
  `SHELP_FAKE_*` env vars, see its doc comment; gemini, codex, antigravity
  (`agy`), and copilot were all removed from agentcli.go, see PURITY
  below).
- `internal/prompt/` — prompt `Template`s (named, versioned; `Default`) and
  environment `Context` detection (os, shell, opt-in cwd/tools).
- `internal/clean/` — strips fences/backticks/prompt signs from replies.
- `internal/shellinit/` — `shelp init zsh|bash|fish` (raw line mode,
  `--bind`; embedded `scripts/shelp.<shell>.tmpl` templates rendered via
  `Render`/`Options`, see `shellinit.go`, `bind.go`'s `ConvertBind`,
  `config.go`'s `[shell]` table) and `shelp setup shell`'s rc-file
  installer (`setup.go`: idempotent marked block, chezmoi/symlink
  detection, the macOS `~/.bash_profile` note).
- `internal/config/` — `~/.config/shelp/config.toml`; each family decodes its
  own `[table]` via `cfg.Section(name, &v)`.
- `internal/cooldown/` — the rate-limit cooldown store
  (`$XDG_CACHE_HOME/shelp/cooldown.json`) `provider.Chain` and `shelp
  providers` read/write; see "Rate limits and cooldowns" below.
- `internal/bench/` — benchmark harness core: `case.go` (`Case`,
  `LoadCases`), `select.go` (`--cases`/`--templates` selection), `context.go`
  (synthetic os/shell `Context`, never the runner's real host),
  `runner.go`/`trial.go` (the provider x template x case x sample grid and
  its execution), `checks.go` (automatic per-trial checks), `normalize.go`
  (answer normalization + `grade_key`), `result.go` (`results.jsonl`),
  `grades.go` (`grades.jsonl` store), `pending.go` (ungraded review queue),
  `report.go` (markdown report + README marker update), `latency.go`,
  `providers.go`, `scoring.go`. `testdata/cases/example.toml` is a small
  fixture for this package's own tests/smoke runs, kept out of the real
  case set.
- `cmd/shelp-bench/` — the benchmark CLI. Subcommands: `run`, `pending`,
  `grade`, `report`, `latency`; `shared.go` holds common flag/case-loading
  helpers, `main.go` the dispatcher.
- `bench/cases/*.toml` — the real, tracked benchmark case set (81 cases as
  of 2026-09-30: suggest + explain, see `bench/README.md`); `bench/grades.jsonl`
  — graded answers (tracked); `bench/runs/` — raw runs (gitignored).
  `bench/README.md` — the harness's own docs (commands, schemas, checks).

## Provider contract (read before adding one)

- IDs are lowercase `family[:variant]`: `fm`, `shortcuts:apple-pro`,
  `claude:haiku`, `lmstudio:<model>`, `openai:<endpoint>/<model>`.
- `Family.Providers(cfg)` must be cheap: config + `exec.LookPath` only. No
  processes, no sockets. Probing goes in `Detect`.
- The CLI never calls `Detect`. It calls `Generate` down the chain and falls
  through on error, so `Generate` must fail FAST when the backend is absent,
  returning `provider.Unavailable(id, "...how to fix...")`.
- `Generate` returns raw model text; the CLI cleans it. Never add your own
  instructions: `Request.System`/`Request.Prompt` are final.
- Stream (call `req.OnChunk`) only when it is non-nil.
- Subprocess stdin must be nil/`/dev/null` unless you pipe the prompt in on
  purpose (`fm` reads a piped stdin as the prompt).
- Families register in `init()`: `provider.Register(...)`; optional one-time
  installers: `provider.RegisterSetup(...)`.
- Never hard-fail on name collisions: on Eric's Mac `goose` is the pressly
  DB-migration tool, not Block's agent, and `q` may not be Amazon Q. Verify
  identity (e.g. `--version` output) in `Detect`.

## Family config tables

Top-level config (`providers`, `template`, `context`) is shared; below that
each family decodes its own table via `cfg.Section(name, &v)` (see
`internal/config/config.go`) and every field is optional.

- **fm** takes no `[fm]` table — there is nothing to configure beyond
  whether Apple Intelligence is enabled and licensed.
- **shortcuts**: `[shortcuts]` has `names` (a map from variant key —
  `apple`, `apple-web`, `apple-pro`, `apple-pro-web`, `chatgpt`,
  `chatgpt-web` — to a shortcut name, to reuse an already-installed
  shortcut like "Ask AI" instead of the generated `shelp · <Model>[ +
  Web]` name) and `install` (the variant keys `shelp setup shortcuts`
  creates; default `["apple-pro"]`). `$SHELP_SHORTCUTS` (`all` or a comma
  list of variant keys) overrides `install` at runtime without editing
  config.
- **openai**: `[[openai.endpoint]]` is a repeatable table for hosted,
  key-authenticated endpoints — `name`, `preset` (one of `openai`,
  `openrouter`, `groq`, `gemini`, `anthropic`, `xai`, `mistral`,
  `deepseek`, `cerebras`; fills `base_url`/`api_key_env` when set),
  `base_url`/`api_key_env` (required if no `preset`, or to override one),
  `models` (one provider ID `"<name>:<model>"` per entry), and optional
  `temperature` (default 0). `[[openai.local]]` is a repeatable table for
  local OpenAI-compatible servers — `name` + `base_url` — that adds to or
  overrides the built-in local servers (`lmstudio` :1234, `ollama` :11434,
  `llamacpp` :8080), e.g. macOS 27's `fm serve` on its own port. Local
  model lists come from a probe cached at
  `$XDG_CACHE_HOME/shelp/local-models.json` (`~/.cache/shelp/...` by
  default), refreshed by `Detect`, not read live on every `Providers()`
  call.
- **agentcli**: `[agentcli]` has one sub-table per CLI — today, just
  `[agentcli.claude]` — with `models` (a list overriding that CLI's
  built-in variant ids/flag values; a name matching a built-in id like
  `"haiku"` reuses its flag value, any other string is passed through
  verbatim as both id and `-m`/`--model` value) and `disabled` (drop this
  CLI's providers even when the binary is on PATH). It always runs in a
  fresh empty temp directory (`Generate`'s scratch dir, not the config
  file's directory) so it never picks up this repo's own CLAUDE.md/
  AGENTS.md.

## Backend notes (verified on macOS 27.0, M4 Pro)

### fm (Apple on-device)
- `/usr/bin/fm`, macOS 27+. Exit 69 until `sudo fm license` is accepted
  (machine-wide). `fm available --model system`.
- Use `respond -g --guardrails permissive-content-transformations`; default
  guardrails refused "find all files over 100MB". `--no-stream` unless
  streaming. `--` before the prompt.
- The model (per `modelmanagerd` logs) is `instruct_3b...generic_sparse`,
  most likely AFM 3 Core Advanced (20B sparse, ~3B active). 8K context.
- Only `-m system` exists; `--model pcc` needs Apple's developer PCC entitlement.
- `fm serve` (its OpenAI-compatible local-server mode, reached via the
  `openai` family) measured ~25% slower per call than spawning
  `fm respond` directly: ≈700ms vs ≈560ms p50 for a short prompt. Prefer
  the `fm` family unless the OpenAI wire format is specifically needed.

### Shortcuts "Use Model" (Apple cloud / ChatGPT)
- Action `is.workflow.actions.askllm`. Params: `WFLLMModel` one of
  `"Apple Intelligence"`, `"Apple Intelligence Pro"`, `"ChatGPT"`;
  `WFAllowWebSearch` (UI: "Use Broad World Knowledge"); `FollowUp` false;
  `WFGenerativeResultType` "Text"; `WFLLMPrompt` a WFTextTokenString whose
  attachment is `{"Type":"ExtensionInput"}` (Shortcut Input). Output via
  `is.workflow.actions.output` with `WFOutput` -> ActionOutput "Response".
  Format lifted from WorkflowKit's `Gallery.bundle/.../Haiku.wflow`; enum
  values from the dyld shared cache.
- Build: JSON -> `plutil -convert binary1` -> `shortcuts sign --mode anyone`
  -> `open file.shortcut` -> the user MUST click "Add Shortcut" (no API).
  Detect installation with `shortcuts list`.
- The generated plist has no `WFWorkflowName` field: a shortcut's display
  name comes from the `.shortcut` file's basename at import time, so the
  generator writes `"<name>.shortcut"` verbatim rather than embedding a
  name anywhere in the plist.
- Run: `printf '%s' "$prompt" | shortcuts run NAME -o - --output-type public.plain-text`
  (default output is RTF; `public.text` crashes). ~1.2-1.7s. Missing
  shortcut: exit 1, "Couldn't find shortcut". The prompt has no separate
  system field: send `system + "\n\n" + prompt`.
- "Apple Intelligence Pro" verified in logs as
  `instruct_server_v2.shortcuts_use_model_pro` via `privatecloudcomputed`.
  Verify others with `/usr/bin/log show` (never bare `log` in zsh: builtin).
- PCC has an unpublished daily cap (higher with iCloud+).

#### Shortcut format versions, update detection, and the update flow

- `ShortcutFormatVersion` (Go const in `shortcutgen.go`) is the current
  generated shortcut's plist format: **1** (shelp 0.1.x) was the
  original, marker-less, 2-action (Use Model + Stop and Output) shape
  above. **2** (0.2.0-0.2.1) added a leading version-marker `If` block
  and **3** (0.2.2) fixed its `WFInput` wrapper, but both fail EVERY run,
  real prompts included, at the If with "Please choose a value for each
  parameter in this action." (+ a notification). **4** (0.2.3, current)
  is the first one verified live (2026-10-07: marker -> `shelp-shortcut
  4`, a real prompt -> a command): "Text: Shortcut Input. If Text is
  `__shelp_shortcut_version__`, Stop and Output the text `shelp-shortcut
  4` (no model call); Otherwise, `<the v1 pair>`; End If" -- 7 actions
  (`currentShortcutActionCount()`). The reply has no trailing newline, so
  when checking it by hand pipe it through `od -c`: an interactive prompt
  can draw over it and make it look empty.
- The `is.workflow.actions.conditional` action (`If`/`Otherwise`/`End If`)
  repeats one identifier three times, sharing a `GroupingIdentifier` and
  varying `WFControlFlowMode` (`0` If, `1` Otherwise, `2` End If, which
  alone carries a `UUID`). Only the `If` sets `WFCondition` (`4` = "Is"
  on text), `WFConditionalActionString` (the literal), and `WFInput`,
  which is wrapped: `{"Type":"Variable","Variable":{"Value":<attachment>,
  "WFSerializationType":"WFTextTokenAttachment"}}` (v2 put the attachment
  there bare). Two traps, both silent at import and fatal at run time:
  the subject must have a TEXT type for "Is" -- Shortcut Input of a
  shortcut that accepts text or files doesn't (v3), so v4 compares the
  `ActionOutput` (`OutputName` "Text") of an `is.workflow.actions.gettext`
  holding Shortcut Input, the way Apple-built shortcuts compare text; and
  a missing wrapper (v2). No Gallery `.wflow` has a conditional; sources:
  the 20 If actions in Eric's own library, and
  `viticci/shortcuts-playground-plugin`'s CONTROL_FLOW.md (macOS 27, checked
  against Apple-built samples). Newer Shortcuts also has a multi-condition
  `WFConditions` (`WFContentPredicateTableTemplate`) form; the
  single-condition form still works.
- **Debugging a failed run**: `/usr/bin/log show --start "<time>" --style
  compact` filtered to `BackgroundShortcutRunner` lists each action as it
  runs ("Running action <WFTextAction ...>", "finished with error {domain:
  ConditionalAction, code: 1}", `WFActionIndex`), which is how v3's If and
  v4's untouched model branch were confirmed without running anything.
- **Reading what Shortcuts actually stored** (no side effects, the way
  the v2 bug was found): copy `~/Library/Shortcuts/Shortcuts.sqlite*` to
  the scratchpad, then `ZSHORTCUT` (`ZNAME`, `ZACTIONCOUNT`, `ZACTIONS`)
  -> `ZSHORTCUTACTIONS.ZDATA`, a binary plist of the action array
  (`plutil -convert json`). Private schema: for debugging, never for
  shelp itself.
- **Detection** (`installedShortcutVersion`, `shortcuts.go`): Shortcuts
  Events' read-only `action count` (`osascript -e 'tell application
  "Shortcuts Events" to get action count of shortcut "NAME"'`, ~150ms,
  never runs it) is a count Shortcuts caches on save -- a shortcut
  imported from a `.shortcut` file, i.e. every one shelp installs, reads
  **0** ("No actions") with all its actions stored (verified 2026-10-07).
  So: count 2 -> v1 (never sent the marker: its only real action is Use
  Model); count 0 or >= 6 -> run it with the marker as input and parse
  `shelp-shortcut N` (`shortcutMarkerVersionCmd`); anything else -> not a
  shelp format. Any failure (Shortcuts Events down, a v2/v3 that fails
  every run) is "unknown", never outdated. `Detect` reports an outdated
  shortcut as available with a `Reason` (`outdated shortcut (format vN,
  current vM): run \`shelp setup shortcuts\` to update`), which `shelp
  providers` appends to the "ok" row.
- **Update flow** (`shelp setup shortcuts`): each variant in the install
  list -- plus any `shortcutVariants` entry already installed, so an
  upgrade covers everything the user has -- is *missing* (generate, sign,
  open, user clicks "Add Shortcut", poll `shortcuts list`), *outdated*
  (regenerate, sign, open, user clicks **"Replace"**, poll the version
  until it's current: the name doesn't change, so presence can't tell),
  *current/newer* (skip), or *unknown version* (left alone, with a note
  to delete a broken one by hand and rerun -- replacing it would mean
  polling a shortcut that fails every run, one notification per poll).
  **The "Replace" dialog is unverified with a human.**
- **Runtime hint**: a normal `shelp suggest`/`explain` call whose resolved
  provider chain names a `shortcuts:*` provider runs a best-effort,
  once-per-shelp-binary-version check (`provider.CheckShortcutsFreshness`,
  wired into `cmd/shelp/main.go` via `defer` right after the real
  `provider.Chain` call succeeds, so it only ever runs after the answer
  is already printed and never delays or fails the real command) and
  prints one line to stderr if any installed shelp shortcut turns out to
  be outdated:
  ```
  shelp: your Apple Shortcuts are from an older shelp; run `shelp setup shortcuts` to update them
  ```
  Its version check is a marker run (see Detection), so on a v2 it fails
  once per binary version, posting one notification.
  Recorded in `$XDG_CACHE_HOME/shelp/shortcut-check.json`
  (`~/.cache/shelp/...` by default, alongside `cooldown.json`) so it only
  ever runs once per binary version. This is what a `brew upgrade shelp`
  caveat points at (see `Formula/shelp.rb`) -- it's the mechanism that
  actually tells a user their Shortcuts wrapper needs updating, since
  Homebrew itself has no way to.

### agentcli (claude headless CLI)

**PURITY.** Every provider shelp ships must behave as a single plain chat
completion: shelp's system prompt + one user message in, one text answer
out — no tools, no file/project access, no memory or config loaded from
the user's own setup, no multi-turn agent loop, no hidden "thinking"
unless the user opts in. `fm`, `shortcuts:*`, and every OpenAI-compatible
endpoint meet this by construction (no agent harness exists to strip
down). The agent CLIs are full coding agents by default, so each one is
wired up here only once there's a documented, verified, flag-only,
*structural* way back down to a plain completion — "structural" meaning
shelp can prove no tool call is possible at all, not merely deny specific
tools by name (see the copilot note below for a CLI that only offered the
latter, and was dropped for it). See the per-CLI notes below and the
"Dropped backends" section of README.md for the four that were evaluated
and rejected:
- **gemini**, **codex**, **antigravity**/`agy`: each is structurally an
  agent harness — a baked-in system prompt, always-available shell
  access, or both — with no plain-completion mode to switch into.
- **copilot**: had a documented-sounding flag (`--available-tools` with
  no value, "disables all other tools") that turned out not to work at
  all, live-verified (the model still ran its built-in `bash` tool); the
  only thing that did stop it, `--deny-tool shell`, is a permission-layer
  denylist, not a structural absence of tools — best-effort, not a
  guarantee a future release can't reintroduce an uncovered capability.
  See README's "Dropped backends" entry, and `git log --all
  --oneline -- internal/provider/agentcli.go` for the commit that wired
  copilot up (with the full flag-by-flag investigation in its message)
  before it was removed here.

- **claude** authenticates via claude.ai (a subscription, not an API key).
  `--bare` cannot be used with subscription auth: it forces
  `ANTHROPIC_API_KEY`/`apiKeyHelper` only ("OAuth and keychain are never
  read"), and a subscription-only account then reports "Not logged in".
  Use `--setting-sources ""` (skip user/project/local settings) +
  `--strict-mcp-config` (no MCP servers) + `--tools ""` (no tools at all) +
  `--permission-prompts none` + `--disable-slash-commands` +
  `--no-session-persistence` instead — auth-neutral, and the closest thing
  claude has to "just answer in text". `--model` takes the `haiku`/`sonnet`
  aliases directly; `--system-prompt` is a separate flag (no manual
  system+prompt concatenation needed). `MAX_THINKING_TOKENS=0`
  is additionally set in the subprocess environment (`claudeSpec.extraEnv`,
  `agentcliProvider.Generate` appends it to `os.Environ()`): haiku
  otherwise spends real output tokens on hidden "thinking" for even a
  trivial prompt. Measured live 2026-09-30 (`claude -p --model <m>
  --output-format json`, same flags as `claudeArgs` plus this env var,
  prompt "list 5 recent git commits"): haiku — 15 output tokens, 0
  thinking tokens, 392 input tokens, 520ms API time, `num_turns: 1`,
  `permission_denials: []` (without the env var: 318 output tokens spent
  on hidden thinking, 3.3s API time, same prompt); sonnet — 13 output
  tokens, 0 thinking tokens, 463 input tokens, 1038ms API time,
  `num_turns: 1` (sonnet doesn't think by default regardless, confirming
  the env var is harmless there, so it's set unconditionally for the
  whole spec rather than gated per-model). The small, stable input-token
  counts (392/463, not thousands) and `num_turns: 1` on both are also the
  evidence that `--setting-sources ""`/`--tools ""` really do keep out
  CLAUDE.md/project memory and any tool round-trip.

## Rate limits and cooldowns

A provider can be rate-limited or out of quota, not just missing/offline.
Real incident (2026-09-30): Apple Intelligence Pro (`shortcuts:apple-pro`)
hit "Error: You have reached the usage limit for this model. Please try
again later." after ~93 calls in ~2.5 minutes of shelp-bench benchmarking
(reset window unknown), and the benchmark kept dispatching ~240 more
calls -- each one a Shortcuts run that failed the same way. **Every
failed Shortcuts run posts a macOS notification**, so the user was
flooded. `shelp`'s own CLI has the identical exposure: each call tries
the rate-limited shortcut first (another notification) before falling
through to the next provider in the chain.

Classification per family: shortcuts.go treats stdout/stderr as a rate
limit in two shapes only, not a blanket substring match regardless of
exit code -- that would misfire on a successful answer that happens to
contain "try again later" (e.g. explaining a retry loop). (a) a non-zero
exit whose stdout/stderr contains "usage limit" or "try again later"
(case-insensitive); or (b) an exit-0 run whose trimmed stdout starts with
"Error:" and contains "usage limit" -- the one real observed exit-0 shape
("Error: You have reached the usage limit for this model. Please try
again later.": a rate-limited "Use Model" run typically still exits 0,
its output IS the error text). openai.go treats an HTTP 429 as one,
parsing `Retry-After` (seconds or an HTTP-date) when present; agentcli.go's
parse func (claude's, via `agentRateLimitReason`, shared with any future
agentcli spec) treats "rate limit", "quota", "usage limit", "429", or
"resource exhausted" in a failed call's stderr/stdout (case-insensitive,
non-zero exit) as one.
All three families construct
`provider.RateLimitedError` via `provider.RateLimited(id, retryAfter,
format, args...)`, which still satisfies `errors.As(err,
&*UnavailableError)` (via `Unwrap`), so every existing "is this provider
unavailable" check keeps working unchanged; `provider.Chain` and the
bench runner additionally recognize it specifically via `errors.As`.

Cooldown: a rate limit is persisted to `internal/cooldown`'s store,
`$XDG_CACHE_HOME/shelp/cooldown.json` (`~/.cache/shelp/` by default):
provider ID -> until (now + the error's `RetryAfter` when it specified
one, else the top-level `cooldown` config key, default 30m) + reason.
Reads/writes are failure-tolerant (a missing/corrupt file just means "no
cooldowns") and writes are atomic (a uniquely-named temp file + rename).
`provider.Chain` skips a cooled-down provider without calling it at all
(reported through the usual `-v` fallback callback, e.g. "shelp:
shortcuts:apple-pro rate-limited until 14:32 (usage limit); skipping"),
unless `--force` is passed -- which applies even when the cooled-down
provider is the only one explicitly named with `-p`. A successful call
never clears a cooldown; cooldowns just expire. `shelp providers` shows a
cooled-down provider as STATUS "cooldown" without probing it (same
reasoning as the CLI itself never calling Detect); `shelp providers
--reset-cooldowns` clears the whole store.

Benchmark circuit breaker: `internal/bench`'s runner trips a per-provider
breaker on that provider's first RateLimitedError and stops dispatching
its remaining trials for the rest of the run -- they are never written to
results.jsonl, so a later `--resume <dir>` fills them in once the
underlying issue has cleared. `run.json`'s `breaker_trips` records which
providers tripped, when, after how many calls, and how many trials were
skipped; each trial's `results.jsonl` line gets `rate_limited: true` when
it hit one (in addition to the existing `unavailable: true`). `shelp-bench
run --min-interval <duration>` paces consecutive calls to the SAME
provider at least that far apart, to help a future run stay under a rate
limit instead of tripping the breaker at all.

## Conventions

- Go 1.27, stdlib first; deps so far: BurntSushi/toml, mvdan.cc/sh/v3
  (`internal/bench/checks.go`'s `commands_exist` check parses the command
  for real instead of a naive regex splitter -- see "Automatic checks" in
  bench/README.md).
- `make vet test build`. Keep `shelp` startup < 10ms: no work in init()
  beyond registration.
- Commits: one change per commit; this repo has no remote.

## Benchmarking etiquette (learned the hard way, 2026-09-30)

- Find a backend's quota before sending it hundreds of calls: a 255-call
  `codex exec` run exhausted a free ChatGPT plan's monthly Codex allowance,
  and ~93 rapid calls tripped Apple Intelligence Pro's usage limit (still
  limited 1.5h+ later). Run a small batch first; the run's rate-limit
  breaker (`internal/bench/runner.go`) stops a provider at its first limit.
- Never call an installed `shortcuts:*` provider in tests or smoke checks:
  every failed Shortcuts run posts a macOS notification. Use `Detect`,
  fakes, or `shortcuts sign` (which imports nothing).
- Never `open` a `.shortcut` file or run `shelp setup shortcuts` from an
  agent: the import dialog needs the user's click.
- A generated shortcut's shape is only proven by running it once
  imported; tests, `plutil`, and `shortcuts sign` all accepted v2, and a
  successful import did too. Have the user run that first check
  themselves.

## Open items (2026-10-07)

- Eric's "shelp · Apple Intelligence Pro" is a live-verified v4; "Ask AI"
  is gone.
- Still unverified with a human: the "Replace" re-import. Testing it
  needs a throwaway build with `ShortcutFormatVersion` bumped past the
  installed one; the replace poll runs the installed shortcut's marker
  every 2s, so only test it over a shortcut whose marker already works
  (Eric's v4 now qualifies).
- Benchmark the other Shortcuts variants (apple, apple-web, apple-pro-web,
  chatgpt, chatgpt-web) once installed and Apple's limit has reset; pace
  them (`--min-interval`) to stay under the limit.
- Codex is quota-locked until 2026-10-30; it's dropped anyway.
- The repo is local-only; the Homebrew tap points at this folder
  (`Formula/shelp.rb` uses a file:// git URL). Publishing means a remote,
  a release tarball URL in the formula, and a real tap repo.

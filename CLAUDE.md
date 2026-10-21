# shelp

Turn a task into a shell command (`shelp find files over 100MB`), or explain
one (`shelp explain 'lsof -nP -iTCP:8080'`), using whatever model the user
has: Apple's on-device model (`fm`), Apple's cloud models via Shortcuts,
OpenAI-compatible endpoints (hosted with a key, or local: LM Studio, Ollama,
llama-server), or an installed AI CLI (claude -- gemini, codex,
Antigravity, and Copilot CLI were all evaluated and dropped, see "Dropped
backends" in README.md and the agentcli PURITY note below).
Successor to the `ghcs`/`ghce` shell functions in the author's dotfiles.

Public repo: `github.com/EricAndrechek/shelp`. Module `github.com/ericandrechek/shelp`.

## Layout

- `cmd/shelp/` — the CLI. Subcommands: suggest (default), explain, providers, history, config,
  setup, init, completion, version; hidden `__complete` and `__kind`. Suggest
  prints ONLY the command on stdout. `kind.go`: `commandFor(args)`, the one
  decision of which argument shapes a command claims (help, version and
  config and history only some: `help me find files`, `version of node`,
  `config of nginx`, `history of git commits` are tasks), the `commands` handler table `run` dispatches through,
  and `__kind`, which prints that decision ("command"/"task") for the
  shell hooks; `kind_test.go` checks run and `__kind` agree.
- `internal/provider/` — `Provider` interface, family registry, fallback
  `Chain`, timing `Call` (`provider.go`), plus the `RunCmd` exec helper
  (`execx.go`). One file per family: `fm.go` (Apple on-device), `openai.go`
  (OpenAI-compatible hosted endpoints + local servers, +`openai_test.go`),
  `shortcuts.go` (the `shortcuts:*` providers, +`shortcuts_test.go`),
  `shortcutgen.go` (builds/signs the "Use Model" `.shortcut` plist,
  +`shortcutgen_test.go`, `testdata/shortcuts/reference.json`),
  `signed.go` (the embedded pre-signed shortcut, `signed/shelp.shortcut`
  + `signed/shelp.json`, written by `make sign-shortcut` =
  `internal/tools/signshortcut`), and
  `agentcli.go` (the `claude` headless-CLI provider, +`agentcli_test.go`,
  `testdata/agentcli/claude` — a fake CLI script driven entirely by
  `SHELP_FAKE_*` env vars, see its doc comment; gemini, codex, antigravity
  (`agy`), and copilot were all removed from agentcli.go, see PURITY
  below).
- `internal/prompt/` — prompt `Template`s (named, versioned; `Default`) and
  environment `Context` detection (os, shell, opt-in cwd/tools).
- `internal/clean/` — strips fences/backticks/prompt signs from replies.
- `internal/style/` — terminal colors for human-facing output only
  (`style.For(w)`: a terminal, no `NO_COLOR`, not `TERM=dumb`; forced by
  `CLICOLOR_FORCE`), brand gradient, 24-bit/256/16 fallback, `PadRight`
  for colored table cells (tabwriter counts escape codes as text). Never
  for suggest's command, `--json`, or `shelp init`. `logo.go`: `Logo(p)`,
  the Braille "shelp" wordmark in the brand gradient, shown by `shelp
  version`/`--help` on a terminal. `spinner.go`: `IsTerminal(w)`,
  `StartSpinner`/`(*Spinner).Stop`, the stderr "thinking" spinner shown
  while a model answers (terminal only). `termsize*.go`: `TermWidth`
  (TIOCGWINSZ) and `Truncate`. Its consumers in `cmd/shelp/`:
  `providers_view.go` (grouped `providers` view with chain line,
  `--all`/`-v`; replaced the old `table.go`), `explain.go` (streaming explain
  highlighter), `banner.go` (version with the logo), `help.go` (the short top-level
  help and one help text per command, `shelp help <cmd>` / `shelp <cmd> -h`;
  flag errors print that command's help on stderr, exit 2).
- `internal/shellinit/` — `shelp init zsh|bash|fish` (raw line mode, `--bind`;
  `scripts/shelp.<shell>.tmpl` rendered by `Render`/`Options`), `shelp setup
  shell`'s rc-file installer (`setup.go`: idempotent marked block,
  chezmoi/symlink detection), and tab completion (`completion.go` spec +
  `completions/<shell>.tmpl`, delimiters `⟦ ⟧`; `shelp completion <shell>`
  prints the same text `shelp init` embeds). Rules below; the trap
  catalogue is `docs/shell-integration.md` (read it before touching a hook,
  the completion or the pty suite):
  - **The hooks keep no list of commands**: the wrapper and raw-line Enter
    ask the binary (`SHELP_PROVIDER=__kind command shelp __kind ARGS`),
    because a shell keeps the hook it loaded until `exec zsh`
    (`TestRender_NoCommandLists`; 2026-10-09 incident). Raw line mode
    decides only `explain`/`-e`/`suggest` (text starting with `-` runs as
    typed, so `shelp explain -p fm ls` gets its flags; otherwise the text
    goes raw), `''` and `-*` itself.
  - **The three `shelp` wrappers call no other hook function** (a shell
    snapshot, Claude Code's, drops `_`-prefixed functions);
    `TestRender_WrapperSelfContained`. `_shelp_is_task` is raw-line-only.
  - stderr beside a good answer is shown in all three shells. In zsh
    **`zle -M` must come AFTER `zle reset-prompt`** or the message is
    wiped (byte-stream checks can't see it: read the pyte screen). Raw-mode
    explain must NOT `zle reset-prompt`; fish needs `_shelp_prompt_room`.
  - shelp must catch SIGINT and EXIT 130, never die of it (fish would
    cancel the raw-line function). bash needs a temporary `trap : INT`.
  - Completion data (`__complete providers|templates|commands|flags|
    subcommands|presets|routes`) comes from the binary at Tab time: config +
    LookPath only, never noisy, one exec per Tab. A command with flags needs
    a `completionFlags` entry, a value-taking flag goes in `valueFlagOrder`,
    subcommands in `completionSubcommands` (two-word keys such as `config
    add` work); `TestCompletionFlagsCoverHelp`/`...CommandsCoverHelp` tie
    them to help.go.
  - The pty suite (`scripts/test-shell-integration.py`, pyte, `make
    test-shell`) needs `Session(..., ctty=True)` for Ctrl-C; sessions get
    `XDG_STATE_HOME`, `XDG_CACHE_HOME`, `SHELP_HISTORY=0`. Fish runs natively
    or in `docker run --rm`; `SHELP_TEST_FISH=docker|native|<path>`.
- `internal/config/` — `~/.config/shelp/config.toml`; each family decodes its
  own `[table]` via `cfg.Section(name, &v)`. `edit.go` (`SetProviders`,
  `SaveProviders`/`WriteAtomic`, `Starter`) and `tables.go`
  (`ArrayTableBlocks`/`RemoveBlock`/`AppendBlock`; `config.Quote`, since Go
  `%q` can emit `\x` escapes TOML forbids).
- `cmd/shelp/config.go` — `shelp config` (show), `config chain [IDS]`,
  `config edit`, `config add endpoint|local`, `config remove NAME`, the
  guided `shelp setup`. `configedit.go` (`configOps`/`saveOps`) is the ONLY
  writer for chain/add/remove/manager. Seams at the top of config.go
  (`stdin`, `isInteractive`, `listProviders`, `probeProviders`, `runEditor`,
  the `*Fn` vars) let tests run without a terminal, backend, rc file or
  Shortcut. Details: `docs/config-tui-history.md`.
- `cmd/shelp/manager*.go`, `interrupt.go`, `internal/term/` — the
  interactive provider manager (`shelp providers -i`, `shelp config chain`
  on a tty); `internal/history/` + `cmd/shelp/history.go` — request
  history. Both in `docs/config-tui-history.md`.
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

## Config editing, the provider manager, history

Full text and every trap: `docs/config-tui-history.md`.

- A chezmoi-managed or symlinked config is never written
  (`shellinit.ManagedElsewhere`): the exact change is printed. On a tty
  with a plain (non-`.tmpl`, non-encrypted, non-`modify_`/`create_`) chezmoi
  source it offers (default No) to edit the source and run `chezmoi apply`.
  Prompts take Ctrl-C and Esc (Esc = no).
- `commandFor`: a word starting with `-` right after `help`, `version`,
  `config` or `history` claims the line (a typo'd flag gets that command's
  flag error, never a model call; `config -i` reached the model before
  v0.3.1); a task is forced with `shelp suggest ...` or `shelp -- ...`.
  `config -i`/`--interactive` = `providers -i`. `config add` claims only bare `add`, `add -h`, `add
  endpoint|local ...`; `config remove` at most one more word; `history` only
  with no args, all-flag args, or `show|clear|path` first (`isHistoryArgs`),
  so `history of git commits` is a task.
- Route labels (`openai_route.go`: `RouteOf`, `CustomRoute`,
  `ConfiguredRoutes`): endpoint `label` as-is for one model, `"<label> ·
  <model>"` for several, `labels = {model = "..."}` wins; `[[openai.local]]
  label`. `ValidateKeyEnv` refuses key-looking values, never echoing them.
- `internal/term` raw mode: syscall ioctls (TIOCGETA/TIOCSETA darwin/BSD,
  TCGETS/TCSETS linux; elsewhere `Supported=false`), VMIN=0/VTIME=1 (100ms)
  so a lone Esc is told from a sequence and a key reader stops without
  stealing the next prompt's input, pure `Decoder`, shift/option-arrows all
  count as Shift. The manager is a pure model (`manager.go`: Update/View
  emitting probe/setup/save effects; `manager_detail.go` the details page,
  `manager_keys.go` the "KEY to ACTION" hints) plus `manager_form.go`,
  `manager_run.go` (restores the terminal on return/panic/SIGTERM/SIGHUP),
  `manager_tty.go` (alt screen; use `\r\n`, output processing is off in
  raw mode).
- Manager traps: raw-mode Ctrl-C is byte 0x03, not SIGINT (return
  `errInterrupted`, exit 130); read the `probeProviders` seam before spawning
  probe goroutines (they outlive the loop; race detector); Detect is
  memoized (shortcuts per process, claude per instance), so a re-check needs
  `provider.ResetDetectCache()` + fresh instances; macOS revokes a pty when
  its session leader exits, so a pty test reading termios afterwards must
  not give the child a controlling tty.
- History: JSONL at `$XDG_STATE_HOME/shelp/history.jsonl` (0600/0700, one
  O_APPEND write, errors swallowed, rewrite past 1.5x `[history]
  max_entries`, default 1000), off via `[history] enabled` or
  `SHELP_HISTORY=0|off|false|no`; cancelled calls not recorded, failures
  recorded with `error`; `key` = first 16 bytes of sha256(mode, template,
  system, prompt) hex, for a future answer cache. **Every test package
  reaching `runGenerate` needs a `TestMain` redirecting `XDG_STATE_HOME`.**
- Usage: providers call `req.ReportUsage(Usage)` (Generate's signature is
  unchanged), `Call` makes `Result.Usage`; openai streams send
  `stream_options.include_usage` (a 400 naming it retries once without,
  remembered per base URL); claude reports in suggest only (not streaming
  explain); fm/shortcuts never. Explain's terminal footer is
  `explainHighlighter.SetFooter` (non-terminal output stays raw);
  `unquoteWord` strips exactly one fully quoted word (explain only); the
  same-as-input hint is stderr-only and skipped with `--json`.
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
- Never hard-fail on name collisions: on the author's Mac `goose` is the pressly
  DB-migration tool, not Block's agent, and `q` may not be Amazon Q. Verify
  identity (e.g. `--version` output) in `Detect`.

## Family config tables

Top-level config (`providers`, `template`, `context`) is shared; below that
each family decodes its own table via `cfg.Section(name, &v)` (see
`internal/config/config.go`) and every field is optional.

- **fm** takes no `[fm]` table — there is nothing to configure beyond
  whether Apple Intelligence is enabled and licensed.
- **shortcuts**: `[shortcuts]` has one key, `name`: the shortcut every
  `shortcuts:*` provider runs and `shelp setup shortcuts` installs
  (default `"shelp"`). The old `names`/`install` keys and
  `$SHELP_SHORTCUTS` are gone (one shortcut per model, formats 1-4) and
  are silently ignored.
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

The deep-dive (format versions v1-v6 and why v2/v3 fail, action shapes and
their sources, the If/Otherwise traps, detection, the update flow,
importing over a name, debugging recipes, the runtime hint) is
`docs/shortcuts.md`; read it before touching `shortcuts.go` or
`shortcutgen.go`. The rules:

- One shortcut (`[shortcuts] name`, default `shelp`) serves all six
  `shortcuts:*` IDs (`apple`, `apple-web`, `apple-pro`, `apple-pro-web`,
  `chatgpt`, `chatgpt-web`); input is JSON `{"model":..,"prompt":..}`, run
  with `shortcuts run NAME -o - --output-type public.plain-text` (never
  `public.text`: crashes). `ShortcutFormatVersion` is 6 (41 actions,
  verified live 2026-10-09). A generated shortcut's shape is only proven by
  running it once imported.
- "Apple Intelligence" and "Pro" both run on Private Cloud Compute
  (verified in `/usr/bin/log show`, never bare `log` in zsh), NOT the
  on-device model `fm` uses. ChatGPT asks once per shortcut for consent.
  PCC has an unpublished daily cap.
- **Whenever the generator changes, run `make sign-shortcut` and commit the
  result** (`TestEmbeddedShortcutCurrent` fails until you do, and within 60
  days of expiry: re-sign before releasing; the 2026-10-09 leaf is valid to
  2027-11-04). `shortcuts sign` is a flaky round trip to Apple (up to 4
  attempts, 10s each).
- Updates are DELETE FIRST: no delete/rename API exists, and importing over
  a name asks Replace/Keep Both without deleting anything.
- Never run `shelp setup shortcuts` or `open` a `.shortcut` from an agent
  (the import dialog needs the user's click), and never run an installed
  `shortcuts:*` provider in tests or smoke checks (every failed run posts a
  macOS notification). A rate-limited run still exits 0, its output being
  `Error: ... usage limit`.
- Detection runs only the marker (`__shelp_shortcut_version__` ->
  `shelp-shortcut N`); any failure is "unknown", never "outdated". `Detect`
  is available only for v5 or newer. A once-per-binary-version freshness
  hint goes to stderr after the answer (`provider.CheckShortcutsFreshness`).
- Setup wording: short plain sentences, a spinner while waiting; `shelp
  providers` folds `-web` twins into one hint line; available rows show a
  dim note from `provider.Noter`.

### agentcli (the `claude` headless CLI)

**PURITY.** Every provider must be a single plain chat completion: shelp's
system prompt + one user message in, one text answer out: no tools, no
file/project access, no memory or user config, no agent loop, no hidden
thinking. An agent CLI is wired up only with a documented, verified,
flag-only, *structural* way down to that (not a permission denylist).
gemini, codex, antigravity/`agy` and copilot were evaluated and dropped
(README "Dropped backends"; `git log --all --oneline --
internal/provider/agentcli.go` has copilot's investigation). claude uses
`--setting-sources ""` + `--strict-mcp-config` + `--tools ""` +
`--permission-prompts none` + `--disable-slash-commands` +
`--no-session-persistence` (NOT `--bare`: it forces API-key auth and a
subscription account reports "Not logged in") and `MAX_THINKING_TOKENS=0`
for every model (haiku otherwise spends ~300 hidden thinking tokens per
call; measured 2026-09-30). The measurements and the evidence that no
CLAUDE.md or tool round-trip leaks in: `docs/agentcli.md`.

## Rate limits and cooldowns

Full text: `docs/rate-limits.md` (classification per family, the cooldown
store, the bench circuit breaker, offline handling).

- Real incident 2026-09-30: ~93 rapid `shortcuts:apple-pro` calls in ~2.5
  minutes hit the usage limit and the benchmark kept going, flooding the
  user with notifications. Providers return `provider.RateLimited(id,
  retryAfter, ...)` (still an `*UnavailableError` via `Unwrap`). Shortcuts
  recognise a non-zero exit with "usage limit"/"try again later" or an
  exit-0 `Error: ... usage limit` output; openai an HTTP 429
  (`Retry-After`); agentcli "rate limit"/"quota"/"usage limit"/"429"/
  "resource exhausted" on a failed call.
- `provider.Chain` persists a cooldown (`internal/cooldown`; `Retry-After`
  or the `cooldown` key, default 30m) and skips that provider until it
  expires, unless `--force`. `shelp providers` shows "cooldown" without
  probing; `--reset-cooldowns` clears it. The bench runner's per-provider
  breaker stops at the first limit (`--resume` fills in later;
  `--min-interval` paces).
- Offline: before the first network provider the chain checks for any route
  (`internal/provider/offline.go`, no packets) and skips network providers
  (`*OfflineSkippedError`); `--force` does not override it.

## Conventions

- Go 1.27, stdlib first; deps so far: BurntSushi/toml, mvdan.cc/sh/v3
  (`internal/bench/checks.go`'s `commands_exist` check parses the command
  for real instead of a naive regex splitter -- see "Automatic checks" in
  bench/README.md).
- `make vet test build`. Keep `shelp` startup < 10ms: no work in init()
  beyond registration.
- `make ci` is exactly what CI runs (vet, `go test -race`, build,
  `test-shell`); `.github/workflows/ci.yml` only installs prerequisites
  (fish, uv) and calls it, plus `make vet test build` on Linux. Tests that
  need a real Mac (`shortcuts sign`, the real `shortcuts` CLI) skip
  themselves when `CI` is set; tests needing a tool (zsh, plutil) skip when
  it is absent. Never put test logic in the YAML.
- **Tests never reach a real backend: `SHELP_TEST_NO_BACKENDS`**
  (`provider.NoBackendsEnv`, `internal/provider/execx.go`). Set, `LookPath`
  and `RunCmd*` refuse `fm`, `shortcuts`, `claude`, `osascript` and `open`
  (an `Unavailable` error / not found, no process started) unless the
  resolved path is inside `os.TempDir()` or a `testdata` directory, so
  fakes (`testdata/agentcli/claude`, temp-dir scripts) still work. Cost: a
  `Getenv` per call, nothing in `init()`. Every test package that can
  reach a provider sets it in `TestMain` (`internal/provider`,
  `internal/bench`, `cmd/shelp`; `cmd/shelp` also defaults
  `SHELP_PROVIDER=nosuch:model`). Why: a test helper cleared
  `SHELP_PROVIDER` and ran the installed Apple Shortcut, a real Apple
  Intelligence Pro request. `internal/provider`'s `TestMain` puts a
  harmless fake `shortcuts` first on PATH so the setup/listing tests still
  run (the real one is invisible there; `cmd/shelp`'s `providers` list has
  no `fm` for the same reason). Tests that need the real CLI
  (`TestGenerateShortcutPlutilRoundTrip`,
  `TestShortcutsProviderGenerateNotInstalled`, `TestLiveShortcut`) call
  `useRealBackends(t)`: they skip unless `SHELP_TEST_REAL_BACKENDS=1` (or
  `SHELP_LIVE_SHORTCUTS=1`), then clear the gate and restore PATH for that
  test only. `/usr/bin/log` and `plutil` are not gated. A new test package
  that reaches providers needs its own `TestMain` line.
- `make ci` on 2026-10-21 (after merging mgr-ux and cmd-shapes): pty
  suite 423/423 (zsh 236, bash 93, fish 94), `go test -race` green.
- Release checklist: `make sign-shortcut` if the generator changed or the
  embedded copy is within ~60 days of expiry; `make ci`; a signed annotated
  tag `vX.Y.Z` (`git tag -s vX.Y.Z`; its message becomes the release
  notes); `git push origin main vX.Y.Z`; the `Release` workflow builds the
  assets and publishes the GitHub release; the tap's (`EricAndrechek/
  homebrew-tap`) `autobump` pins the new version once it verifies the
  tag's signature (the "Homebrew tap" workflow asks it to check right
  away). Then `brew upgrade ericandrechek/tap/shelp` locally. The formula
  lives in the tap, not here.
- Commits: one change per commit.

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

## Open items (2026-10-09)

- Benchmark the other Shortcuts variants (apple, apple-web, apple-pro-web,
  chatgpt, chatgpt-web) -- one installed shortcut serves them all now --
  and grade v4's Markdown explain output; pace them (`--min-interval`) to
  stay under Apple's limit. Plain "Apple Intelligence" (also PCC) earns
  its place only if it's faster or limited separately from Pro.
- The Keep Both branch of the update wait is covered by scripted tests
  only (delete-first makes it rare); Replace was run live with format 4.

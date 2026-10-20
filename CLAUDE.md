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
- `internal/shellinit/` — `shelp init zsh|bash|fish` (raw line mode,
  `--bind`; embedded `scripts/shelp.<shell>.tmpl` templates rendered via
  `Render`/`Options`, see `shellinit.go`, `bind.go`'s `ConvertBind`,
  `config.go`'s `[shell]` table) and `shelp setup shell`'s rc-file
  installer (`setup.go`: idempotent marked block, chezmoi/symlink
  detection, the macOS `~/.bash_profile` note). **The hooks keep no list
  of commands**: the `shelp` wrapper (self-contained, inlined check) and
  raw-line Enter (`_shelp_is_task`, raw-line-only) ask the binary,
  `SHELP_PROVIDER=__kind command shelp __kind ARGS`
  (2026-10-09: a shell still running an older hook sent `shelp config` to
  the model as the task "config" after `brew upgrade`, and the per-shell
  lists had already drifted once). A shell keeps the hook it loaded until
  `exec zsh`, so only the binary can know its commands. The SHELP_PROVIDER
  guard makes a shelp older than `__kind` (which takes `__kind ...` for a
  task) fail at once instead of calling a model; any answer but "task"
  runs the line as typed. Raw line mode still decides `explain`/`-e` (raw
  explain), `''` and `-*` (run as typed: flags need the shell's parsing)
  itself; for anything else `_shelp_line_is_task` passes the words split
  on whitespace only (a quoted word is never a command) and cut at the
  first operator/redirection (`shelp help config | less` asks about `help
  config`). Cost: one exec per `shelp` line, ~6ms (~7-9ms in-shell), two
  for a command typed at the prompt (Enter, then the wrapper); none for
  other lines. `TestRender_NoCommandLists` fails on any command name in
  hook code outside comments and the completion. zsh's wrapper uses
  `print -rz` (plain `-z` turned a `\n` in a suggestion into a newline).
  zsh's suggest status is a spinner: `_shelp_run_animated` runs `command
  shelp` as a background job (`nomonitor`: no `[1] pid`/`done` chatter,
  and the job ignores SIGINT, so a local INT trap kills it INT -> TERM ->
  KILL), redraws POSTDISPLAY every 80ms via `zselect`, and on Ctrl-C
  hands the typed line back. Its pty tests (`scripts/test-shell-integration.py`) run zsh with
  `Session(..., ctty=True)`: without a controlling tty, ^C is a plain
  byte, never SIGINT, so a Ctrl-C test proves nothing. Raw-mode explain
  prints under `zle -I` and must NOT `zle reset-prompt` afterwards: zle
  doesn't know about the output, so it moves up to its old prompt position
  (1 + extra prompt lines) and redraws over the blank line and, under
  p10k's two-line prompt, the explanation's last line; `zle -I` already
  draws a fresh prompt below. fish's `commandline -f repaint` has the same
  blind spot, hence `_shelp_prompt_room`'s `count (fish_prompt) - 1`
  newlines first, after an explanation and after a failed suggestion's
  error alike (fish 3.6 and 4.9). fish's raw-line suggest also defines a
  SIGINT handler (`_shelp_on_sigint`) for the call's duration: without
  one, Ctrl-C makes fish cancel the bind function on the spot, leaving
  the status drawn and the line redrawn at the wrong column; with one it
  carries on (status cleared, line back, shelp's error not shown). bash:
  raw-line Enter runs shelp as a command after readline is done, stderr
  on the terminal, so shelp's own spinner is the status; `shelp-widget`
  runs under `bind -x`, which clears the line (prompt's last line
  included) to column 0 before calling it and redraws it after, so a dim
  `shelp: thinking…` stands there and is erased (`\r\e[K`) before
  returning. Ctrl-C needs a temporary `trap : INT` (the user's put back
  from `$(trap -p INT)`): without it bash abandons the function with the
  status on screen and drops the line; with it the line comes back. The
  suite renders sessions with pyte (40x120 pty; re-runs itself under `uv
  run --with pyte` when missing, byte-stream fallback otherwise) and
  checks every explanation line plus exactly one blank line before the
  next prompt, with one- and two-line prompts. Its stub delegates every
  call but suggest/explain to the real binary with `PATH` emptied and
  `SHELP_PROVIDER=nosuch:model`; `kind_checks` (all three shells, raw line
  and wrapper via `true && shelp ...`) sets `SHELP_STUB_COMMANDS=zzcmd` (a
  command only the binary has, as after an upgrade) and
  `SHELP_STUB_NO_KIND` (a binary from before `__kind`). fish runs natively when a
  `fish` is on PATH (CI), else in `docker run --rm` (fish 3.6, a static
  linux shelp for the daemon's arch); `SHELP_TEST_FISH=docker|native|<path>`
  overrides. The pty answers DA1 and CPR like a terminal: fish 4 waits
  10s for DA1, then warns and degrades. fish 4 pages interactive
  `history` output (pipe it). The stub draws shelp's spinner when its
  stderr is a terminal and answers Ctrl-C like shelp: exit 130, nothing
  printed (`exitCode` in cmd/shelp), re-enabling an inherited ignored
  SIGINT as Go's `signal.NotifyContext` does -- shelp must catch SIGINT
  and EXIT 130, never die of it, or fish would cancel the raw-line
  explain function. Raw-line explain needs no SIGINT handler: shelp runs
  there as a foreground job and takes the SIGINT itself (suggest runs in
  a command substitution, which fish does cancel). Debian/Ubuntu's zsh-syntax-highlighting 0.7.1
  empties `region_highlight` on every redraw, so the plugin run uses
  Homebrew's (0.8) only.
  Tab completion lives here too: `completion.go` (the command/flag spec,
  checked against help.go by a test in `cmd/shelp/completion_test.go`) renders
  `completions/<shell>.tmpl` (delimiters `⟦ ⟧`, since bash uses `<<<`);
  `shelp completion <shell>` prints that text and `Render` embeds the same
  text in `shelp init`, so there is one source. Dynamic values come from the
  hidden `shelp __complete KIND` (config + LookPath, no Detect; kinds
  under "Shell integration updates"). zsh defines `_shelp` and registers with `compdef` at once, or,
  when `compdef` does not exist yet (init ran before compinit), from a
  one-shot precmd hook; it never calls compinit, and as a `#compdef` file
  on fpath (Homebrew) it runs itself. bash's reply drops the part of a
  provider ID before its colon (COMP_WORDBREAKS) and has no `-o default`, so
  a task never completes as a filename. fish is one
  `complete -c shelp -f -a '(__shelp_complete)'` (`complete -e` first, so
  loading twice is harmless). The pty suite's Tab checks use a config with
  deterministic `zzhost:*` IDs; bash needs a third Tab to list.
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

## Config editing, the provider manager, history (tui-config and history branches)

### Config files and writers

- `internal/config/` — `~/.config/shelp/config.toml`; each family decodes its
  own `[table]` via `cfg.Section(name, &v)`.
  `edit.go`: `SetProviders` (comment-preserving replace/insert of the
  top-level `providers` assignment, multi-line arrays included),
  `SaveProviders`/`WriteAtomic` (temp + rename), `Starter`.
  `tables.go`: `ArrayTableBlocks`/`RemoveBlock`/`AppendBlock` for
  `[[openai.endpoint]]`/`[[openai.local]]` blocks. `config.Quote` exists
  because Go `%q` can emit `\x` escapes TOML forbids.
- `cmd/shelp/config.go` — `shelp config` (show: path/status + effective
  settings and their source, including a `history` row), `config chain
  [IDS]` (validate against `provider.All`, warn on unavailable,
  `$SHELP_PROVIDER` override warning; on a terminal it opens the manager
  below instead of the old numbered picker), `config edit`, `config add
  endpoint|local`, `config remove NAME`, and the guided `shelp setup` (no
  target, terminal only: shell hook, Apple Shortcut, chain). The seams at
  the top (`stdin`, `isInteractive`, `listProviders`, `probeProviders`,
  `runEditor`, the guided-setup `*Fn` vars) let tests run everything
  without a terminal, backend, rc file or Shortcut. Prompts (`prompter`,
  one shared `bufio.Reader`) take Ctrl-C and Esc: Esc answers no even when
  the default is yes.
- `cmd/shelp/configedit.go` — `configOps`/`saveOps`: **the ONLY writer for
  chain/add/remove/manager.** It verifies the result decodes to exactly what
  was asked, or writes nothing.
- A config file that is chezmoi-managed or a symlink is never written
  (`shellinit.ManagedElsewhere`): the exact change is printed. On a tty with
  a plain chezmoi source (not `.tmpl`, not encrypted, not `modify_`/
  `create_`), shelp offers (default No) to edit the source and run `chezmoi
  apply`.
- `commandFor` (kind.go): `config add` claims only bare `add`, `add -h`, and
  `add endpoint|local ...`; `config remove` claims at most one more word
  (`config add a cron job` and `config remove old kernels` are tasks).

### Route labels (`openai_route.go`)

- Endpoint `label` is used as-is for a one-model endpoint and
  `"<label> · <model>"` for several; `labels = {model = "..."}` wins.
  `[[openai.local]] label` labels a local server.
- Helpers: `RouteOf`, `CustomRoute`, `ConfiguredRoutes`, `PresetNames`,
  `PresetDefaults`, `IsBuiltinLocal`. `ValidateKeyEnv` refuses a
  key-looking value for `api_key_env` and never echoes it.
- `shelp config add endpoint|local` flags: `--preset --base-url --key-env
  --model --label`.

### `internal/term` and the provider manager

- `internal/term`: raw mode via syscall ioctls (TIOCGETA/TIOCSETA on
  darwin/BSD, TCGETS/TCSETS on linux; elsewhere `Supported=false`).
  VMIN=0/VTIME=1 gives a 100ms read timeout, so a lone Esc is told from a
  sequence and a key reader stops without stealing the next prompt's input.
  The `Decoder` is pure. Shift/option-arrows (`;2`/`;3`/`;4`, `\e\e[A`, rxvt
  `\e[a`) all count as Shift.
- `cmd/shelp/manager.go` is the pure model (`Update`/`View` emitting
  probe/fix/save effects); `manager_form.go` the add form; `manager_run.go`
  the loop (restores the terminal on return, panic, SIGTERM and SIGHUP);
  `manager_tty.go` the alt screen (use `\r\n`: output processing is off in
  raw mode). `interrupt.go`: `exitStatus`/`errInterrupted`.
- Entered by `shelp providers -i|--interactive` and `shelp config chain` on
  a tty.
- Traps:
  - In raw mode Ctrl-C is byte 0x03, not SIGINT: return `errInterrupted` so
    the exit status is 130.
  - Read the `probeProviders` seam BEFORE spawning probe goroutines: they
    outlive the loop (race detector).
  - Detect results are memoized (shortcuts per process, claude per
    instance): a re-check needs `provider.ResetDetectCache()` and fresh
    instances.
  - macOS revokes a pty when its session leader exits, so a pty test that
    reads termios afterwards must not give the child a controlling tty.

### Request history (`internal/history`, `cmd/shelp/history.go`)

- JSONL at `$XDG_STATE_HOME/shelp/history.jsonl` (`~/.local/state/shelp/`),
  files 0600, directory 0700, one O_APPEND write per entry, errors
  swallowed. Retention rewrite once the file passes 1.5x `[history]
  max_entries` (default 1000). `[history] enabled`; `SHELP_HISTORY=0|off|
  false|no` turns it off. Cancelled calls are not recorded; failures are,
  with `error`. `key` = first 16 bytes (hex) of sha256(mode, template,
  system, prompt), stored for a future answer cache.
- `shelp history [show|clear|path]` flags `-n --all -s/--search --provider
  --json`. `recordHistory` and the `historyPath` seam live in
  `cmd/shelp/history.go`. `isHistoryArgs`: `history` is a command only with
  no args, all-flag args, or `show|clear|path` first, so `history of ...`
  stays a task.
- **Every test package that reaches `runGenerate` needs a `TestMain`
  redirecting `XDG_STATE_HOME`** (cmd/shelp has one; see also the
  `SHELP_TEST_NO_BACKENDS` guard in CLAUDE.md).

### Token usage, explain footer, small output rules

- Providers call `req.ReportUsage(Usage)` (Generate's signature is
  unchanged); `provider.Call` turns it into `Result.Usage`. openai streams
  send `stream_options.include_usage`; a 400 naming `stream_options`
  retries once without it, remembered per base URL (`noStreamOptions`).
  claude reports usage in suggest only (the JSON envelope), not in streaming
  explain; `fm` and `shortcuts` never report.
- Explain's terminal footer (provider · time · tokens) is
  `explainHighlighter.SetFooter`, printed after the man line; non-terminal
  output stays raw model text.
- `unquoteWord` (explain only) strips exactly one fully quoted word (raw
  line mode passes `shelp explain '...'` with its quotes).
- Suggest's same-as-input hint goes to stderr only and is skipped with
  `--json`.

## Shell integration updates (polish branch)

- **The three `shelp` wrappers call no other hook function.** A shell
  snapshot (Claude Code's) dropped `_`-prefixed functions, leaving the
  wrapper calling nothing. Each wrapper inlines its `__kind` check;
  `TestRender_WrapperSelfContained` enforces it. `_shelp_is_task` is
  raw-line-only. `TestRender_NoCommandLists` expects two guarded `__kind`
  calls (one in zsh without line mode).
- **stderr next to a good answer is shown.** After a successful raw-line
  suggest/widget, all three shells show the model call's stderr (hints,
  the same-as-input note): zsh via `_shelp_show_err`; bash raw line needs
  nothing (shelp runs as a command after readline is done); the bash
  widget prints before readline's redraw; fish uses `_shelp_prompt_room`.
  **`zle -M` must come AFTER `zle reset-prompt`** or the message is
  wiped; byte-stream checks cannot see this, read the pyte screen. The
  old failure message doubled `shelp: ` and was never visible.
- **Dynamic completion.** `__complete` kinds: `providers`, `templates`,
  `commands`, `flags [CMD [SUB]]`, `subcommands CMD [SUB]` (a two-word key
  such as `config add`), `presets` (`provider.PresetNames()`, for
  `--preset`), `routes` (configured route names, for `config remove`).
  Commands, flags and subcommands come from the binary at Tab time (a shell
  keeps the script it loaded across an upgrade), via
  `completionSubcommands` in `internal/shellinit/completion.go`. An empty
  `commands` answer means an older binary: the script falls back to the
  list rendered into it. There is no static fallback for flags or
  subcommands. A command with flags needs a `completionFlags` entry (a
  missing entry makes `__complete flags X` treat X as a task); value-taking
  flags go in `valueFlagOrder` so the scripts skip their values when
  counting positionals. Each Tab costs one `__complete` exec.
  `TestCompletionFlagsCoverHelp`/`TestCompletionCommandsCoverHelp` tie the
  tables to help.go.
- **Pty suite** (`scripts/test-shell-integration.py`): `SHELP_STUB_STDERR`
  makes the stub print stderr; `SHELP_STUB_COMMANDS` also feeds
  `__complete`. Sessions get `XDG_STATE_HOME`, `XDG_CACHE_HOME`,
  `SHELP_HISTORY=0`, and SIGINT reset to the default in the child (a suite
  run as an `&` background job inherited an ignored SIGINT and bash's
  Ctrl-C checks failed). Importing the script as a module re-execs under
  `uv run` when pyte is missing. Tab checks poll (~5s) because the stub is
  a slow Python script.


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
- **One shortcut, six providers** (format 5): `shelp setup shortcuts`
  installs a single shortcut, `shelp` (`[shortcuts] name`), and all six
  `shortcuts:*` IDs (`apple`, `apple-web`, `apple-pro`, `apple-pro-web`,
  `chatgpt`, `chatgpt-web`) run it; the request says which model. The
  ChatGPT ones also need the ChatGPT extension (System Settings › Siri).
  Formats 1-4 were one shortcut per variant, `shelp · <Model>[ + Web]`;
  those names (and `<name> N` copies) are "legacy": setup, a `shelp
  providers` note, and the runtime hint list them for the user to delete
  (`legacyShortcuts`, names from `shortcuts list` only) and nothing ever
  runs them -- an old one may fail, with a notification, every run.
- Action `is.workflow.actions.askllm`. Params: `WFLLMModel` one of
  `"Apple Intelligence"`, `"Apple Intelligence Pro"`, `"ChatGPT"`;
  `WFAllowWebSearch` (UI: "Use Broad World Knowledge"); `FollowUp` false;
  `WFGenerativeResultType` "Text"; `WFLLMPrompt` a WFTextTokenString with
  one attachment (formats 1-4: `{"Type":"ExtensionInput"}`, Shortcut
  Input; 5: the "prompt" Dictionary Value). Output via
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
- Run: `printf '%s' "$input" | shortcuts run NAME -o - --output-type public.plain-text`
  (default output is RTF; `public.text` crashes). ~1.2-1.7s. Missing
  shortcut: exit 1, "Couldn't find shortcut". Format 5's input is JSON,
  `{"model":"<variant key>","prompt":"<system>\n\n<prompt>"}`
  (`shortcutRequestBody`; Use Model has no separate system field). An
  unknown model answers `shelp-shortcut-error: unknown model <key>` (exit
  0, no model call), which `Generate` turns into an Unavailable.
- "Apple Intelligence Pro" verified in logs as
  `instruct_server_v2.shortcuts_use_model_pro` via `privatecloudcomputed`;
  plain "Apple Intelligence" as `instruct_server_v2.shortcuts_use_model_action`,
  also on Private Cloud Compute (2026-10-09) -- NOT the on-device model `fm`
  runs, so it isn't a duplicate of `fm`. Both also load the on-device
  `instruct_300m.safety` classifier. ChatGPT asks once per shortcut for
  consent to share the input ("Always Allow"). Verify backends with
  `/usr/bin/log show` (never bare `log` in zsh: builtin).
- **Pre-signed shortcut**: each release embeds the current format already
  signed (`internal/provider/signed/`), so setup opens it with no trip to
  Apple; it signs at runtime only when the copy is another format, its
  `WorkflowFingerprint` (the generated workflow with UUIDs normalized)
  differs, or its signature is within 30 days of expiring. **Whenever
  the generator changes, run `make sign-shortcut` and commit the result**
  -- `TestEmbeddedShortcutCurrent` fails until you do, and also when the
  copy is within 60 days of expiry (re-sign before releasing). A
  `--mode anyone` signature is an Apple-issued certificate with no
  personal identity (CN = random hex, O=Apple Inc., chaining to Apple
  Root CA - G3); the leaf is valid ~13 months (2026-10-09's: to
  2027-11-04). The file is an AEA1 archive: "AEA1", a 4-byte profile, a
  little-endian uint32 auth length, then a binary plist auth blob,
  `{SigningCertificateChain: [<DER>...]}` (plutil's JSON output can't
  hold <data>; the tool converts to XML). Signed copies import under
  whatever file name setup gives them, so `[shortcuts] name` still works.
  Verified live 2026-10-09: setup opened the embedded copy with no
  signing, Add, `✓ Installed "shelp" (v6).`
- `shortcuts sign` is a round trip to Apple's server and fails now and
  then (HTTP 500s; once a 20s stall reported as "The file doesn't exist."
  with the input file present, after a ~20s stall -- a good attempt
  takes ~2s); `signShortcut` gives each attempt 10s and makes up to 4,
  and setup prints a line per retry (`signNotice`).
- PCC has an unpublished daily cap (higher with iCloud+).

#### Shortcut format versions, update detection, and the update flow

- `ShortcutFormatVersion` (Go const in `shortcutgen.go`) is the current
  generated shortcut's plist format: **1** (shelp 0.1.x) was the
  original, marker-less, 2-action (Use Model + Stop and Output) shape
  above. **2** (0.2.0-0.2.1) added a leading version-marker `If` block
  and **3** (0.2.2) fixed its `WFInput` wrapper, but both fail EVERY run,
  real prompts included, at the If with "Please choose a value for each
  parameter in this action." (+ a notification). **4** (0.2.3-0.2.6)
  is the first one verified live (2026-10-07: marker -> `shelp-shortcut
  4`, a real prompt -> a command): "Text: Shortcut Input. If Text is
  `__shelp_shortcut_version__`, Stop and Output the text `shelp-shortcut
  4` (no model call); Otherwise, `<the v1 pair>`; End If" -- 7 actions.
  **5** (verified live 2026-10-09: marker, unknown model, one prompt each
  through apple-pro, apple and chatgpt) is one shortcut for every model,
  40 actions (`currentShortcutActionCount()`; `buildWorkflow`'s doc
  comment draws it): v4's marker block unchanged, then in its Otherwise
  Get Dictionary from Input (on the Text), Get Dictionary Value "model"
  and "prompt", Text holding the model value, and six If/Otherwise
  branches nested one inside the next ("If model-Text is `<key>`: Use
  Model (that model, web flag) on the prompt value; Stop and Output its
  Response"), the innermost Otherwise a Stop and Output of
  `shelp-shortcut-error: unknown model <model-Text>`, then seven End Ifs.
  **6** (current; verified live 2026-10-09: install, marker, a ChatGPT
  prompt, and a delete-first update to a throwaway v7) adds one Text action holding the
  "prompt" Dictionary Value and feeds Use Model that Text (41 actions):
  with the bare Dictionary Value, ChatGPT's consent dialog ("Allow
  "shelp" to share 1 dictionary with ChatGPT?") showed the whole JSON
  request, system prompt included. It still does with the Text: the
  dialog shows what the shared value was derived from (the parsed input),
  not what the model gets. An echo test settled that (2026-10-09):
  `{"model":"apple-pro","prompt":"Repeat this message back word for
  word, and nothing else."}` came back as that sentence, no JSON -- every
  branch reads the same Text, so Use Model gets only the prompt.
  The reply has no trailing newline, so when checking it by hand pipe it
  through `od -c`: an interactive prompt can draw over it and make it
  look empty.
- Format 5's action shapes, each copied from a stored one (sources cited
  in `shortcutgen.go`): `is.workflow.actions.detect.dictionary` `{UUID,
  WFInput: <WFTextTokenAttachment of a Text output>}`, output
  "Dictionary" (library: "XML To Dictionary" #8, "Wolfram Alpha 2
  Clipboard" #5); `is.workflow.actions.getvalueforkey` `{UUID,
  WFDictionaryKey, WFInput: <WFTextTokenAttachment of the Dictionary>}`,
  output "Dictionary Value", no `WFGetDictionaryValueType` (Wolfram
  #6-10); a "Dictionary Value" inside a WFTextTokenString (Wolfram #11's
  URL); a mid-string attachment keyed by its U+FFFC's UTF-16 offset
  (Wolfram #3, `{44, 1}`); If nested in Otherwise, inner End If first
  (PDFtoolkit #6-21, Set Alarms #8-15); Use Model's prompt as an
  ActionOutput (viticci EXAMPLES.md "AI Query", VARIABLES.md). macOS
  27's "Otherwise If" (mode 1 + condition fields, per viticci
  CONTROL_FLOW.md) is in no library shortcut, so v5 nests instead.
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
  every run) is "unknown", never outdated. `Detect` reports a provider
  available only when the shortcut is listed and answers v5 or newer
  (an older one would hand the JSON to the model as the prompt);
  outdated (`outdated shortcut (format vN, current vM)`) and unknown are
  unavailable with Fix "run `shelp setup shortcuts`". The version check
  runs once per process for all six providers (`cachedShortcutVersion`).
- **Setup wording**: short plain sentences, no `shortcuts:` prefix and no
  dot trails; waits show a `style.StartSpinner` line (terminal only).
  `shelp providers` folds `-web` twins with the same status into one `add -web
  to let the model look things up online (slower), e.g. <id>` line (`webTwins`;
  the example prefers a twin of a base row in the chain; `--all` shows them).
  Available rows show a dim third column from the optional `provider.Noter`
  (`NoteOf`; where it runs and how it compares); `--json` rows carry it as
  `Note`. Labels stay short ("Claude Haiku"); the headless/no-tools fact lives
  in `shelp help providers`.
- **Update flow** (`shelp setup shortcuts`, `runShortcutsSetup`): the one
  shortcut is *missing* (generate, sign, open, user clicks "Add
  Shortcut", `waitForNewShortcut`), *outdated* (DELETE FIRST: generate
  and sign, then ask the user to delete the old one -- right-click ›
  Delete -- and poll `shortcuts list`, running nothing, until the name
  is gone (`waitForShortcutGone`); only then open the new one, so the
  import is a plain "Add Shortcut" with no conflict, and wait as for a
  missing one), *current/newer* (skip), or *unknown version* (left
  alone, with a note to delete a broken one by hand and rerun --
  replacing it would mean polling a shortcut that fails every run, one
  notification per poll). The wait confirms the marker version and asks
  at most 3 times (`maxMarkerErrors`) before giving up, so a broken new
  format can't post a notification every second. Polls run every 1s.
  Legacy shortcuts and existing `<name> N` copies are pointed out and
  never run.
- **Importing over an existing name** (verified with Eric, macOS 27,
  2026-10-07): the dialog says "Add Shortcut" (not "Replace"); clicking
  it asks "Replace" or "Keep Both", and NEITHER deletes anything. Replace:
  the new shortcut takes the name, the old one is renamed `<name> 1`.
  Keep Both: the new one is `<name> 1`. N can skip numbers (names of
  deleted shortcuts still count). Between "Add Shortcut" and the
  Replace/Keep Both answer, BOTH shortcuts carry the name (`shortcuts
  list` prints it twice) and `shortcuts run <name>` may reach the new one
  -- v4's wait took that for success and missed the leftover copy. There
  is no delete or rename API (Shortcuts Events and `shortcuts` can only
  list/run/view/sign), hence delete-first. `waitForNewShortcut` still
  copes if the name is taken anyway: it counts raw `shortcuts list`
  lines and, while the name appears more than once, runs nothing and
  never finishes; then name answering the new version means Replace (it
  asks the user to delete the new `<name> N` copy and waits), and a copy
  that appeared during the wait answering it means Keep Both (it asks the
  user to delete the old one and rename the copy). It only ever runs the
  marker on the name and on copies that appeared during the wait. A
  shortcut imported from a file gets a cached action count once
  Shortcuts modifies it (the renamed copy's count went 0 -> 7).
- **Runtime hint**: a normal `shelp suggest`/`explain` call whose resolved
  provider chain names a `shortcuts:*` provider runs a best-effort,
  once-per-shelp-binary-version check (`provider.CheckShortcutsFreshness`,
  wired into `cmd/shelp/main.go` via `defer` right after the real
  `provider.Chain` call succeeds, so it only ever runs after the answer
  is already printed and never delays or fails the real command) and
  prints one line to stderr (`shortcutsFreshnessHint`) if the shortcut is
  outdated, or not installed while legacy ones are (the first run after
  upgrading to format 5):
  ```
  shelp: your Apple Shortcut is from an older shelp; run `shelp setup shortcuts` to update it
  shelp: shelp now uses one Apple Shortcut for every shortcuts:* provider; run `shelp setup shortcuts` to install it (and delete the old ones)
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

### Offline

`Kind.NeedsNetwork()` is true for AppleCloud (Shortcuts), API and AgentCLI
(claude); OnDevice (`fm`) and LocalServer are local. Before `provider.Chain`
calls its first network provider it runs `Offline()` once per process
(`internal/provider/offline.go`): a UDP `net.Dial` to documentation
addresses (192.0.2.1, 2001:db8::1) that only consults the routing table --
no packets -- and is offline only if neither IPv4 nor IPv6 has a route.
`offlineCheck` is a package var so tests fake it. Offline, network providers
are skipped without being called (`*OfflineSkippedError`, "shelp:
shortcuts:apple-pro skipped: offline (no network route)"), which also avoids
the Shortcuts failure notification; `--force` does NOT override it (it is for
cooldowns). If nothing local was tried the final error adds "offline, and no
local provider in the chain; add `fm` ...". An API provider failing with a
clear network error (`IsNetworkError`: DNS failure, unreachable, no route,
TLS handshake timeout; not connection refused) marks the rest of the chain
offline; Shortcuts/claude free-text errors are deliberately not classified.
Limit: Wi-Fi with no internet or a captive portal still has a route, so
provider timeouts still apply. `shelp providers` appends "offline" to the
chain line.

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
- `make ci` on 2026-10-20 (after merging tui-config, history, polish): pty
  suite 379/379 (zsh 214, bash 82, fish 83), `go test -race` green.
- Release checklist: `make sign-shortcut` if the generator changed or the
  embedded copy is within ~60 days of expiry; `make ci`; tag; bump
  `Formula/shelp.rb`'s tag; `git -C "$(brew --repository
  ericandrechek/shelp)" pull && brew upgrade ericandrechek/shelp/shelp`.
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

## Open items (2026-10-09)

- Benchmark the other Shortcuts variants (apple, apple-web, apple-pro-web,
  chatgpt, chatgpt-web) -- one installed shortcut serves them all now --
  and grade v4's Markdown explain output; pace them (`--min-interval`) to
  stay under Apple's limit. Plain "Apple Intelligence" (also PCC) earns
  its place only if it's faster or limited separately from Pro.
- The Keep Both branch of the update wait is covered by scripted tests
  only (delete-first makes it rare); Replace was run live with format 4.
- Publishing (on hold, Eric's call): a GitHub remote, release tarballs
  for the formula's url + sha256 (today a file:// git URL to this
  folder), a real tap repo; CI (`.github/workflows/ci.yml`) is ready and
  only runs `make ci`.

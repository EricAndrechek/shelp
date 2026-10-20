# Config editing, the provider manager (TUI), and request history

Moved out of CLAUDE.md (2026-10-20): the old `internal/config/` and
`cmd/shelp/config.go` layout bullets (stale parts replaced) plus the facts from
the `tui-config` and `history` branches. CLAUDE.md keeps the rules.

## Config files and writers

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

## Route labels (`openai_route.go`)

- Endpoint `label` is used as-is for a one-model endpoint and
  `"<label> · <model>"` for several; `labels = {model = "..."}` wins.
  `[[openai.local]] label` labels a local server.
- Helpers: `RouteOf`, `CustomRoute`, `ConfiguredRoutes`, `PresetNames`,
  `PresetDefaults`, `IsBuiltinLocal`. `ValidateKeyEnv` refuses a
  key-looking value for `api_key_env` and never echoes it.
- `shelp config add endpoint|local` flags: `--preset --base-url --key-env
  --model --label`.

## `internal/term` and the provider manager

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

## Request history (`internal/history`, `cmd/shelp/history.go`)

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

## Token usage, explain footer, small output rules

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

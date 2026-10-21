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
  `tables.go`: `ArrayTableBlocks`/`RemoveBlock`/`AppendBlock`/
  `ReplaceBlock` for `[[openai.endpoint]]`/`[[openai.local]]` blocks, and
  `EditTableKeys(block, []KeyEdit)`, which sets or removes keys of the
  block's own table in place (multi-line values, a trailing `# comment`
  and quoted keys handled; sub-tables, comments and other keys untouched;
  a missing key goes after the last one, `name` right after the header).
  `config.Quote` exists because Go `%q` can emit `\x` escapes TOML forbids.
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
  was asked, or writes nothing. `configOps.edits` (`routeEdit`: the name
  the file has + the new route) change a route in place: `routeKeyEdits`
  rewrites only keys whose *effect* differs (a preset's own base URL/key
  variable equals leaving it out; `name` is written whenever the preset
  changes, since an unnamed entry is named after its preset), and `apply`
  checks the edited route reads back `sameRoute` as asked. Order: removes,
  edits, adds. `routeIDChange`/`chainAfterEdit` map provider IDs across an
  edit (a local server's by name prefix, an endpoint's per kept model);
  `withoutRoute` is the config an edited route is validated against (its
  own name and IDs free).
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
  --model --label --replace`. `--replace` with an existing route of that
  name: same kind = an in-place edit, other kind = remove + add; chain
  entries that go away are dropped; no such route = a plain add. Without
  it a taken name is still an error, which names the flag.

## `internal/term` and the provider manager

- `internal/term`: raw mode via syscall ioctls (TIOCGETA/TIOCSETA on
  darwin/BSD, TCGETS/TCSETS on linux; elsewhere `Supported=false`).
  VMIN=0/VTIME=1 gives a 100ms read timeout, so a lone Esc is told from a
  sequence and a key reader stops without stealing the next prompt's input.
  The `Decoder` is pure. Shift/option-arrows (`;2`/`;3`/`;4`, `\e\e[A`, rxvt
  `\e[a`) all count as Shift.
- `cmd/shelp/manager.go` is the pure model (`Update`/`View` emitting
  probe/setup/save effects; a probe effect with `fresh` calls
  `provider.ResetDetectCache()` first: `r`); `manager_detail.go` the
  details page; `manager_keys.go` the key hints; `manager_form.go` the add
  form, also the edit form (`newEditForm`); `manager_run.go` the loop
  (restores the terminal on return, panic, SIGTERM and SIGHUP);
  `manager_tty.go` the alt screen (use `\r\n`: output processing is off in
  raw mode). `interrupt.go`: `exitStatus`/`errInterrupted`.
- Entered by `shelp providers -i|--interactive` and `shelp config chain` on
  a tty.
- Pages: the list and a provider's details (`modeDetail`), one screen.
  `m.back` is the page a question (`modeConfirm`) or the form returns to;
  `m.page()` is the page drawn under them. `sync()` leaves the details for
  the list when their provider is gone (a removed route).
- Details (→/enter/`l` on any row): label, where it runs (`whereSentence`,
  one plain sentence per kind), status, what its check detected, chain
  place, note, "To make it work" (`fixLines`: the provider's Fix plus what
  only the manager knows; fm's `sudo` command on its own line, never run)
  and, for a route, its entry (`routeLines`: base URL, key variable set or
  not, never the value, models, label). Keys: ←/esc/`h`/`q` back, ↑↓
  neighbours, space, `r` (this one, fresh), `e`, `x`, `s`, pgup/pgdn
  scroll. Long lines wrap (`style.Wrap`, which reopens a color at a
  break) instead of being cut.
- The only action that leaves the screen is the Apple Shortcut setup
  (`canSetupShortcut`: an Apple-cloud row whose Fix names `shelp setup
  shortcuts`), enter on its details: Stop, `runShortcutsStep`,
  `ResetDetectCache`, Start, back on the same page with every Apple-cloud
  row re-probed (`setupDone`) and the result in the status line. No
  "press any key"; that remains only for a save to a file managed
  elsewhere.
- Key hints: one dim line of "KEY to ACTION" phrases joined by " · ",
  lowercase, for the row under the cursor, ending "? for shortcuts"
  (`listHints`/`detailHints`, `fitHints` drops the lowest `keep` rank on a
  narrow terminal); `?` swaps it for `keyGrid` (up to four columns,
  `style.PadRight`-aligned, fewer when narrow, last cell "? to close") plus
  an "also:" line of alternates; esc closes the grid before anything else.
  Modifiers are words (shift, opt, ctrl), never glyphs; arrows are fine.
  Hints lead with the obvious keys; the alternates (j/k, K/J, +/-, l/h,
  g/G) keep working. A confirm question says its own keys and stands alone.
- Edit (`e`, list or details, on a route from the file or added this
  session; a built-in server says to add one of its name instead): the add
  form filled in, kind fixed, the chain question only for new IDs.
  `editRoute` replaces a pending add, updates a pending edit, or records a
  new `routeEdit`; the chain follows renamed IDs in place and drops gone
  ones, and the status line lists both. Removing an edited route removes
  the file's entry by its original name.
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
    Compare termios with PENDIN (0x20000000 in lflag) masked: macOS sets
    that state bit when the pty goes back to canonical mode.
  - A real-pty check of the Shortcut setup round trip needs no real
    backend: with `SHELP_TEST_NO_BACKENDS=1`, fake `shortcuts`, `open` and
    `osascript` scripts first on PATH inside `$TMPDIR` are the only ones
    the gate lets run (`open` touches a marker that the fake `shortcuts
    list` then reports; `shortcuts run` answers `shelp-shortcut 6`).

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

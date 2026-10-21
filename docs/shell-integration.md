# Shell integration (`internal/shellinit/`): the trap catalogue

Moved out of CLAUDE.md verbatim (2026-10-20), plus the polish-branch facts. CLAUDE.md keeps the rules.

`internal/shellinit/` — `shelp init zsh|bash|fish` (raw line mode,
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
runs the line as typed. Raw line mode still decides `explain`/`-e` and
`suggest` (raw explain/suggest, except text starting with `-`, which runs
as typed so its flags are parsed: before v0.3.1 `shelp explain -p fm ls`
sent `-p fm ls` to the model as the command), `''` and `-*` (run as
typed: flags need the shell's parsing) itself; for anything else `_shelp_line_is_task` passes the words split
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
below under "Dynamic completion"). zsh defines `_shelp` and registers with `compdef` at once, or,
when `compdef` does not exist yet (init ran before compinit), from a
one-shot precmd hook; it never calls compinit, and as a `#compdef` file
on fpath (Homebrew) it runs itself. bash's reply drops the part of a
provider ID before its colon (COMP_WORDBREAKS) and has no `-o default`, so
a task never completes as a filename. fish is one
`complete -c shelp -f -a '(__shelp_complete)'` (`complete -e` first, so
loading twice is harmless). The pty suite's Tab checks use a config with
deterministic `zzhost:*` IDs; bash needs a third Tab to list.

## Added 2026-10-20 (polish branch, merged)

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
  makes the stub print stderr; `SHELP_STUB_ARGV_LOG` logs each
  suggest/explain argv (`flag_checks` assert flags arrive as flags); `SHELP_STUB_COMMANDS` also feeds
  `__complete`. Sessions get `XDG_STATE_HOME`, `XDG_CACHE_HOME`,
  `SHELP_HISTORY=0`, and SIGINT reset to the default in the child (a suite
  run as an `&` background job inherited an ignored SIGINT and bash's
  Ctrl-C checks failed). Importing the script as a module re-execs under
  `uv run` when pyte is missing. Tab checks poll (~5s) because the stub is
  a slow Python script.

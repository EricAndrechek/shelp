# shelp bench

`shelp-bench` runs shelp's benchmark: cases x shells x os x providers x
templates x samples. It does **not** grade answers itself — grading is done
by a human-supervised Claude reviewer against the automatic checks and
grading store this tool maintains. The harness's job is to run trials
cheaply, apply mechanical checks, store grades durably, and report.

```sh
make bench-build              # -> bin/shelp-bench
./bin/shelp-bench run --providers available
./bin/shelp-bench pending --format md | less
./bin/shelp-bench pending > review.jsonl   # edit: add "score" (0/1/2), optional "note"
./bin/shelp-bench grade review.jsonl
./bin/shelp-bench report --readme README.md
```

Run these from the repo root: case files, run output, and the grades store
are all relative paths (`bench/cases`, `bench/runs`, `bench/grades.jsonl`).

## Case format (`bench/cases/*.toml`)

Each file holds a list of `[[case]]` tables (see `internal/bench/case.go`
for the authoritative struct and `internal/bench/testdata/cases/example.toml`
for a small fixture set used by this package's own tests/smoke runs — the
real case set lives here in `bench/cases/*.toml`).

| field        | meaning |
|---|---|
| `id`         | unique, kebab-case |
| `mode`       | `"suggest"` (default) or `"explain"` |
| `input`      | the task (suggest) or command to explain (explain) |
| `shells`     | target shells; default `["zsh"]` |
| `os`         | target case-os keys; default `["macos"]`. See below — this is a label, not host detection |
| `difficulty` | `"easy"` \| `"medium"` \| `"hard"` |
| `tags`       | free-form, used by `--cases tag:x` |
| `rubric`     | what a grader checks: correct behavior, acceptable variants, known-wrong answers |
| `reference`  | answers known to be fully correct; an exact (normalized) match auto-grades 2 |
| `setup`      | the fixture the command assumes (files, a git repo); informational for graders |

Trials = cases x each case's `shells` x each case's `os` x providers x
templates x samples.

## Synthetic context — never detected from the runner's host

A trial's `os` is a **label** (`"macos"` or `"linux"`), not this machine's
real OS. `internal/bench/context.go` maps it to a fixed environment so
results are comparable across machines, no matter what actually ran the
benchmark:

| case os | Context.OS   | Userland | Cwd (`cwd` field)       | Tools (`tools` field) |
|---|---|---|---|---|
| `macos` | `macOS 27.0` | `BSD`    | `/Users/bench/project`  | brew, git, gh, jq, rg, fd, fzf, docker, kubectl, uv, pnpm, node, python3, go |
| `linux` | `Ubuntu 24.04` | `GNU`  | `/home/bench/project`   | apt, git, gh, jq, rg, fd, fzf, docker, kubectl, uv, pnpm, node, python3, go |

`--context` (default `os,shell`) selects which of `os,shell,cwd,tools` are
included in the rendered prompt; `shell` comes from the case's `shells`
entry for that trial, not from this host's `$SHELL`.

## Commands

### `run`

```
shelp-bench run [--providers a,b|available] [--templates v1,...]
                 [--cases ids|tag:x|mode:suggest|glob] [--samples N]
                 [--context os,shell,...] [--concurrency N]
                 [--min-interval duration]
                 [--out bench/runs/<UTC timestamp>] [--resume dir]
                 [--cases-dir dir]
```

- `--providers available` (the default) probes every registered provider's
  `Detect` in parallel with a timeout and uses the ones that pass, printing
  which were skipped and why. A comma list instead resolves those exact IDs
  via `provider.Get`, without probing — useful for testing a provider you
  know is flaky.
- `--templates` defaults to just the CLI's default template
  (`internal/prompt.Default`); pass a list to compare templates.
- `--cases` selectors are comma-separated and OR'd together: an exact case
  id, `tag:x`, `mode:suggest`/`mode:explain`, or a glob (`filepath.Match`
  syntax) against the case id. Omit for every case.
- Every call mirrors `cmd/shelp/main.go`'s `runGenerate` exactly: suggest
  mode passes no `OnChunk` and cleans with `clean.Command`; explain mode
  passes a no-op `OnChunk` (the CLI streams explain to the terminal; the
  benchmark just needs the same timing behavior, not the output) and cleans
  with `clean.Text`. Timing comes from `provider.Call`, not `provider.Chain`
  — the benchmark tests one provider at a time, no fallback.
- Concurrency: each provider gets its own semaphore sized `--concurrency`
  (default 1, i.e. serialized — safe for `fm` and Shortcuts); different
  providers always run in parallel with each other. The first trial
  dispatched for a provider is marked `"cold": true`. `--min-interval`
  additionally paces consecutive calls to the SAME provider at least that
  far apart (across all its trials, independent of `--concurrency`) — use
  it to stay under a backend's known rate limit, e.g.
  `--min-interval 2s`.
- **Rate-limit circuit breaker**: a trial whose call comes back as
  `*provider.RateLimitedError` (a usage-limit/quota error, not just a
  plain failure — see the root README's "Rate limits and cooldowns" and
  CLAUDE.md) trips a breaker for that provider, the first time, for the
  rest of the run: every other trial for that provider not yet dispatched
  is skipped — **not run, and not written to `results.jsonl`** — so a
  later `--resume <dir>` treats them as still pending and fills them in.
  One line is printed when this happens: `provider X rate-limited after N
  calls: <reason>; skipping its remaining M trials (resume later with
  --resume <dir>)`. This is exactly what protects against a repeat of the
  2026-09-30 incident where a rate-limited Apple Intelligence Pro shortcut
  kept getting dispatched (and kept posting a macOS notification on every
  failure) for the rest of a run.
- Results stream to `results.jsonl` as each trial finishes (fsync'd per
  line), so a crash mid-run keeps every finished trial. `--resume <dir>`
  recomputes the same trial grid from your flags and skips any trial whose
  key (case, shell, os, provider, template, sample) is already present —
  this is also how a breaker-skipped trial gets filled in on a later run.
- `run.json` records the invocation: args, host OS, `shelp` git commit,
  every provider's `Detect` result as of run start, templates, cases,
  samples, context fields, concurrency, start/end time, and
  `breaker_trips` (below).

### `pending`

```
shelp-bench pending [--run dir ...] [--cases-dir dir] [--grades file]
                     [--limit N] [--format jsonl|md]
```

Walks the given run directories (default: every subdirectory of
`bench/runs/` with a `results.jsonl`), computes each trial's `grade_key`,
and groups by it: trials whose cleaned answer normalizes identically share
one record, however many providers/templates/samples/runs produced it. A
record is left out once it's resolvable — either an exact (normalized)
match against the case's `reference` answers, or already present in
`bench/grades.jsonl` — so the reviewer never sees the same answer twice.
Sorted by case, then by descending count (the most-produced wrong answers
first). `--format jsonl` (default) is shaped to feed straight into `grade`
once you add `"score"`; `--format md` is a readable sheet.

### `grade`

```
shelp-bench grade [--grades file] [--grader name] [--force] [file.jsonl]
```

Reads jsonl (file or stdin) shaped like `pending`'s output plus `"score"`
(0, 1, or 2 — see below) and optional `"note"`. Unknown JSON fields are
rejected by default (catches a typo'd key); `--force` accepts them, ignored.
Appends one record per line to the grades store (default
`bench/grades.jsonl`), stamping `grader` (default `"claude"`) and
`graded_at`. A later line for the same `grade_key` overrides an earlier one
— regrading is just grading again.

Score meaning:

| score | meaning |
|---|---|
| 2 | correct and idiomatic for the shell/OS |
| 1 | works, with caveats or a minor inaccuracy |
| 0 | wrong, unsafe, or unusable |

### `report`

```
shelp-bench report [--run dir ...] [--cases-dir dir] [--grades file]
                    [--out bench/RESULTS.md] [--readme README.md]
```

Writes a markdown report from every given run (default: all of
`bench/runs/`):

1. **Summary**: trials, graded%, mean score (as a % of 2), fully-correct%,
   format-clean%, unavailable%, suggest latency p50/p90, explain
   first-chunk p50 and total p50, and cold-call p50 — per provider x
   template x **context-field set** (the set of `os,shell,cwd,tools` fields
   actually populated in each trial's recorded `context`, from
   `bench.ContextSet`, not from `run.json`). Two runs of the same
   provider/template made with different `--context` flags (e.g. one with
   `os,shell`, one with `os,shell,tools`) get separate rows instead of being
   silently averaged together, since their prompts differed. A **Context**
   column only appears when more than one context-field set is present
   across the loaded runs; with the common case of a single context-field
   set, the table is identical to a plain provider x template summary.
2. **By difficulty** and **by mode**: the same graded%/mean-score%/
   fully-correct% breakdown.
3. **Per-case grid**: rows are cases, columns are providers; each cell is
   the mean graded score for that pair (averaged across every
   template/shell/os/sample/context — this grid is not context-aware).
   ✅ mean rounds to 2, 🟡 rounds to 1, ❌ rounds to 0, · ungraded.
4. **Prompt templates**, compared only over the provider set common to
   every template present *within the same context-field set*, so the
   comparison is apples-to-apples; also gets a Context column (and a
   separate common-provider set per context) when more than one
   context-field set is present.

Ungraded trials count toward `Trials` everywhere but are excluded from every
accuracy percentage. `--readme` additionally replaces the text between
`<!-- bench:start -->` and `<!-- bench:end -->` in that file with table (1)
plus a one-line footnote (date, case count, samples); it errors if the
markers aren't there rather than silently doing nothing.

### `latency`

```
shelp-bench latency --providers a,b [--n 10] [--binary path] [--prompt text]
```

Measures what a user actually feels: builds `./cmd/shelp` to a temp binary
(unless `--binary` is given) and runs `shelp -p <id> --json <prompt>` as a
real subprocess, N times per provider, sequentially. Reports cold (the
first call) vs warm p50/p90 (process spawn through output, so it also
includes exec overhead beyond `Generate`'s own latency_ms). Appends to
`bench/latency.jsonl`.

## `results.jsonl` schema (one object per trial)

```jsonc
{
  "case": "find-large-files", "mode": "suggest", "shell": "zsh", "os": "macos",
  "provider": "fm", "template": "v1",
  "context": {"os": "macOS 27.0", "userland": "BSD", "shell": "zsh"},
  "sample": 1,
  "raw": "Sure, here's the command:\nfind ~ -type f -size +100M\n",
  "output": "find ~ -type f -size +100M",
  "error": "", "unavailable": false, "rate_limited": false,
  "latency_ms": 612, "first_chunk_ms": 612, "cold": true,
  "checks": {
    "format_clean": false,
    "parses": true,
    "commands_exist": true,
    "gnuisms": [],
    "dangerous": []
  },
  "grade_key": "3a7f...", "started_at": "2026-09-30T12:00:00Z"
}
```

`checks` fields are omitted when the check didn't apply: `parses` and
`commands_exist` are `null`/absent when the needed shell isn't installed;
`commands_exist` is also absent when the trial's case `os` doesn't match the
host actually running the benchmark (it needs a local `command -v`);
`gnuisms` only runs for `os = "macos"`. See "Automatic checks" below for
exactly what each one flags.

`rate_limited` (omitted when `false`) is `true` exactly when `error` came
from a `*provider.RateLimitedError` — a usage-limit/quota error
specifically, a stricter condition than `unavailable` (which is also `true`
in this case, since a rate limit is a form of unavailability). A trial
skipped outright by the circuit breaker (see `run`, above) never produces
a `results.jsonl` line at all, rate-limited or otherwise.

`run.json`'s `breaker_trips` (empty/absent when nothing tripped) has one
entry per provider that rate-limited during the run:

```jsonc
{
  "provider": "shortcuts:apple-pro",
  "reason": "Error: You have reached the usage limit for this model. Please try again later.",
  "after_calls": 93, "skipped_trials": 240,
  "at": "2026-09-30T12:03:00Z"
}
```

## `grades.jsonl` schema (one object per grade, append-only)

```jsonc
{"grade_key": "3a7f...", "case": "find-large-files", "answer": "find ~ -type f -size +100M",
 "score": 2, "note": "", "grader": "claude", "graded_at": "2026-09-30T13:00:00Z"}
```

Later lines for the same `grade_key` override earlier ones; nothing is ever
edited in place, so the file is a complete audit trail of regrades.

`grade_key` = hex sha256 of `case_id | shell | os | normalized_answer`.
Normalization (also used for `reference` matching, so both use the same
rule): trim; for suggest mode, collapse runs of whitespace *outside* single-
or double-quoted spans to one space (whitespace inside quotes is part of the
command and is preserved) and drop one trailing `;`; for explain mode, trim
and collapse all whitespace.

## Automatic checks (suggest mode)

Run per trial, recorded in `checks`, never a substitute for grading:

- **`format_clean`**: the raw reply was already just the command (cleaning
  changed nothing but whitespace) and has no prose lead-in (`"Here"`,
  `"To "`, `"You can"`, `"Sure"`).
- **`parses`**: a syntax-only check, never executing the command:
  `zsh -n -c`, `bash -n -c`, `sh -n -c`, or `fish --no-execute -c`,
  whichever matches the trial's target shell. `null` when that shell isn't
  installed on the runner.
- **`commands_exist`** (only when the trial's case `os` matches the host
  actually running the benchmark): the command is parsed with
  `mvdan.cc/sh/v3/syntax` (`LangBash` for the `bash`/`zsh` target shells,
  `LangPOSIX` for `sh`; `fish` skips this check entirely, since its syntax
  differs too much from bash/POSIX to parse safely as either) and every
  `CallExpr`'s first argument is taken as a candidate command name — but
  only when it's a plain literal (`Word.Lit()` non-empty): a `$x` variable,
  a `"$(...)"`/backtick substitution, or a quoted string is never treated
  as a command name. Real parsing (rather than the naive regex splitter
  this replaced) is what keeps zsh arithmetic (`(( ... ))`), `case`
  patterns, and a quoted awk/sed/grep program from ever being misread as a
  command word in the first place — the old splitter produced false
  "missing command" hits like `((`, `master)$'`, `sub(/^`, `c[n]++`, and
  `HEAD)(N)` on exactly these constructs. Leading `VAR=val` assignments and
  shell builtins/keywords are skipped, same as before. Some zsh-only
  syntax (a `repeat` loop, for example) genuinely fails to parse as bash —
  glob qualifiers like `*(N)`, `${f:l}`-style modifiers, `print -z`, and
  `(( ... ))` itself all parse fine and do NOT hit this — and on a parse
  error the check falls back to the old regex-based splitter, but now also
  drops any candidate containing a character that can't appear in a real
  command name (`` (){}[]'"$;|&<>= `` and `/`, unless the candidate is a
  path that actually exists), so a parse failure can't reintroduce the
  same false positives through the fallback path. Lists any missing names.
- **`gnuisms`** (`os = "macos"` only): named regex/heuristic rules for
  GNU-only usage that behaves differently or fails on stock macOS —
  `sed -i` without a backup-suffix argument, `grep -P`, `find -printf`,
  `date -d`, `stat -c`/`--format`, `xargs -r`, `du --max-depth`,
  `ls --color`, `readlink -e`, `tac`, `nproc`, `free -*`, `ip a|addr|route`,
  `ss -*`, `netstat -tulpn`, `systemctl`, `apt`/`apt-get`, `timeout`.
  `sort -h` is explicitly not flagged (it works on macOS).
- **`dangerous`** (always): `rm -rf /`, `rm -rf ~`, `sudo `, `dd if=`,
  `mkfs`, `chmod -R 777`, `curl|wget ... | sh|bash`, `> /dev/sd*`, a fork
  bomb (`:(){ :|:& };:`).

Explain mode only runs `format_clean`, redefined as: no markdown (headings,
`**`, bullets, fences). Everything else is left to the grader.

## Grading and normalization

An answer that normalizes equal to one of its case's `reference` answers is
auto-graded 2 with grader `"reference"` — computed on the fly by `pending`
and `report`, never written to `bench/grades.jsonl`.

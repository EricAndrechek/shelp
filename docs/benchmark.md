# Benchmark

`shelp-bench` runs the cases in `bench/cases/*.toml` against the providers
and prompt templates you choose, checks each answer automatically (syntax,
dangerous commands, GNU-only flags on macOS, and so on), and writes a
graded report. [bench/README.md](../bench/README.md) has the commands, the
case format and the scoring rules.

```sh
make bench-build
./bin/shelp-bench run --providers available --min-interval 15s
./bin/shelp-bench report --readme README.md
```

A provider that hits a rate limit is stopped for the rest of the run, and
`--resume <dir>` fills in its trials later. `--min-interval` spaces out
calls to the same provider so it stays under its limit.

## How the numbers were measured

The 2026-09-30 run (M4 Pro, macOS 27.0) used 81 cases: 67 tasks to suggest
a command for and 14 commands to explain. They cover files, text, processes
and networking, git, system tasks, archives and media, and shell syntax,
with many of the usual macOS/BSD versus Linux/GNU traps (`sed -i ''`,
`date -v` versus `-d`, `ps --sort`, `du -d`). Each case runs once for every
shell and OS it lists, 112 trials per provider and template.

Claude Opus graded every distinct answer 0, 1 or 2 against the case's
rubric, and doubtful claims were checked by running them on a Mac.
Identical answers share a grade, and answers on a case's reference list
score 2. "Mean score" is the average grade as a percentage of 2. Grades
are in `bench/grades.jsonl`, and [bench/RESULTS.md](../bench/RESULTS.md)
has the full report, dropped backends included.

## Findings

These are from the v2 template, before v4 became the default. v4 changes
only how explanations are formatted, so the suggest results still hold. A
new run is tracked in [#1](https://github.com/EricAndrechek/shelp/issues/1).

- Apple Intelligence Pro (through Shortcuts) was the most accurate when it
  was available: 98%, about 1.3s, at no cost. It hit its usage limit after
  about 100 quick calls and stayed limited for more than 1.5 hours, so only
  31 of its 112 trials ran.
- Claude Sonnet is the best fallback: 97% at about 2.2s, using your Claude
  plan.
- Claude Haiku isn't worth it. With thinking on it scored 89% but took
  6.6s; with it off it takes 1.8s and scores 84% with structured output.
- Apple's on-device `fm` is fast (about 0.35s) and private but weak at
  31–36%. It invents flags and tools and falls for most BSD/GNU traps. It
  is still worth keeping as the offline fallback.
- v2 beat v1 for every model (Apple Intelligence Pro 94% to 98%, `fm` 31% to
  36%). v3 added worked examples and helped no one; small models copied the
  examples' shape.
- Telling the model which tools are installed (`--context ...,tools`) made
  `fm` worse (36% to 32%), so it stays opt-in.
- Codex CLI (about 95%) and Copilot CLI (about 94%) were dropped for how
  their tools are controlled, not for quality; see
  [agentcli.md](agentcli.md).

End-to-end latency, from process start to printed command (warm median of
5): `fm` 0.35s (1.25s cold), `claude:haiku` 1.8s, `claude:sonnet` 2.1s.

## Limits

One sample per trial. The grader is a Claude model, so a bias toward
Claude-style answers can't be ruled out, though every rubric names
concrete pass and fail criteria. Linux answers were graded for correctness
on the stated OS, not run on Linux.

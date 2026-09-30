# shelp

Turn a task into a shell command (`shelp find files over 100MB`), or explain
one (`shelp explain 'lsof -nP -iTCP:8080'`), using whatever model the user
has: Apple's on-device model (`fm`), Apple's cloud models via Shortcuts,
OpenAI-compatible endpoints (hosted with a key, or local: LM Studio, Ollama,
llama-server), or installed AI CLIs (claude, gemini, codex, copilot).
Successor to the `ghcs`/`ghce` shell functions in Eric's dotfiles.

Local-only repo for now (no remote). Module `github.com/ericandrechek/shelp`.

## Layout

- `cmd/shelp/` — the CLI. Subcommands: suggest (default), explain, providers,
  setup, init, version. Suggest prints ONLY the command on stdout.
- `internal/provider/` — `Provider` interface, family registry, fallback
  `Chain`, timing `Call` (`provider.go`), plus the `RunCmd` exec helper
  (`execx.go`). One file per family: `fm.go` (Apple on-device), `openai.go`
  (OpenAI-compatible hosted endpoints + local servers, +`openai_test.go`),
  `shortcuts.go` (the `shortcuts:*` providers, +`shortcuts_test.go`) and
  `shortcutgen.go` (builds/signs the "Use Model" `.shortcut` plist,
  +`shortcutgen_test.go`, `testdata/shortcuts/reference.json`); `agentcli.go`
  (claude/gemini/codex/copilot) is still being built, not yet merged.
- `internal/prompt/` — prompt `Template`s (named, versioned; `Default`) and
  environment `Context` detection (os, shell, opt-in cwd/tools).
- `internal/clean/` — strips fences/backticks/prompt signs from replies.
- `internal/shellinit/` — `shelp init zsh|bash|fish` scripts (embedded).
- `internal/config/` — `~/.config/shelp/config.toml`; each family decodes its
  own `[table]` via `cfg.Section(name, &v)`.
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

## Conventions

- Go 1.27, stdlib first; deps so far: BurntSushi/toml.
- `make vet test build`. Keep `shelp` startup < 10ms: no work in init()
  beyond registration.
- Commits: one change per commit; this repo has no remote.

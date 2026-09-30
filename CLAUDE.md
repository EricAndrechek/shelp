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
  `Chain`, timing `Call`, `RunCmd` exec helper. One file per family:
  `fm.go`, `shortcuts.go`, `openai.go`, `agentcli.go`.
- `internal/prompt/` — prompt `Template`s (named, versioned; `Default`) and
  environment `Context` detection (os, shell, opt-in cwd/tools).
- `internal/clean/` — strips fences/backticks/prompt signs from replies.
- `internal/shellinit/` — `shelp init zsh|bash|fish` scripts (embedded).
- `internal/config/` — `~/.config/shelp/config.toml`; each family decodes its
  own `[table]` via `cfg.Section(name, &v)`.
- `internal/bench/` + `cmd/shelp-bench/` — benchmark harness.
- `bench/cases/*.toml` — benchmark cases; `bench/grades.jsonl` — graded
  answers (tracked); `bench/runs/` — raw runs (gitignored).

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

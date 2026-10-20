# agentcli (the `claude` headless CLI): purity and measurements

Moved out of CLAUDE.md verbatim (2026-10-20); CLAUDE.md keeps the rule.

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

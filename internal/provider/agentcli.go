package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// agentcli: providers that shell out to installed AI coding-agent CLIs
// (claude, copilot) in single-shot, non-interactive mode and return their
// raw text reply. These tools are full coding agents by default (they read
// project files, edit files, run shell commands); every spec below
// configures the closest thing each CLI has to "just answer in text", and
// Generate always runs in a fresh empty temp directory so neither picks up
// this repo's own CLAUDE.md/AGENTS.md.
//
// PURITY: every provider in shelp must behave as a single plain chat
// completion -- shelp's system prompt plus one user message in, one text
// answer out. No tools, no file/project access, no memory or instructions
// loaded from the user's own config, no multi-turn agent loop, no hidden
// "thinking" unless the user opts in. Three CLIs that were once wired up
// here (gemini, codex, antigravity/agy) were REMOVED rather than hardened,
// because each one is fundamentally an agent harness that can't be fully
// reduced to a plain completion -- see the "Dropped backends" section of
// bench/README.md (and CLAUDE.md) for why each one specifically fails this
// bar, and don't re-add them without first finding a flag that makes them
// behave as a pure completion. claude and copilot stay because each one
// has a documented, verified way to disable every optional capability;
// see their sections below and CLAUDE.md's "Provider purity" table for the
// measured numbers (input tokens, turns, thinking/tool-call rate) backing
// that claim.
//
// Notes from tuning on this Mac, macOS 27.0, verified 2026-09-30 against
// claude 2.1.285 and GitHub Copilot CLI 1.0.89 (see MERGE NOTES in the
// PR/commit description for the full investigation):
//
//   - claude authenticates via claude.ai (a subscription, not an API key).
//     --bare cannot be used: it forces ANTHROPIC_API_KEY/apiKeyHelper only
//     ("OAuth and keychain are never read") and this account then reports
//     "Not logged in". We use --setting-sources "" (skip user/project/local
//     settings) + --strict-mcp-config (no MCP servers) + --tools "" (no
//     tools at all) + --permission-prompts none + --disable-slash-commands
//     instead, which is auth-neutral. --model accepts the "haiku"/"sonnet"
//     aliases directly. MAX_THINKING_TOKENS=0 is additionally set in the
//     subprocess environment (claudeSpec.extraEnv): haiku defaults to
//     "thinking" even for a trivial one-liner (measured live 2026-09-30:
//     318 output tokens spent on hidden thinking, 3.3s API time, for "list
//     5 recent git commits"; with the env var, 15 output tokens, 0.5s --
//     reproduced here: 15 output tokens / 520ms api time / 0 thinking
//     tokens / num_turns 1 / 392 input tokens). Sonnet doesn't think by
//     default (13 output tokens, 1038ms api time, 0 thinking tokens,
//     num_turns 1, 463 input tokens, measured the same way), so the env
//     var is harmless there -- set unconditionally for the whole claude
//     spec rather than only for haiku. Both calls' small, stable
//     input-token counts (392/463, not thousands) and num_turns=1 confirm
//     no CLAUDE.md/project memory is being loaded and no tool round-trip
//     is happening, matching the --setting-sources ""/--tools "" flags'
//     intent.
//   - copilot's --no-auto-update flag corrupts its own argument parser when
//     combined with -p and -C on the installed 1.0.x build (fails with
//     "error: unknown option '-C'"; reproduced repeatedly, order-independent
//     with -p/-C present, absent when either is dropped) -- omitted.
//     copilot silently self-updates in the background regardless (observed
//     version drift 1.0.80 -> 1.0.89 -> 1.0.40 across a few minutes of
//     local testing); don't hard-fail Detect on an unexpected version
//     string, just report it. "auto" ("use 'auto' to let Copilot pick
//     automatically") is documented in --help and is what copilot:default
//     passes; the --help example model "gpt-5.4" is NOT available on this
//     account ("Model \"gpt-5.4\" from --model flag is not available"), so
//     it is deliberately not shipped as a second default variant.
//     `copilot help permissions` documents "--available-tools disables all
//     other tools" -- but PASSING THE BARE FLAG WITH NO VALUE DOES NOT
//     ACTUALLY RESTRICT ANYTHING (verified live 2026-09-30: with a bare
//     --available-tools, the model still invoked its built-in "bash" tool
//     and ran a real shell command in the scratch dir); the fix is an
//     EXPLICIT empty value, `--available-tools=""` (one empty-string list
//     element, not zero elements) -- verified live to produce toolRequests:
//     [] and a single model.call for an easy prompt. That alone is still
//     not sufficient, though: for a prompt more naturally suited to running
//     a command ("find files over 100MB"), the model called "bash" again
//     even with `--available-tools=""` and `--disable-builtin-mcps` both
//     set -- the built-in bash/shell tool is apparently not gated by
//     --available-tools at all. What reliably suppresses it (verified
//     live, same prompt, zero tool.execution_start events, single
//     model.call, clean text answer) is `--deny-tool shell`, which
//     `copilot help permissions` documents as a separate, higher-precedence
//     permission layer ("Denial rules always take precedence over allow
//     rules, even --allow-all-tools"). `--disable-builtin-mcps` is kept
//     too, so the CLI never even connects to its built-in GitHub MCP
//     server (which otherwise loads that server's tool-usage instructions
//     into session state on every call, observed live, even though
//     --available-tools="" keeps its tools out of reach). --reasoning-effort
//     none is documented in --help (possible values: none, minimal, low,
//     medium, high, xhigh, max) and was verified live to be accepted by
//     the account's current default (auto-routed) model with no error.
//
// Every call sets execx.go's RunCmdOpts.Dir to the scratch temp dir. Every
// call is also wrapped in its own bounded timeout (generateTimeout) on top
// of the caller's context: agent CLIs are full coding agents that can, in
// principle, decide to prompt for something we didn't anticipate, and
// Generate must fail fast rather than hang the whole `shelp` process.

// generateTimeout bounds one agent-CLI call, on top of whatever deadline
// the caller's context already carries (context.WithTimeout only ever
// shortens). 60s leaves headroom for a slow network without leaving a
// stuck prompt hanging indefinitely.
const generateTimeout = 60 * time.Second

// agentModel is one model variant of a CLI: id is the shelp-facing suffix
// ("haiku", "default"), cli is the literal value passed to the CLI's
// model flag. cli == "" means omit the flag entirely and let the CLI/
// account resolve its own default.
type agentModel struct {
	id  string
	cli string
}

// cliSpec is one agent-CLI family member: everything needed to build,
// run and parse a call to one installed binary.
type cliSpec struct {
	name          string // binary name on PATH, and the shelp ID prefix
	label         string // human label for Description()
	defaultModels []agentModel
	identify      func(ctx context.Context) Status
	// buildArgs returns the argv (excluding the binary name itself). dir is
	// a fresh, empty, just-created temp directory. model is the resolved
	// CLI flag value ("" means omit the model flag).
	buildArgs func(dir, model string, req Request) []string
	// extraEnv, when non-empty, is appended to os.Environ() for the
	// subprocess (e.g. claude's MAX_THINKING_TOKENS=0). Empty means
	// inherit this process's environment unchanged (RunCmdOpts' normal
	// nil-Env behavior).
	extraEnv []string
	// parse extracts the reply from a finished call, or maps failure to an
	// *UnavailableError via Unavailable(id, ...).
	parse func(id string, res CmdResult) (string, error)
}

// resolveModels turns a config override (raw model-flag strings from
// [agentcli.<name>] models) into agentModels, or returns the built-in
// defaults when there is no override. A configured name that matches one
// of the built-in ids (e.g. "default", "haiku") reuses that variant's CLI
// flag value; any other name is passed through verbatim as both id and CLI
// flag value, so `models = ["gemini-2.5-flash"]`-style overrides still work
// once a user knows a real model name for their account.
func (s cliSpec) resolveModels(names []string) []agentModel {
	defaults := s.defaultModels
	if len(names) == 0 {
		return defaults
	}
	byID := make(map[string]agentModel, len(defaults))
	for _, m := range defaults {
		byID[m.id] = m
	}
	out := make([]agentModel, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if m, ok := byID[n]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, agentModel{id: n, cli: n})
	}
	return out
}

var agentCLISpecs = []cliSpec{claudeSpec, copilotSpec}

// agentcliCLIConfig is one CLI's [agentcli.<name>] table.
type agentcliCLIConfig struct {
	Models   []string `toml:"models"`
	Disabled bool     `toml:"disabled"`
}

// agentcliConfig is the [agentcli] section: one sub-table per CLI.
type agentcliConfig struct {
	Claude  agentcliCLIConfig `toml:"claude"`
	Copilot agentcliCLIConfig `toml:"copilot"`
}

func (c agentcliConfig) forName(name string) agentcliCLIConfig {
	switch name {
	case "claude":
		return c.Claude
	case "copilot":
		return c.Copilot
	default:
		return agentcliCLIConfig{}
	}
}

type agentCLIFamily struct{}

func init() { Register(agentCLIFamily{}) }

func (agentCLIFamily) Name() string { return "agentcli" }

// Providers is cheap: config decode + exec.LookPath per CLI, no processes.
func (agentCLIFamily) Providers(cfg *config.Config) []Provider {
	var acfg agentcliConfig
	_ = cfg.Section("agentcli", &acfg)

	var out []Provider
	for _, spec := range agentCLISpecs {
		if _, ok := LookPath(spec.name); !ok {
			continue
		}
		sub := acfg.forName(spec.name)
		if sub.Disabled {
			continue
		}
		for _, m := range spec.resolveModels(sub.Models) {
			out = append(out, &agentcliProvider{
				spec:     spec,
				cliModel: m.cli,
				id:       spec.name + ":" + m.id,
			})
		}
	}
	return out
}

type agentcliProvider struct {
	spec     cliSpec
	cliModel string // "" means the CLI's own default; no -m/--model flag
	id       string
}

func (p *agentcliProvider) ID() string { return p.id }
func (p *agentcliProvider) Kind() Kind { return AgentCLI }

func (p *agentcliProvider) Description() string {
	m := p.cliModel
	if m == "" {
		m = "default"
	}
	// Most labels ("Claude Code") don't end in "CLI" and need it appended;
	// "GitHub Copilot CLI" already does, and appending unconditionally used
	// to read "GitHub Copilot CLI CLI".
	label := p.spec.label
	if !strings.HasSuffix(label, "CLI") {
		label += " CLI"
	}
	return label + ", model " + m + " (headless, no tools)"
}

func (p *agentcliProvider) Detect(ctx context.Context) Status {
	return p.spec.identify(ctx)
}

func (p *agentcliProvider) Generate(ctx context.Context, req Request) (string, error) {
	dir, err := os.MkdirTemp("", "shelp-agentcli-*")
	if err != nil {
		return "", Unavailable(p.id, "could not create a scratch directory: %v", err)
	}
	defer os.RemoveAll(dir)

	args := p.spec.buildArgs(dir, p.cliModel, req)

	runCtx, cancel := context.WithTimeout(ctx, generateTimeout)
	defer cancel()

	onChunk := req.OnChunk // both remaining CLIs' stdout is exactly the reply

	var env []string
	if len(p.spec.extraEnv) > 0 {
		env = append(append([]string(nil), os.Environ()...), p.spec.extraEnv...)
	}

	res, err := RunCmdOpts(runCtx, CmdOpts{Dir: dir, Env: env, OnChunk: onChunk}, p.spec.name, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", Unavailable(p.id, "%s timed out (shelp caps each agent-CLI call at %s; a stale login can make some agent CLIs hang on an interactive re-auth/trust prompt in headless mode -- try running `%s` interactively once)", p.spec.name, generateTimeout, p.spec.name)
		}
		return "", Unavailable(p.id, "%s: %v", p.spec.name, err)
	}
	return p.spec.parse(p.id, res)
}

// unavailableMsg picks the short reason to report: the last line starting
// with "ERROR:" (any case) in stderr, then stdout; else stderr's first line,
// falling back to stdout's, all ANSI-stripped.
func unavailableMsg(res CmdResult) string {
	for _, out := range []string{res.Stderr, res.Stdout} {
		lines := strings.Split(stripANSI(out), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); strings.HasPrefix(strings.ToUpper(l), "ERROR:") {
				return l
			}
		}
	}
	msg := FirstLine(stripANSI(res.Stderr))
	if msg == "" {
		msg = FirstLine(stripANSI(res.Stdout))
	}
	return msg
}

// agentRateLimitPhrases are substrings that, found (case-insensitively) in
// a failed agent-CLI call's stderr/stdout on a non-zero exit, mean the
// account is rate-limited or out of quota rather than some other failure
// (auth, a bad flag, an unsupported model, ...). Every parse func below
// checks this first, ahead of its own CLI-specific error mapping.
var agentRateLimitPhrases = []string{"rate limit", "quota", "usage limit", "429", "resource exhausted"}

// agentRateLimitReason reports whether msg (already extracted via
// unavailableMsg) names a known rate-limit/quota phrase.
func agentRateLimitReason(msg string) (string, bool) {
	low := strings.ToLower(msg)
	for _, phrase := range agentRateLimitPhrases {
		if strings.Contains(low, phrase) {
			return msg, true
		}
	}
	return "", false
}

// ==================== claude ====================

var claudeSpec = cliSpec{
	name:  "claude",
	label: "Claude Code",
	defaultModels: []agentModel{
		{id: "haiku", cli: "haiku"},
		{id: "sonnet", cli: "sonnet"},
	},
	identify:  claudeIdentify,
	buildArgs: claudeArgs,
	// MAX_THINKING_TOKENS=0: haiku otherwise spends output tokens on hidden
	// "thinking" even for a trivial one-line request (measured live
	// 2026-09-30: 318 output tokens / 3.3s api time -> 15 output tokens /
	// 0.5s with this set; see the file-header note for the full
	// measurement, including confirming sonnet is unaffected either way).
	// This is deliberately the one place shelp adds anything beyond
	// Request.System/Request.Prompt to a call -- see the provider-contract
	// note in CLAUDE.md ("providers don't add instructions") for why an
	// env var that only suppresses a hidden, non-text side channel is the
	// documented exception, alongside copilot's --reasoning-effort/
	// --deny-tool flags below.
	extraEnv: []string{"MAX_THINKING_TOKENS=0"},
	parse:    claudeParse,
}

func claudeIdentify(ctx context.Context) Status {
	v, err := RunCmd(ctx, nil, nil, "claude", "--version")
	if err != nil {
		return Status{Reason: err.Error()}
	}
	line := FirstLine(v.Stdout)
	if v.ExitCode != 0 || !strings.Contains(line, "Claude Code") {
		return Status{Reason: "not Claude Code: " + line}
	}
	st, err := RunCmd(ctx, nil, nil, "claude", "auth", "status")
	if err != nil || st.ExitCode != 0 {
		// auth status is a best-effort extra; version alone is enough.
		return Status{Available: true, Version: line}
	}
	var parsed struct {
		LoggedIn         bool   `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal([]byte(st.Stdout), &parsed) == nil {
		if !parsed.LoggedIn {
			return Status{Reason: "claude not logged in: run `claude /login` (or `claude setup-token`)"}
		}
		if parsed.Email != "" {
			line += " (" + parsed.Email + ", " + parsed.SubscriptionType + ")"
		}
	}
	return Status{Available: true, Version: line}
}

func claudeArgs(dir, model string, req Request) []string {
	args := []string{"-p"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args,
		"--output-format", "text",
		"--setting-sources", "",
		"--strict-mcp-config",
		"--tools", "",
		"--permission-prompts", "none",
		"--no-session-persistence",
		"--disable-slash-commands",
		"--system-prompt", req.System,
		"--", req.Prompt,
	)
	return args
}

func claudeParse(id string, res CmdResult) (string, error) {
	if res.ExitCode != 0 {
		msg := unavailableMsg(res)
		if reason, ok := agentRateLimitReason(msg); ok {
			return "", RateLimited(id, 0, "%s", reason)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "not logged in") || strings.Contains(msg, "/login") {
			return "", Unavailable(id, "claude not logged in: run `claude /login`")
		}
		return "", Unavailable(id, "claude exited %d: %s", res.ExitCode, msg)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return "", Unavailable(id, "claude returned no output")
	}
	return res.Stdout, nil
}

// ==================== copilot ====================

var copilotSpec = cliSpec{
	name:  "copilot",
	label: "GitHub Copilot CLI",
	defaultModels: []agentModel{
		{id: "default", cli: "auto"}, // "--model auto": "let Copilot pick automatically" (documented)
	},
	identify:  copilotIdentify,
	buildArgs: copilotArgs,
	parse:     copilotParse,
}

func copilotIdentify(ctx context.Context) Status {
	// --no-auto-update is safe here: the argv-corruption bug (see file
	// header) only reproduces together with -p and -C.
	v, err := RunCmd(ctx, nil, nil, "copilot", "--no-auto-update", "--version")
	if err != nil {
		return Status{Reason: err.Error()}
	}
	line := FirstLine(v.Stdout)
	if v.ExitCode != 0 || !strings.Contains(line, "GitHub Copilot CLI") {
		return Status{Reason: "not GitHub Copilot CLI: " + line}
	}
	return Status{Available: true, Version: line}
}

func copilotArgs(dir, model string, req Request) []string {
	args := []string{
		"-p", req.System + "\n\n" + req.Prompt,
		"-s", // silent: only the agent's response, no stats/banner
		"-C", dir,
		"--no-color",
		// --available-tools with NO value looks like "zero tools" per
		// `copilot help permissions` ("disables all other tools"), but
		// verified live 2026-09-30 that the bare flag does NOT restrict
		// anything -- the model still invoked its built-in "bash" tool and
		// ran a real command. An EXPLICIT empty value (one empty-string
		// list element, via --available-tools="") is what actually yields
		// an empty tool list (verified live: toolRequests: [], a single
		// model.call). See the file header for the fuller writeup,
		// including why that alone still isn't enough.
		"--available-tools=",
		"--no-custom-instructions",
		// Stop the CLI from even connecting to its built-in GitHub MCP
		// server -- verified live to happen (and load that server's
		// tool-usage instructions into session state) even when
		// --available-tools="" keeps every one of its tools unreachable.
		"--disable-builtin-mcps",
		// The actual fix for the built-in "bash" tool that
		// --available-tools doesn't gate at all: --deny-tool is a
		// separate, higher-precedence permission layer ("Denial rules
		// always take precedence over allow rules, even --allow-all-tools"
		// per `copilot help permissions`). Verified live: with this plus
		// the two flags above, a prompt that previously triggered a real
		// `bash` tool call (a real shell command, in the scratch dir)
		// instead gets a single model call and a clean text answer, zero
		// tool.execution_start events.
		"--deny-tool", "shell",
		// --help lists possible values none|minimal|low|medium|high|xhigh|
		// max; "none" was verified live 2026-09-30 to be accepted by this
		// account's current default (auto-routed) model with no error.
		"--reasoning-effort", "none",
		// NOTE: --no-auto-update deliberately omitted here: combined with
		// -p and -C it breaks copilot's own arg parser (see file header).
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func copilotParse(id string, res CmdResult) (string, error) {
	if res.ExitCode != 0 {
		msg := unavailableMsg(res)
		if reason, ok := agentRateLimitReason(msg); ok {
			return "", RateLimited(id, 0, "%s", reason)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "not available") {
			return "", Unavailable(id, "copilot: %s", msg)
		}
		if strings.Contains(low, "not authenticated") || strings.Contains(low, "login") {
			return "", Unavailable(id, "copilot not logged in: run `copilot login`")
		}
		return "", Unavailable(id, "copilot exited %d: %s", res.ExitCode, msg)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return "", Unavailable(id, "copilot returned no output")
	}
	return res.Stdout, nil
}

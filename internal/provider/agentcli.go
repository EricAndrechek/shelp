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
// (today, just claude) in single-shot, non-interactive mode and return
// their raw text reply. These tools are full coding agents by default
// (they read project files, edit files, run shell commands); every spec
// below configures the closest thing each CLI has to "just answer in
// text", and Generate always runs in a fresh empty temp directory so none
// pick up this repo's own CLAUDE.md/AGENTS.md.
//
// PURITY: every provider in shelp must behave as a single plain chat
// completion -- shelp's system prompt plus one user message in, one text
// answer out. No tools, no file/project access, no memory or instructions
// loaded from the user's own config, no multi-turn agent loop, no hidden
// "thinking" unless the user opts in. Four CLIs that were once wired up
// here (gemini, codex, antigravity/agy, copilot) were REMOVED rather than
// hardened: gemini/codex/antigravity are each fundamentally an agent
// harness that can't be fully reduced to a plain completion; copilot's
// only working tool-suppression flag (--deny-tool shell) is a
// permission-layer denylist, not a structural guarantee -- its
// `--available-tools` (documented as disabling every tool) verifiably did
// NOT stop its built-in bash tool from running a real shell command, so
// there was no structural way to prove no tool call is possible, only a
// best-effort one. See the "Dropped backends" section of README.md (and
// CLAUDE.md) for why each one specifically fails this bar -- including
// copilot's full flag-by-flag investigation, preserved there since it's
// no longer wired up here -- and don't re-add any of them without first
// finding a flag that makes them behave as a pure completion. claude
// stays because it has a documented, verified, structural way to disable
// every optional capability; see its section below and CLAUDE.md's
// "Provider purity" table for the measured numbers (input tokens, turns,
// thinking/tool-call rate) backing that claim.
//
// Notes from tuning on this Mac, macOS 27.0, verified 2026-09-30 against
// claude 2.1.285:
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

var agentCLISpecs = []cliSpec{claudeSpec}

// agentcliCLIConfig is one CLI's [agentcli.<name>] table.
type agentcliCLIConfig struct {
	Models   []string `toml:"models"`
	Disabled bool     `toml:"disabled"`
}

// agentcliConfig is the [agentcli] section: one sub-table per CLI (today,
// just claude -- kept as a table-shaped config, not a single flat one, so
// adding a future CLI back needs no config-schema migration).
type agentcliConfig struct {
	Claude agentcliCLIConfig `toml:"claude"`
}

func (c agentcliConfig) forName(name string) agentcliCLIConfig {
	switch name {
	case "claude":
		return c.Claude
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
	// a label that already does (e.g. the since-removed "GitHub Copilot
	// CLI") must not get it appended again -- appending unconditionally
	// used to read "GitHub Copilot CLI CLI".
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

	// Explain mode's text output is exactly the reply, so it can stream;
	// suggest mode's JSON envelope can't.
	onChunk := req.OnChunk
	if req.Mode == Suggest {
		onChunk = nil
	}

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
	// documented exception.
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

// claudeCommandSchema is the structured-output schema for suggest mode.
// Without it, haiku with thinking off occasionally wraps the command in
// prose ("Wait, that won't preserve order. Let me correct: ...") in ~5% of
// benchmark answers (2026-09-30); with it the reply is always a JSON object
// whose only field is the command. Explain mode stays plain text: prose is
// the answer there.
const claudeCommandSchema = `{"type":"object","properties":{"command":{"type":"string","description":"the shell command only, no prose"}},"required":["command"],"additionalProperties":false}`

func claudeArgs(dir, model string, req Request) []string {
	args := []string{"-p"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if req.Mode == Suggest {
		args = append(args, "--output-format", "json", "--json-schema", claudeCommandSchema)
	} else {
		args = append(args, "--output-format", "text")
	}
	args = append(args,
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
	// Suggest mode asks for --output-format json with a schema: unwrap the
	// result envelope. Explain mode's plain text never parses as one.
	var env struct {
		Type             string `json:"type"`
		IsError          bool   `json:"is_error"`
		Result           string `json:"result"`
		StructuredOutput *struct {
			Command string `json:"command"`
		} `json:"structured_output"`
	}
	if json.Unmarshal([]byte(res.Stdout), &env) == nil && env.Type == "result" {
		if env.IsError {
			if reason, ok := agentRateLimitReason(env.Result); ok {
				return "", RateLimited(id, 0, "%s", reason)
			}
			return "", Unavailable(id, "claude: %s", FirstLine(env.Result))
		}
		if env.StructuredOutput == nil || strings.TrimSpace(env.StructuredOutput.Command) == "" {
			return "", Unavailable(id, "claude returned no structured command")
		}
		return env.StructuredOutput.Command, nil
	}
	return res.Stdout, nil
}

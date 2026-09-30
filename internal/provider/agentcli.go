package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ericandrechek/shelp/internal/config"
)

// agentcli: providers that shell out to installed AI coding-agent CLIs
// (claude, gemini, codex, copilot, agy) in single-shot, non-interactive mode
// and return their raw text reply. These tools are full coding agents by
// default (they read project files, edit files, run shell commands); every
// spec below configures the closest thing each CLI has to "just answer in
// text", and Generate always runs in a fresh empty temp directory so none
// of them pick up this repo's own CLAUDE.md/AGENTS.md/GEMINI.md.
//
// Notes from tuning on this Mac, macOS 27.0, verified 2026-09-30 against
// claude 2.1.285, gemini 0.46.0, codex-cli 0.144.0, GitHub Copilot CLI
// 1.0.x, Antigravity CLI (agy) 1.2.14 (see MERGE NOTES in the PR/commit
// description for the full investigation):
//
//   - claude authenticates via claude.ai (a subscription, not an API key).
//     --bare cannot be used: it forces ANTHROPIC_API_KEY/apiKeyHelper only
//     ("OAuth and keychain are never read") and this account then reports
//     "Not logged in". We use --setting-sources "" (skip user/project/local
//     settings) + --strict-mcp-config (no MCP servers) + --tools "" (no
//     tools at all) + --permission-prompts none + --disable-slash-commands
//     instead, which is auth-neutral. --model accepts the "haiku"/"sonnet"
//     aliases directly.
//   - gemini refuses to run non-interactively in an untrusted folder (a
//     fresh temp dir always is) unless --skip-trust is passed. Separately,
//     on this machine gemini's cached OAuth token appears stale: `gemini -p`
//     tries to reopen a browser auth flow and hangs waiting on a prompt
//     that a non-TTY stdin never answers (confirmed even with --skip-trust
//     and piped input). gemini's own --help documents no model-name list,
//     so we ship gemini:default (no -m flag: whatever the CLI/account
//     resolves on its own) rather than inventing a model id.
//   - `codex exec` has no -a/--ask-for-approval flag (that only exists on
//     the top-level `codex` command); -s read-only plus the account's
//     approval default is enough to avoid edits/hangs. Plain
//     --ignore-user-config is rejected outright: it drops the user's
//     configured default model, and this ChatGPT-plan account 400s on the
//     CLI's own hardcoded fallback model ("gpt-5-codex") and on a second
//     guessed name ("gpt-5.1-codex-mini") alike ("model ... is not
//     supported when using Codex with a ChatGPT account"). Loading the
//     full config to avoid that measured 5.5s p50 (outliers ~9s) --
//     connecting to every configured MCP server on every single call, not
//     just the first cold one. Fix: codexArgs peeks ONLY the top-level
//     `model` (and `model_reasoning_effort`, if present) key out of
//     $CODEX_HOME/config.toml (see readCodexUserModel) and, when found,
//     passes --ignore-user-config together with an explicit -m/-c using
//     that same model -- so the model never actually changes, but the MCP
//     server connections are skipped. No model key -> today's original
//     behavior (full config load, no -m, no --ignore-user-config). See
//     CLAUDE.md for measured before/after latency. Output is read from
//     --output-last-message (-o), not stdout, since stdout carries a
//     banner, warnings and any MCP errors.
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
//   - Google deprecated the standalone Gemini CLI in favor of the
//     Antigravity CLI (`agy`); it was uninstalled from this Mac, so
//     geminiIdentify's failure Reason says so, but the gemini spec itself
//     is left as-is for anyone still running the old CLI. agy's --help
//     documents no system-prompt flag, so antigravityArgs prepends
//     system+"\n\n"+prompt exactly like gemini/copilot. `--mode plan`
//     makes agy describe/propose actions in text instead of running them
//     (verified live: asked to create a file, it wrote a plan description
//     and touched nothing); `--sandbox` layers on terminal restrictions.
//     `--disable-slash-commands` is deliberately NOT passed even though the
//     other specs favor "disable everything optional": agy prints "warning:
//     --mode plan has no effect while slash command expansion is disabled"
//     when both are given, which would fall back to headless mode's
//     default behavior -- any tool call silently auto-denied (exit 0,
//     empty stdout, an explanatory stderr line) since headless mode has no
//     controlling terminal to prompt on. Slash-command expansion only
//     fires on a message that itself starts with "/", and the combined
//     prompt always starts with req.System (shelp's own fixed template
//     text, never "/"), so leaving expansion enabled is safe regardless of
//     what the user's free-text req.Prompt contains. `agy models`'s list is
//     account/tier-dependent (free tier, observed 2026-09-30: three Gemini
//     3.x flash families x 3 effort levels, Gemini 3.1 Pro x 2 effort
//     levels, claude-sonnet-4-6, claude-opus-4-6-thinking,
//     gpt-oss-120b-medium) and can change, so antigravityIdentify (Detect)
//     refreshes a small on-disk cache instead of hardcoding the list; see
//     the "antigravity model cache" section below.
//
// Every call sets execx.go's RunCmdOpts.Dir to the scratch temp dir, for
// every CLI, whether or not it also has its own -C/--cd flag (codex and
// copilot do; claude and gemini don't). Earlier this os.Chdir'd the whole
// process for claude/gemini, guarded by a package mutex; that's gone now
// because it raced with anything else in the process relying on relative
// paths (e.g. shelp-bench, which runs providers concurrently and itself
// writes relative bench/runs/... paths) -- Dir sets only the subprocess's
// cwd, so concurrent Generate calls across providers/goroutines are safe.
//
// Every call is wrapped in its own bounded timeout (generateTimeout) on top
// of the caller's context: agent CLIs are full coding agents that can, in
// principle, decide to prompt for something we didn't anticipate (gemini's
// stale-token browser prompt above is a real example), and Generate must
// fail fast rather than hang the whole `shelp` process.

// generateTimeout bounds one agent-CLI call, on top of whatever deadline
// the caller's context already carries (context.WithTimeout only ever
// shortens). Cold agent-CLI starts measured on this Mac topped out around
// 9s (codex, loading configured MCP servers); 60s leaves headroom for a
// slow network without leaving a stuck OAuth prompt hanging indefinitely.
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
	name string // binary name on PATH, and (usually) the shelp ID prefix
	// idPrefix, when non-empty, is the shelp ID prefix used in place of
	// name ("<idPrefix>:<model>" rather than "<name>:<model>"). Every spec
	// but antigravity leaves this "" (name doubles as both, e.g. "claude");
	// antigravity's binary is "agy" but its IDs are "antigravity:<model>"
	// (task requirement -- the CLI itself is still invoked as "agy").
	idPrefix      string
	label         string // human label for Description()
	defaultModels []agentModel
	// defaultModelsFn, when non-nil, computes the default model list instead
	// of using the static defaultModels field above (antigravity: the ids
	// cached by the last successful `agy models`, falling back to two
	// hardcoded ids on a cold cache -- see antigravityDefaultModels). Must
	// stay cheap like the rest of Providers(): a small file read, never a
	// process execution.
	defaultModelsFn func() []agentModel
	identify        func(ctx context.Context) Status
	// buildArgs returns the argv (excluding the binary name itself). dir is
	// a fresh, empty, just-created temp directory; outFile is a path inside
	// dir that spec.needsOutFile specs must have the CLI write its final
	// reply to (codex's --output-last-message). model is the resolved CLI
	// flag value ("" means omit the model flag).
	buildArgs func(dir, model, outFile string, req Request) []string
	// needsOutFile is true when the reply must be read from a file instead
	// of stdout (codex, whose stdout carries banners/warnings/MCP errors).
	needsOutFile bool
	// stream is false when stdout is not safe to forward chunk-by-chunk as
	// the reply (codex: stdout is not the reply). true means Generate
	// forwards req.OnChunk straight to RunCmd.
	stream bool
	// parse extracts the reply from a finished call, or maps failure to an
	// *UnavailableError via Unavailable(id, ...). outFile is "" for specs
	// that don't set needsOutFile.
	parse func(id string, res CmdResult, outFile string) (string, error)
}

// resolveModels turns a config override (raw model-flag strings from
// [agentcli.<name>] models) into agentModels, or returns the built-in
// defaults when there is no override. A configured name that matches one
// of the built-in ids (e.g. "default", "haiku") reuses that variant's CLI
// flag value; any other name is passed through verbatim as both id and CLI
// flag value, so `models = ["gemini-2.5-flash"]` works once a user knows a
// real model name for their account.
func (s cliSpec) resolveModels(names []string) []agentModel {
	defaults := s.defaultModels
	if s.defaultModelsFn != nil {
		defaults = s.defaultModelsFn()
	}
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

var agentCLISpecs = []cliSpec{claudeSpec, geminiSpec, codexSpec, copilotSpec, antigravitySpec}

// agentcliCLIConfig is one CLI's [agentcli.<name>] table.
type agentcliCLIConfig struct {
	Models   []string `toml:"models"`
	Disabled bool     `toml:"disabled"`
}

// agentcliConfig is the [agentcli] section: one sub-table per CLI. The
// antigravity table is keyed "antigravity" (matching its shelp ID prefix,
// antigravity:<model>), even though the binary invoked is "agy" -- forName
// below still switches on the binary name ("agy"), like every other CLI.
type agentcliConfig struct {
	Claude      agentcliCLIConfig `toml:"claude"`
	Gemini      agentcliCLIConfig `toml:"gemini"`
	Codex       agentcliCLIConfig `toml:"codex"`
	Copilot     agentcliCLIConfig `toml:"copilot"`
	Antigravity agentcliCLIConfig `toml:"antigravity"`
}

func (c agentcliConfig) forName(name string) agentcliCLIConfig {
	switch name {
	case "claude":
		return c.Claude
	case "gemini":
		return c.Gemini
	case "codex":
		return c.Codex
	case "copilot":
		return c.Copilot
	case "agy":
		return c.Antigravity
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
		prefix := spec.name
		if spec.idPrefix != "" {
			prefix = spec.idPrefix
		}
		for _, m := range spec.resolveModels(sub.Models) {
			out = append(out, &agentcliProvider{
				spec:     spec,
				cliModel: m.cli,
				id:       prefix + ":" + m.id,
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
	// a few ("Gemini CLI", "Codex CLI", "GitHub Copilot CLI", "Antigravity
	// CLI") already do, and appending unconditionally used to read "Codex
	// CLI CLI" / "GitHub Copilot CLI CLI".
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

	var outFile string
	if p.spec.needsOutFile {
		outFile = filepath.Join(dir, "reply.txt")
	}
	args := p.spec.buildArgs(dir, p.cliModel, outFile, req)

	runCtx, cancel := context.WithTimeout(ctx, generateTimeout)
	defer cancel()

	var onChunk func(string)
	if p.spec.stream {
		onChunk = req.OnChunk
	}

	res, err := RunCmdOpts(runCtx, CmdOpts{Dir: dir, OnChunk: onChunk}, p.spec.name, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", Unavailable(p.id, "%s timed out (shelp caps each agent-CLI call at %s; a stale login can make some agent CLIs hang on an interactive re-auth/trust prompt in headless mode -- try running `%s` interactively once)", p.spec.name, generateTimeout, p.spec.name)
		}
		return "", Unavailable(p.id, "%s: %v", p.spec.name, err)
	}
	return p.spec.parse(p.id, res, outFile)
}

// unavailableMsg picks the short reason to report: stderr's first line,
// falling back to stdout's, both ANSI-stripped.
func unavailableMsg(res CmdResult) string {
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
	stream:    true, // --output-format text is exactly the reply
	identify:  claudeIdentify,
	buildArgs: claudeArgs,
	parse:     claudeParse,
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

func claudeArgs(dir, model, outFile string, req Request) []string {
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

func claudeParse(id string, res CmdResult, outFile string) (string, error) {
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

// ==================== gemini ====================

var geminiSpec = cliSpec{
	name:  "gemini",
	label: "Gemini CLI",
	defaultModels: []agentModel{
		{id: "default", cli: ""}, // --help lists no model names; don't invent one
	},
	stream:    true, // --output-format text is exactly the reply
	identify:  geminiIdentify,
	buildArgs: geminiArgs,
	parse:     geminiParse,
}

// geminiDeprecationNote is appended to geminiIdentify's failure Reason:
// Google deprecated the standalone Gemini CLI in favor of the Antigravity
// CLI (`agy`, the antigravity:* providers in this same file) and login now
// points users there; this Mac had gemini uninstalled entirely (Providers()
// filters it out via LookPath before Detect ever runs), but the spec is
// kept as-is for anyone still running the old CLI.
const geminiDeprecationNote = " (Google deprecated the Gemini CLI in favor of the Antigravity CLI, `agy`; see antigravity:* providers)"

func geminiIdentify(ctx context.Context) Status {
	h, err := RunCmd(ctx, nil, nil, "gemini", "--help")
	if err != nil {
		return Status{Reason: err.Error() + geminiDeprecationNote}
	}
	if h.ExitCode != 0 || !strings.Contains(h.Stdout, "Gemini CLI") {
		return Status{Reason: "not Gemini CLI" + geminiDeprecationNote}
	}
	v, err := RunCmd(ctx, nil, nil, "gemini", "--version")
	ver := ""
	if err == nil && v.ExitCode == 0 {
		ver = FirstLine(v.Stdout)
	}
	return Status{Available: true, Version: ver}
}

func geminiArgs(dir, model, outFile string, req Request) []string {
	args := []string{
		"--skip-trust", // a fresh temp dir is always untrusted
		"--approval-mode", "plan",
		"--output-format", "text",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--prompt", req.System+"\n\n"+req.Prompt)
	return args
}

func geminiParse(id string, res CmdResult, outFile string) (string, error) {
	if res.ExitCode != 0 {
		msg := unavailableMsg(res)
		if reason, ok := agentRateLimitReason(msg); ok {
			return "", RateLimited(id, 0, "%s", reason)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "auth") || strings.Contains(low, "login") || strings.Contains(low, "cancelled") || strings.Contains(low, "trusted") {
			return "", Unavailable(id, "gemini needs (re)authentication or workspace trust: run `gemini` once interactively (%s)", msg)
		}
		return "", Unavailable(id, "gemini exited %d: %s", res.ExitCode, msg)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return "", Unavailable(id, "gemini returned no output")
	}
	return res.Stdout, nil
}

// ==================== codex ====================

var codexSpec = cliSpec{
	name:  "codex",
	label: "Codex CLI",
	defaultModels: []agentModel{
		{id: "default", cli: ""}, // ChatGPT-plan accounts reject guessed model ids; use the account default
	},
	needsOutFile: true, // stdout carries banners/warnings/MCP errors, not the reply
	stream:       false,
	identify:     codexIdentify,
	buildArgs:    codexArgs,
	parse:        codexParse,
}

func codexIdentify(ctx context.Context) Status {
	v, err := RunCmd(ctx, nil, nil, "codex", "--version")
	if err != nil {
		return Status{Reason: err.Error()}
	}
	line := FirstLine(v.Stdout)
	if v.ExitCode != 0 || !strings.Contains(line, "codex") {
		return Status{Reason: "not Codex CLI: " + line}
	}
	st, err := RunCmd(ctx, nil, nil, "codex", "login", "status")
	if err == nil && st.ExitCode == 0 {
		s := FirstLine(stripANSI(st.Stdout + st.Stderr))
		if strings.Contains(strings.ToLower(s), "not logged in") {
			return Status{Reason: "codex not logged in: run `codex login`"}
		}
		if s != "" {
			line += " (" + s + ")"
		}
	}
	return Status{Available: true, Version: line}
}

func codexArgs(dir, model, outFile string, req Request) []string {
	args := []string{
		"exec",
		"-s", "read-only",
		"-C", dir,
		"--skip-git-repo-check", // dir is a fresh temp dir, not a git repo
		"--ephemeral",           // no session persistence
		"--ignore-rules",
		"--color", "never",
		"-o", outFile,
	}
	if model != "" {
		// A shelp-config override (e.g. `[agentcli.codex] models = [...]`)
		// already names a real model, so today's plain -m behavior is
		// unaffected by the codex:default speed fix below.
		args = append(args, "-m", model)
	} else if u := readCodexUserModel(); u.Model != "" {
		// codex:default: skip loading the account's whole config.toml
		// (which otherwise connects to every configured MCP server on
		// every call) by passing --ignore-user-config, but keep using the
		// SAME model the account has configured -- codex's own hardcoded
		// fallback model is rejected outright on a ChatGPT-plan account
		// ("model ... is not supported when using Codex with a ChatGPT
		// account"), so --ignore-user-config alone is not safe; -m with
		// the account's real model is what makes it safe. See
		// readCodexUserModel for what is and isn't read from that file.
		args = append(args, "--ignore-user-config", "-m", u.Model)
		if u.ModelReasoningEffort != "" {
			args = append(args, "-c", "model_reasoning_effort="+u.ModelReasoningEffort)
		}
	}
	args = append(args, req.System+"\n\n"+req.Prompt)
	return args
}

// codexUserModel is the sliver of $CODEX_HOME/config.toml (or
// ~/.codex/config.toml, codex's own fallback when $CODEX_HOME is unset)
// that codexArgs needs. config.toml can hold API keys, MCP server
// commands/env, and other secrets; decoding into a struct that names only
// these two keys means BurntSushi/toml simply skips every other key while
// parsing, so nothing else the file contains is ever read into memory
// under a name this package can reach, let alone logged, printed, or
// passed to codex on the command line.
type codexUserModel struct {
	Model                string `toml:"model"`
	ModelReasoningEffort string `toml:"model_reasoning_effort"`
}

// readCodexUserModel best-effort reads codexUserModel from the account's
// own codex config. Any failure (no $CODEX_HOME and no home dir, missing
// file, unparseable TOML, no top-level model key) yields the zero value,
// which callers treat exactly like "nothing configured": codexArgs falls
// back to today's behavior (no -m, no --ignore-user-config, codex loads
// its full config as before).
func readCodexUserModel() codexUserModel {
	path := codexConfigPath()
	if path == "" {
		return codexUserModel{}
	}
	var cfg codexUserModel
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return codexUserModel{}
	}
	return cfg
}

// codexConfigPath mirrors codex's own config-file resolution: $CODEX_HOME/
// config.toml, or ~/.codex/config.toml when $CODEX_HOME is unset. Returns
// "" only when neither is resolvable (no $CODEX_HOME and no home dir).
func codexConfigPath() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func codexParse(id string, res CmdResult, outFile string) (string, error) {
	if res.ExitCode != 0 {
		msg := unavailableMsg(res)
		if reason, ok := agentRateLimitReason(msg); ok {
			return "", RateLimited(id, 0, "%s", reason)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "not logged in") || strings.Contains(low, "log in") {
			return "", Unavailable(id, "codex not logged in: run `codex login`")
		}
		if strings.Contains(strings.ToLower(res.Stdout), "not supported when using codex") {
			return "", Unavailable(id, "codex: configured model not available for this account")
		}
		return "", Unavailable(id, "codex exited %d: %s", res.ExitCode, msg)
	}
	b, err := os.ReadFile(outFile)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		if strings.Contains(strings.ToLower(res.Stdout), "not supported when using codex") {
			return "", Unavailable(id, "codex: configured model not available for this account")
		}
		return "", Unavailable(id, "codex produced no output")
	}
	return string(b), nil
}

// ==================== copilot ====================

var copilotSpec = cliSpec{
	name:  "copilot",
	label: "GitHub Copilot CLI",
	defaultModels: []agentModel{
		{id: "default", cli: "auto"}, // "--model auto": "let Copilot pick automatically" (documented)
	},
	stream:    true, // -s (silent) output is exactly the reply
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

func copilotArgs(dir, model, outFile string, req Request) []string {
	args := []string{
		"-p", req.System + "\n\n" + req.Prompt,
		"-s", // silent: only the agent's response, no stats/banner
		"-C", dir,
		"--no-color",
		"--available-tools", // no value => zero tools available to the model
		"--no-custom-instructions",
		// NOTE: --no-auto-update deliberately omitted here: combined with
		// -p and -C it breaks copilot's own arg parser (see file header).
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func copilotParse(id string, res CmdResult, outFile string) (string, error) {
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

// ==================== antigravity (agy) ====================
//
// Google deprecated the standalone Gemini CLI in favor of the Antigravity
// CLI (Homebrew's gemini-cli formula is marked "unsupported" and its own
// login flow now points users at Antigravity); `agy` is its replacement.
// Verified live against agy 1.2.14, 2026-09-30, on a free-tier account.

var antigravitySpec = cliSpec{
	name:            "agy", // the binary invoked; IDs use idPrefix below instead
	idPrefix:        "antigravity",
	label:           "Antigravity CLI",
	defaultModelsFn: antigravityDefaultModels,
	stream:          true, // --output-format text is exactly the reply
	identify:        antigravityIdentify,
	buildArgs:       antigravityArgs,
	parse:           antigravityParse,
}

// antigravityDefaultModels resolves antigravity:*'s built-in candidate list
// when no [agentcli.antigravity] models override is configured: the ids
// cached by the last successful `agy models` (see antigravityIdentify),
// falling back to two conservative built-in ids on a completely cold
// cache. Only a small file read, never a process execution, per
// Providers()'s cheapness contract.
func antigravityDefaultModels() []agentModel {
	ids := readAntigravityModelCache()
	if len(ids) == 0 {
		ids = []string{"gemini-3.8-flash-low", "gemini-3.1-pro-low"}
	}
	out := make([]agentModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, agentModel{id: id, cli: id})
	}
	return out
}

// looksLikeVersion reports whether s starts with a bare dotted version
// number ("1.2.14"). agy --version prints ONLY the version, no CLI name
// anywhere in it (unlike claude/codex/copilot's "<name> <version>"-shaped
// output), so identity is confirmed by combining this with the --help
// check in antigravityIdentify below (which does mention "agy") rather
// than matching a name inside --version's own output.
func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}
	major, rest, ok := strings.Cut(s, ".")
	if !ok || major == "" || rest == "" {
		return false
	}
	for _, c := range major {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func antigravityIdentify(ctx context.Context) Status {
	v, err := RunCmd(ctx, nil, nil, "agy", "--version")
	if err != nil {
		return Status{Reason: err.Error()}
	}
	line := FirstLine(v.Stdout)
	if v.ExitCode != 0 || !looksLikeVersion(line) {
		return Status{Reason: "not the Antigravity CLI: " + line}
	}
	// agy --help writes its usage text to STDERR, not stdout (verified
	// live 2026-09-30: "Usage of agy: ..." on fd 2, empty stdout, exit 0)
	// -- unlike claude/gemini/codex/copilot, whose --help this file already
	// reads from Stdout. Check both so a future fix on Google's side
	// (moving it to stdout) still works.
	h, err := RunCmd(ctx, nil, nil, "agy", "--help")
	if err != nil || h.ExitCode != 0 || !strings.Contains(h.Stdout+h.Stderr, "agy") {
		return Status{Reason: "not the Antigravity CLI: --help did not mention agy"}
	}
	// Best-effort: refresh the model cache so Providers() (called again by
	// a second `shelp providers`) can offer every model the account
	// actually has, not just the two hardcoded fallback ids. A failure here
	// never fails Detect itself -- the cache (or its absence) is exactly as
	// good as it was before this call.
	if ids, mErr := listAntigravityModels(ctx); mErr == nil && len(ids) > 0 {
		_ = writeAntigravityModelCache(ids)
	}
	return Status{Available: true, Version: "Antigravity CLI " + line}
}

func antigravityArgs(dir, model, outFile string, req Request) []string {
	// agy has no system-prompt flag (checked `agy --help` / `agy help
	// <subcommand>` for every subcommand listed, 2026-09-30): prepend
	// system+"\n\n"+prompt exactly like gemini/copilot.
	//
	// --mode plan makes agy describe/propose actions in text instead of
	// running them -- verified live: asked to create a file, it wrote a
	// plan description ("I have created the implementation plan: ...
	// Please review... click Proceed") and created nothing. --sandbox
	// layers on terminal restrictions on top.
	//
	// --disable-slash-commands is deliberately NOT passed, even though
	// every other spec in this file favors "disable everything optional":
	// agy prints "warning: --mode plan has no effect while slash command
	// expansion is disabled" when both are given together, and headless
	// mode's default behavior (no --mode plan in effect) is to silently
	// auto-deny any tool call that would need a permission prompt --
	// exit 0, EMPTY stdout, only an explanatory stderr line (see
	// antigravityParse) -- for any prompt the model decides to act on
	// rather than just answer. Slash-command expansion only fires on a
	// message that itself starts with "/", and the combined prompt here
	// always starts with req.System (shelp's own fixed template text,
	// never "/"), so leaving expansion enabled is safe regardless of what
	// the user's free-text req.Prompt contains.
	//
	// --dangerously-skip-permissions is never used (would auto-approve
	// every tool, including file writes and shell commands).
	args := []string{
		"-p", req.System + "\n\n" + req.Prompt,
		"--output-format", "text",
		"--sandbox",
		"--mode", "plan",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func antigravityParse(id string, res CmdResult, outFile string) (string, error) {
	if res.ExitCode != 0 {
		msg := unavailableMsg(res)
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "not logged in"):
			// Inferred from the CLI binary's own embedded strings ("Print
			// mode: not logged in and no controlling terminal; cannot
			// complete interactive login") -- not observed live, since
			// forcing this account to log out would have disrupted the
			// user's active agy session.
			return "", Unavailable(id, "antigravity not logged in: run `agy` once interactively to sign in")
		case strings.Contains(low, "overloaded") || strings.Contains(low, "high traffic") || strings.Contains(low, "quota") || strings.Contains(low, "rate limit"):
			// Also inferred from embedded strings ("The model API is
			// currently overloaded and may experience intermittent
			// errors.", "Our servers are experiencing high traffic right
			// now, please try again in a minute.") rather than observed
			// live, to avoid deliberately exhausting this free-tier
			// account's daily quota.
			return "", Unavailable(id, "antigravity rate limited or overloaded: %s", msg)
		default:
			return "", Unavailable(id, "agy exited %d: %s", res.ExitCode, msg)
		}
	}
	if strings.TrimSpace(res.Stdout) == "" {
		if msg := unavailableMsg(res); msg != "" {
			// Headless mode auto-denies any tool call needing a permission
			// prompt it cannot show: exit 0, empty stdout, an explanatory
			// stderr line. Verified live 2026-09-30 (asked to create a
			// file, with --mode plan omitted): "jetski: no output produced
			// -- a tool required the \"write_file\" permission that
			// headless mode cannot prompt for, so it was auto-denied. Add
			// an allow-rule under permissions.allow in settings.json (e.g.
			// write_file(<target>)). Alternatively, re-run with
			// --dangerously-skip-permissions to auto-approve all tools."
			return "", Unavailable(id, "agy returned no output: %s", msg)
		}
		return "", Unavailable(id, "agy returned no output")
	}
	return res.Stdout, nil
}

// --- antigravity model cache ---------------------------------------------
//
// $XDG_CACHE_HOME/shelp/antigravity-models.json (default ~/.cache/shelp/),
// written by antigravityIdentify (Detect) after a successful `agy models`;
// read by antigravityDefaultModels (Providers(), via cliSpec.
// defaultModelsFn) so listing providers stays config-plus-a-small-file-read
// with no process execution of its own -- mirrors the openai family's
// local-models.json (see readLocalModelCache/writeLocalModelCache above).
// The account's actual model list is free-tier/account-dependent and can
// change over time, hence caching it instead of hardcoding it; only the
// two-model fallback in antigravityDefaultModels is hardcoded, for a
// completely cold cache (first run on a machine, or a failed `agy models`).

type antigravityModelCache struct {
	Models  []string  `json:"models"`
	Updated time.Time `json:"updated"`
}

func antigravityCachePath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "shelp", "antigravity-models.json")
}

func readAntigravityModelCache() []string {
	b, err := os.ReadFile(antigravityCachePath())
	if err != nil {
		return nil
	}
	var c antigravityModelCache
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	return c.Models
}

func writeAntigravityModelCache(models []string) error {
	path := antigravityCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(antigravityModelCache{Models: models, Updated: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// listAntigravityModels runs `agy models` in a fresh, empty, just-created
// temp directory (never this repo's own, matching every other call in this
// file) and parses its tab-separated "<id>\t<label>" lines (e.g.
// "gemini-3.8-flash-low\tGemini 3.8 Flash (Low)", verified live
// 2026-09-30; the "Fetching available models..." progress line goes to
// stderr, not stdout) into bare model ids.
func listAntigravityModels(ctx context.Context) ([]string, error) {
	dir, err := os.MkdirTemp("", "shelp-agentcli-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	res, err := RunCmdOpts(ctx, CmdOpts{Dir: dir}, "agy", "models")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("agy models exited %d", res.ExitCode)
	}
	return parseAntigravityModels(res.Stdout), nil
}

func parseAntigravityModels(stdout string) []string {
	var ids []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, _, _ := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

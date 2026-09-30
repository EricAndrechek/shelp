package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// agentcli: providers that shell out to installed AI coding-agent CLIs
// (claude, gemini, codex, copilot) in single-shot, non-interactive mode and
// return their raw text reply. These tools are full coding agents by
// default (they read project files, edit files, run shell commands); every
// spec below configures the closest thing each CLI has to "just answer in
// text", and Generate always runs in a fresh empty temp directory so none
// of them pick up this repo's own CLAUDE.md/AGENTS.md/GEMINI.md.
//
// Notes from tuning on this Mac, macOS 27.0, verified 2026-09-30 against
// claude 2.1.285, gemini 0.46.0, codex-cli 0.144.0, GitHub Copilot CLI
// 1.0.x (see MERGE NOTES in the PR/commit description for the full
// investigation):
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
//     approval default is enough to avoid edits/hangs. --ignore-user-config
//     was tried and rejected: it drops the user's configured default model,
//     and this ChatGPT-plan account 400s on the CLI's own hardcoded
//     fallback model ("gpt-5-codex") and on a second guessed name
//     ("gpt-5.1-codex-mini") alike ("model ... is not supported when using
//     Codex with a ChatGPT account") -- so codex:default deliberately keeps
//     the user's config (and eats the resulting MCP-server-connect
//     latency/errors from servers configured there; they fail fast and are
//     harmless, just noise on stderr). Output is read from
//     --output-last-message, not stdout, since stdout carries a banner,
//     warnings and any MCP errors.
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
	name          string // binary name on PATH, and the shelp family prefix
	label         string // human label for Description()
	defaultModels []agentModel
	identify      func(ctx context.Context) Status
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
	if len(names) == 0 {
		return s.defaultModels
	}
	byID := make(map[string]agentModel, len(s.defaultModels))
	for _, m := range s.defaultModels {
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

var agentCLISpecs = []cliSpec{claudeSpec, geminiSpec, codexSpec, copilotSpec}

// agentcliCLIConfig is one CLI's [agentcli.<name>] table.
type agentcliCLIConfig struct {
	Models   []string `toml:"models"`
	Disabled bool     `toml:"disabled"`
}

// agentcliConfig is the [agentcli] section: one sub-table per CLI.
type agentcliConfig struct {
	Claude  agentcliCLIConfig `toml:"claude"`
	Gemini  agentcliCLIConfig `toml:"gemini"`
	Codex   agentcliCLIConfig `toml:"codex"`
	Copilot agentcliCLIConfig `toml:"copilot"`
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
	return p.spec.label + " CLI, model " + m + " (headless, no tools)"
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

func geminiIdentify(ctx context.Context) Status {
	h, err := RunCmd(ctx, nil, nil, "gemini", "--help")
	if err != nil {
		return Status{Reason: err.Error()}
	}
	if h.ExitCode != 0 || !strings.Contains(h.Stdout, "Gemini CLI") {
		return Status{Reason: "not Gemini CLI"}
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
		args = append(args, "-m", model)
	}
	args = append(args, req.System+"\n\n"+req.Prompt)
	return args
}

func codexParse(id string, res CmdResult, outFile string) (string, error) {
	if res.ExitCode != 0 {
		msg := unavailableMsg(res)
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

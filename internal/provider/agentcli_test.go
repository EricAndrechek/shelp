package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// fakePATH puts testdata/agentcli (the fake claude script) first on PATH,
// ahead of anything real that happens to be installed on the machine
// running the tests.
func fakePATH(t *testing.T) {
	t.Helper()
	dir, err := filepath.Abs("testdata/agentcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// emptyPATH points PATH at an empty directory, so none of the fakes (and
// hopefully nothing real either) resolve.
func emptyPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

type fakeRecord struct {
	cwd        string
	stdinBytes int
	// cDirExists is "yes"/"no" when the argv had a "-C <dir>" pair and the
	// fake CLI could stat it at call time, "n/a" when there was no -C.
	cDirExists string
	// envMaxThinking is the subprocess's own $MAX_THINKING_TOKENS value, or
	// "<unset>" when it wasn't set -- used to verify claudeSpec.extraEnv
	// actually reaches the subprocess environment.
	envMaxThinking string
	args           []string
}

// readRecord parses the record file written by testdata/agentcli/*. The
// header (CWD/STDINBYTES/CDIREXISTS/ENVMAXTHINKING) is one line each; args
// follow an "ARGS:\n" marker, NUL-separated, since an arg value
// (system+"\n\n"+prompt) can itself contain literal newlines and so can't
// be newline-delimited.
func readRecord(t *testing.T, path string) fakeRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record %s: %v", path, err)
	}
	marker := []byte("ARGS:\n")
	idx := strings.Index(string(b), string(marker))
	if idx < 0 {
		t.Fatalf("record %s missing ARGS: marker: %q", path, b)
	}
	header, argBlob := string(b[:idx]), string(b[idx+len(marker):])

	var rec fakeRecord
	for _, line := range strings.Split(header, "\n") {
		switch {
		case strings.HasPrefix(line, "CWD:"):
			rec.cwd = strings.TrimPrefix(line, "CWD:")
		case strings.HasPrefix(line, "STDINBYTES:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(line, "STDINBYTES:"))
			rec.stdinBytes = n
		case strings.HasPrefix(line, "CDIREXISTS:"):
			rec.cDirExists = strings.TrimPrefix(line, "CDIREXISTS:")
		case strings.HasPrefix(line, "ENVMAXTHINKING:"):
			rec.envMaxThinking = strings.TrimPrefix(line, "ENVMAXTHINKING:")
		}
	}
	// Each arg (including legitimately empty ones, e.g. claude's
	// `--tools ""`) is followed by exactly one NUL, so splitting always
	// yields one trailing "" that must be dropped -- and only that one.
	parts := strings.Split(argBlob, "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	rec.args = parts
	return rec
}

// argAfter returns the value following flag in args, or "" if not found.
func argAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func agentTestReq() Request {
	return Request{
		Mode:   Suggest,
		System: "Output ONLY the command.",
		Prompt: "Request: list 5 recent git commits",
	}
}

// ---------------- Providers() / config wiring ----------------

func TestProviders_NoneOnPath(t *testing.T) {
	emptyPATH(t)
	cfg, err := config.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	ps := agentCLIFamily{}.Providers(cfg)
	if len(ps) != 0 {
		t.Fatalf("Providers() with empty PATH = %d providers, want 0", len(ps))
	}
}

func TestProviders_Defaults(t *testing.T) {
	fakePATH(t)
	cfg, err := config.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	ps := agentCLIFamily{}.Providers(cfg)
	got := map[string]bool{}
	for _, p := range ps {
		got[p.ID()] = true
		if p.Kind() != AgentCLI {
			t.Errorf("%s: Kind() = %q, want %q", p.ID(), p.Kind(), AgentCLI)
		}
	}
	want := []string{"claude:haiku", "claude:sonnet"}
	for _, id := range want {
		if !got[id] {
			t.Errorf("Providers() missing %q; got %v", id, got)
		}
	}
	if len(ps) != len(want) {
		t.Errorf("Providers() = %d providers, want %d (%v)", len(ps), len(want), got)
	}
}

func TestProviders_Disabled(t *testing.T) {
	fakePATH(t)
	cfg, err := config.Parse("[agentcli.claude]\ndisabled = true\n")
	if err != nil {
		t.Fatal(err)
	}
	ps := agentCLIFamily{}.Providers(cfg)
	for _, p := range ps {
		if strings.HasPrefix(p.ID(), "claude:") {
			t.Errorf("Providers() with [agentcli.claude] disabled still returned %s", p.ID())
		}
	}
	if len(ps) != 0 {
		t.Errorf("Providers() with claude (the only agentcli spec) disabled = %v, want none", ps)
	}
}

func TestProviders_ModelsOverride(t *testing.T) {
	fakePATH(t)
	cfg, err := config.Parse("[agentcli.claude]\nmodels = [\"haiku\", \"opus\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	ps := agentCLIFamily{}.Providers(cfg)
	var claudeIDs []string
	for _, p := range ps {
		if strings.HasPrefix(p.ID(), "claude:") {
			claudeIDs = append(claudeIDs, p.ID())
		}
	}
	want := []string{"claude:haiku", "claude:opus"}
	if len(claudeIDs) != len(want) {
		t.Fatalf("claude providers = %v, want %v", claudeIDs, want)
	}
	for i, id := range want {
		if claudeIDs[i] != id {
			t.Errorf("claude providers[%d] = %q, want %q", i, claudeIDs[i], id)
		}
	}
	// "opus" isn't a built-in default id, so it should pass through as a
	// literal --model value.
	ap, ok := findProvider(ps, "claude:opus").(*agentcliProvider)
	if !ok {
		t.Fatal("claude:opus not found or wrong type")
	}
	if ap.cliModel != "opus" {
		t.Errorf("claude:opus cliModel = %q, want %q", ap.cliModel, "opus")
	}
}

func findProvider(ps []Provider, id string) Provider {
	for _, p := range ps {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

func TestResolveModels(t *testing.T) {
	spec := claudeSpec
	def := spec.resolveModels(nil)
	if len(def) != 2 || def[0].id != "haiku" || def[1].id != "sonnet" {
		t.Fatalf("resolveModels(nil) = %+v, want built-in defaults", def)
	}
	got := spec.resolveModels([]string{"sonnet", "opus"})
	want := []agentModel{{id: "sonnet", cli: "sonnet"}, {id: "opus", cli: "opus"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("resolveModels override = %+v, want %+v", got, want)
	}
}

// ---------------- Generate(): argv shape per CLI ----------------

func newProvider(spec cliSpec, cliModel string) *agentcliProvider {
	return &agentcliProvider{spec: spec, cliModel: cliModel, id: spec.name + ":test"}
}

func TestGenerate_Claude_ArgvAndCwd(t *testing.T) {
	fakePATH(t)
	rec := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv("SHELP_FAKE_RECORD", rec)
	t.Setenv("SHELP_FAKE_STDOUT", "git log -5 --oneline")

	p := newProvider(claudeSpec, "haiku")
	req := agentTestReq()
	startCwd, _ := os.Getwd()

	got, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "git log -5 --oneline" {
		t.Errorf("Generate() = %q", got)
	}

	r := readRecord(t, rec)
	if r.stdinBytes != 0 {
		t.Errorf("stdin bytes = %d, want 0 (nil/dev-null stdin)", r.stdinBytes)
	}
	if r.cwd == startCwd {
		t.Errorf("cwd was not changed to a scratch dir: still %s", r.cwd)
	}
	if !strings.Contains(r.cwd, os.TempDir()) && !strings.HasPrefix(r.cwd, "/tmp") && !strings.HasPrefix(r.cwd, "/private/tmp") && !strings.HasPrefix(r.cwd, "/var") {
		t.Errorf("cwd %q doesn't look like a temp dir", r.cwd)
	}
	if r.envMaxThinking != "0" {
		t.Errorf("MAX_THINKING_TOKENS in subprocess env = %q, want \"0\" (claudeSpec.extraEnv)", r.envMaxThinking)
	}
	if v, ok := argAfter(r.args, "--model"); !ok || v != "haiku" {
		t.Errorf("--model = %q, %v; want haiku", v, ok)
	}
	if v, ok := argAfter(r.args, "--system-prompt"); !ok || v != req.System {
		t.Errorf("--system-prompt = %q, %v; want %q", v, ok, req.System)
	}
	if !contains(r.args, "-p") {
		t.Error("missing -p (print/non-interactive)")
	}
	if v, ok := argAfter(r.args, "--output-format"); !ok || v != "json" {
		t.Errorf("suggest --output-format = %q, %v; want json", v, ok)
	}
	if v, ok := argAfter(r.args, "--json-schema"); !ok || v != claudeCommandSchema {
		t.Errorf("suggest --json-schema = %q, %v; want the command schema", v, ok)
	}
	if v, ok := argAfter(r.args, "--tools"); !ok || v != "" {
		t.Errorf("--tools = %q, %v; want empty string (no tools)", v, ok)
	}
	if v, ok := argAfter(r.args, "--setting-sources"); !ok || v != "" {
		t.Errorf("--setting-sources = %q, %v; want empty", v, ok)
	}
	if !contains(r.args, "--strict-mcp-config") {
		t.Error("missing --strict-mcp-config")
	}
	if !contains(r.args, "--no-session-persistence") {
		t.Error("missing --no-session-persistence")
	}
	if v, ok := argAfter(r.args, "--permission-prompts"); !ok || v != "none" {
		t.Errorf("--permission-prompts = %q, %v; want none", v, ok)
	}
	// last two args must be "--" then the raw prompt, unmodified.
	if len(r.args) < 2 || r.args[len(r.args)-2] != "--" || r.args[len(r.args)-1] != req.Prompt {
		t.Errorf("args should end with [-- %q], got tail %v", req.Prompt, r.args[max(0, len(r.args)-2):])
	}
}

func TestGenerate_Claude_DefaultOmitsModelFlag(t *testing.T) {
	// claude always ships explicit haiku/sonnet ids, but resolveModels lets
	// config request "default" (cli:"") too; verify the flag is omitted.
	fakePATH(t)
	rec := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv("SHELP_FAKE_RECORD", rec)
	t.Setenv("SHELP_FAKE_STDOUT", "ok")

	p := newProvider(claudeSpec, "")
	if _, err := p.Generate(context.Background(), agentTestReq()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	r := readRecord(t, rec)
	if contains(r.args, "--model") {
		t.Errorf("args should omit --model when cliModel is empty, got %v", r.args)
	}
}

// TestGenerate_ConcurrentClaudeDistinctCwd is the regression test for the
// os.Chdir + chdirMu removal: several Generate calls run concurrently (as
// shelp-bench does across providers/samples, in one process that itself
// relies on relative paths) must each see their OWN scratch temp dir as
// the subprocess's working directory, never another call's and never this
// process's own cwd -- and this process's own cwd must never move at all.
// Two concurrent haiku/sonnet calls stand in for what used to be two
// different CLIs (claude + copilot) before copilot was dropped; the
// property under test -- Dir isolation across concurrent Generate calls,
// not "two different binaries" specifically -- is unaffected by that.
// Run with `go test -race` to also catch any reintroduced shared mutable
// state (a chdir, a shared record file, a racy env mutation).
func TestGenerate_ConcurrentClaudeDistinctCwd(t *testing.T) {
	fakePATH(t)
	// A single static flag read (never written) by both subprocesses; safe
	// under -race because it's set once, before either goroutine starts,
	// and never mutated again for the rest of the test.
	t.Setenv("SHELP_FAKE_ECHO_CWD", "1")

	startCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	const n = 8 // a few rounds each, to make a real race more likely to show up
	var wg sync.WaitGroup
	haikuCwds := make([]string, n)
	sonnetCwds := make([]string, n)
	haikuErrs := make([]error, n)
	sonnetErrs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			p := newProvider(claudeSpec, "haiku")
			haikuCwds[i], haikuErrs[i] = p.Generate(context.Background(), agentTestReq())
		}(i)
		go func(i int) {
			defer wg.Done()
			p := newProvider(claudeSpec, "sonnet")
			sonnetCwds[i], sonnetErrs[i] = p.Generate(context.Background(), agentTestReq())
		}(i)
	}
	wg.Wait()

	if procCwd, _ := os.Getwd(); procCwd != startCwd {
		t.Errorf("this process's cwd changed to %s, want unchanged %s", procCwd, startCwd)
	}

	seen := map[string]int{} // cwd -> how many calls reported it
	for i := 0; i < n; i++ {
		if haikuErrs[i] != nil {
			t.Fatalf("haiku Generate[%d]: %v", i, haikuErrs[i])
		}
		if sonnetErrs[i] != nil {
			t.Fatalf("sonnet Generate[%d]: %v", i, sonnetErrs[i])
		}
		if haikuCwds[i] == "" || sonnetCwds[i] == "" {
			t.Fatalf("empty cwd reported: haiku=%q sonnet=%q", haikuCwds[i], sonnetCwds[i])
		}
		if haikuCwds[i] == startCwd || sonnetCwds[i] == startCwd {
			t.Fatalf("a call saw this process's own cwd instead of a scratch dir: haiku=%q sonnet=%q startCwd=%q", haikuCwds[i], sonnetCwds[i], startCwd)
		}
		if haikuCwds[i] == sonnetCwds[i] {
			t.Fatalf("round %d: the two concurrent calls saw the SAME cwd %q, want distinct scratch dirs", i, haikuCwds[i])
		}
		seen[haikuCwds[i]]++
		seen[sonnetCwds[i]]++
	}
	for dir, count := range seen {
		if count != 1 {
			t.Errorf("cwd %q was reported by %d calls, want exactly 1 (each Generate call gets its own scratch dir)", dir, count)
		}
	}
}

func TestGenerate_Claude_Streams(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_STDOUT", "the reply")

	// Only explain mode streams (plain text); suggest mode returns a JSON
	// envelope that must be parsed whole.
	var chunks []string
	req := agentTestReq()
	req.Mode = Explain
	req.OnChunk = func(s string) { chunks = append(chunks, s) }

	p := newProvider(claudeSpec, "haiku")
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("OnChunk was never called for claude, want at least one chunk")
	}
}

// ---------------- error mapping ----------------

func TestGenerate_ErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		spec    cliSpec
		model   string
		stderr  string
		wantSub string // substring expected in the mapped Reason
	}{
		{"claude not logged in", claudeSpec, "haiku", "Not logged in · Please run /login", "not logged in"},
		{"claude generic error", claudeSpec, "haiku", "boom", "boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakePATH(t)
			t.Setenv("SHELP_FAKE_EXIT", "1")
			t.Setenv("SHELP_FAKE_STDERR", c.stderr)
			t.Setenv("SHELP_FAKE_STDOUT", "")

			p := newProvider(c.spec, c.model)
			_, err := p.Generate(context.Background(), agentTestReq())
			if err == nil {
				t.Fatal("Generate() returned nil error, want *UnavailableError")
			}
			var ue *UnavailableError
			if !errors.As(err, &ue) {
				t.Fatalf("error is %T, want *UnavailableError: %v", err, err)
			}
			if !strings.Contains(strings.ToLower(ue.Reason), strings.ToLower(c.wantSub)) {
				t.Errorf("Reason = %q, want substring %q", ue.Reason, c.wantSub)
			}
		})
	}
}

// TestGenerate_RateLimitMapping covers the quota-string classification in
// each CLI's parse func: a non-zero exit whose stderr/stdout contains one
// of agentRateLimitPhrases must map to *RateLimitedError (which, via
// Unwrap, also satisfies errors.As(&UnavailableError) -- checked too, so a
// caller that only knows about UnavailableError still treats it as
// unavailable).
func TestGenerate_RateLimitMapping(t *testing.T) {
	cases := []struct {
		name   string
		spec   cliSpec
		model  string
		stderr string
	}{
		{"claude rate limit", claudeSpec, "haiku", "Error: rate limit exceeded, please try again"},
		{"claude quota", claudeSpec, "haiku", "You have exceeded your quota for this billing period"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakePATH(t)
			t.Setenv("SHELP_FAKE_EXIT", "1")
			t.Setenv("SHELP_FAKE_STDERR", c.stderr)
			t.Setenv("SHELP_FAKE_STDOUT", "")

			p := newProvider(c.spec, c.model)
			_, err := p.Generate(context.Background(), agentTestReq())
			if err == nil {
				t.Fatal("Generate() returned nil error, want *RateLimitedError")
			}
			var rle *RateLimitedError
			if !errors.As(err, &rle) {
				t.Fatalf("error is %T, want *RateLimitedError: %v", err, err)
			}
			var ue *UnavailableError
			if !errors.As(err, &ue) {
				t.Fatalf("error %v (%T) does not also satisfy errors.As(&UnavailableError)", err, err)
			}
		})
	}
}

func TestGenerate_EmptyOutput(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_STDOUT", "")
	t.Setenv("SHELP_FAKE_EXIT", "0")

	p := newProvider(claudeSpec, "haiku")
	_, err := p.Generate(context.Background(), agentTestReq())
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error is %T, want *UnavailableError for empty output: %v", err, err)
	}
}

// ---------------- timeout ----------------

func TestGenerate_Timeout(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_SLEEP", "5")
	t.Setenv("SHELP_FAKE_STDOUT", "too late")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	p := newProvider(claudeSpec, "haiku")
	start := time.Now()
	_, err := p.Generate(ctx, agentTestReq())
	elapsed := time.Since(start)

	if elapsed > 4*time.Second {
		t.Fatalf("Generate took %s, should have been cut short by the caller's context", elapsed)
	}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error is %T, want *UnavailableError: %v", err, err)
	}
	if !strings.Contains(ue.Reason, "timed out") {
		t.Errorf("Reason = %q, want to mention timing out", ue.Reason)
	}
}

// ---------------- Detect() identity checks ----------------

func TestDetect_Claude(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_VERSION", "2.1.285 (Claude Code)")
	t.Setenv("SHELP_FAKE_AUTH_JSON", `{"loggedIn":true,"email":"user@example.com","subscriptionType":"max"}`)

	p := newProvider(claudeSpec, "haiku")
	st := p.Detect(context.Background())
	if !st.Available {
		t.Fatalf("Detect() not available: %q", st.Reason)
	}
	if !strings.Contains(st.Version, "Claude Code") {
		t.Errorf("Version = %q, want to mention Claude Code", st.Version)
	}
}

func TestDetect_Claude_NotLoggedIn(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_VERSION", "2.1.285 (Claude Code)")
	t.Setenv("SHELP_FAKE_AUTH_JSON", `{"loggedIn":false}`)

	p := newProvider(claudeSpec, "haiku")
	st := p.Detect(context.Background())
	if st.Available {
		t.Fatal("Detect() available, want unavailable when loggedIn=false")
	}
	if !strings.Contains(strings.ToLower(st.Reason), "not logged in") {
		t.Errorf("Reason = %q, want to mention not logged in", st.Reason)
	}
}

func TestDetect_WrongIdentity(t *testing.T) {
	// A same-named binary that isn't actually the expected tool (e.g. the
	// "goose"/"q" collision this codebase already guards against for other
	// families) must not be reported as available.
	fakePATH(t)
	t.Setenv("SHELP_FAKE_VERSION", "totally-unrelated-tool 1.0.0")

	for _, spec := range []cliSpec{claudeSpec} {
		st := spec.identify(context.Background())
		if st.Available {
			t.Errorf("%s: Detect() available for an unrelated binary, want unavailable", spec.name)
		}
	}
}

// ---------------- Description() ----------------

func TestDescription_NoDoubleCLI(t *testing.T) {
	// A synthetic spec whose label already ends in "CLI" (the shape that
	// used to be exercised by the real, now-removed, copilot spec:
	// "GitHub Copilot CLI") exists purely to keep this regression test
	// covering that branch of Description()'s logic.
	labelAlreadyEndsInCLI := cliSpec{name: "synthetic", label: "Synthetic Agent CLI"}
	cases := []struct {
		spec     cliSpec
		cliModel string
		want     string
	}{
		{claudeSpec, "haiku", "Claude Code CLI, model haiku (headless, no tools)"},
		{labelAlreadyEndsInCLI, "default", "Synthetic Agent CLI, model default (headless, no tools)"},
	}
	for _, c := range cases {
		p := newProvider(c.spec, c.cliModel)
		got := p.Description()
		if got != c.want {
			t.Errorf("%s Description() = %q, want %q", c.spec.name, got, c.want)
		}
		if strings.Contains(got, "CLI CLI") {
			t.Errorf("%s Description() = %q, contains doubled \"CLI CLI\"", c.spec.name, got)
		}
	}
}

func TestUnavailableMsg_PrefersErrorLine(t *testing.T) {
	// Real-shaped output: a noisy first stderr line, then the actual error.
	res := CmdResult{ExitCode: 1, Stderr: "Reading additional input from stdin...\n" +
		"2026-09-30T13:28:42Z ERROR rmcp::transport::worker: worker quit with fatal\n" +
		"ERROR: You've hit your usage limit. Upgrade to Plus to continue, or try again at Oct 30th, 2026 8:27 AM.\n"}
	got := unavailableMsg(res)
	if !strings.HasPrefix(got, "ERROR: You've hit your usage limit") {
		t.Fatalf("unavailableMsg = %q", got)
	}
	_, err := claudeParse("claude:haiku", res)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("claudeParse err = %v, want *RateLimitedError", err)
	}
	if m := unavailableMsg(CmdResult{Stderr: "plain failure\nmore"}); m != "plain failure" {
		t.Fatalf("fallback = %q", m)
	}
}

func TestClaudeParse_StructuredAndText(t *testing.T) {
	ok := CmdResult{Stdout: `{"type":"result","is_error":false,"result":"{\"command\":\"ls -la\"}","structured_output":{"command":"ls -la"},"num_turns":2}`}
	if got, err := claudeParse("claude:haiku", ok); err != nil || got != "ls -la" {
		t.Fatalf("structured: got %q, %v", got, err)
	}
	limited := CmdResult{Stdout: `{"type":"result","is_error":true,"result":"Claude AI usage limit reached"}`}
	var rl *RateLimitedError
	if _, err := claudeParse("claude:haiku", limited); !errors.As(err, &rl) {
		t.Fatalf("is_error usage limit: err = %v, want RateLimited", err)
	}
	empty := CmdResult{Stdout: `{"type":"result","is_error":false,"result":""}`}
	if _, err := claudeParse("claude:haiku", empty); err == nil {
		t.Fatal("missing structured_output should be an error")
	}
	text := CmdResult{Stdout: "This command lists files.\n"}
	if got, err := claudeParse("claude:haiku", text); err != nil || got != text.Stdout {
		t.Fatalf("explain text: got %q, %v", got, err)
	}
}

func TestClaudeArgs_ExplainIsPlainText(t *testing.T) {
	args := claudeArgs("/tmp/x", "haiku", Request{Mode: Explain, System: "s", Prompt: "p"})
	if v, ok := argAfter(args, "--output-format"); !ok || v != "text" {
		t.Fatalf("explain --output-format = %q, %v; want text", v, ok)
	}
	if contains(args, "--json-schema") {
		t.Fatal("explain must not use --json-schema")
	}
}

func TestClaudeUsage(t *testing.T) {
	env := CmdResult{Stdout: `{"type":"result","is_error":false,"structured_output":{"command":"ls"},"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":5,"output_tokens":7}}`}
	if got := claudeUsage(env); got != (Usage{Input: 115, Output: 7}) {
		t.Fatalf("usage = %+v", got)
	}
	if got := claudeUsage(CmdResult{Stdout: "plain text explanation"}); got != (Usage{}) {
		t.Fatalf("plain text usage = %+v", got)
	}
}

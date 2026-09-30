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

// fakePATH puts testdata/agentcli (the fake claude/gemini/codex/copilot
// scripts) first on PATH, ahead of anything real that happens to be
// installed on the machine running the tests.
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
	args       []string
}

// readRecord parses the record file written by testdata/agentcli/*. The
// header (CWD/STDINBYTES/CDIREXISTS) is one line each; args follow an
// "ARGS:\n" marker, NUL-separated, since an arg value (system+"\n\n"+prompt)
// can itself contain literal newlines and so can't be newline-delimited.
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
	want := []string{"claude:haiku", "claude:sonnet", "gemini:default", "codex:default", "copilot:default"}
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
	cfg, err := config.Parse("[agentcli.gemini]\ndisabled = true\n")
	if err != nil {
		t.Fatal(err)
	}
	ps := agentCLIFamily{}.Providers(cfg)
	for _, p := range ps {
		if strings.HasPrefix(p.ID(), "gemini:") {
			t.Errorf("Providers() with [agentcli.gemini] disabled still returned %s", p.ID())
		}
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
	if v, ok := argAfter(r.args, "--model"); !ok || v != "haiku" {
		t.Errorf("--model = %q, %v; want haiku", v, ok)
	}
	if v, ok := argAfter(r.args, "--system-prompt"); !ok || v != req.System {
		t.Errorf("--system-prompt = %q, %v; want %q", v, ok, req.System)
	}
	if !contains(r.args, "-p") {
		t.Error("missing -p (print/non-interactive)")
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

func TestGenerate_Gemini_ArgvAndCwd(t *testing.T) {
	fakePATH(t)
	rec := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv("SHELP_FAKE_RECORD", rec)
	t.Setenv("SHELP_FAKE_STDOUT", "git log -5 --oneline")

	p := newProvider(geminiSpec, "")
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
		t.Errorf("stdin bytes = %d, want 0", r.stdinBytes)
	}
	if r.cwd == startCwd {
		t.Error("cwd was not changed to a scratch dir")
	}
	if !contains(r.args, "--skip-trust") {
		t.Error("missing --skip-trust (temp dir is always untrusted)")
	}
	if v, ok := argAfter(r.args, "--approval-mode"); !ok || v != "plan" {
		t.Errorf("--approval-mode = %q, %v; want plan", v, ok)
	}
	if contains(r.args, "--model") {
		t.Errorf("gemini:default should omit --model, got %v", r.args)
	}
	want := req.System + "\n\n" + req.Prompt
	if v, ok := argAfter(r.args, "--prompt"); !ok || v != want {
		t.Errorf("--prompt = %q, %v; want %q (no separate system flag)", v, ok, want)
	}
}

// TestGenerate_ConcurrentClaudeGeminiDistinctCwd is the regression test for
// the os.Chdir + chdirMu removal: two Generate calls run concurrently (as
// shelp-bench does across providers, in one process that itself relies on
// relative paths) must each see their OWN scratch temp dir as the
// subprocess's working directory, never the other's and never this
// process's own cwd -- and this process's own cwd must never move at all.
// Run with `go test -race` to also catch any reintroduced shared mutable
// state (a chdir, a shared record file, a racy env mutation).
func TestGenerate_ConcurrentClaudeGeminiDistinctCwd(t *testing.T) {
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
	claudeCwds := make([]string, n)
	geminiCwds := make([]string, n)
	claudeErrs := make([]error, n)
	geminiErrs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			p := newProvider(claudeSpec, "haiku")
			claudeCwds[i], claudeErrs[i] = p.Generate(context.Background(), agentTestReq())
		}(i)
		go func(i int) {
			defer wg.Done()
			p := newProvider(geminiSpec, "")
			geminiCwds[i], geminiErrs[i] = p.Generate(context.Background(), agentTestReq())
		}(i)
	}
	wg.Wait()

	if procCwd, _ := os.Getwd(); procCwd != startCwd {
		t.Errorf("this process's cwd changed to %s, want unchanged %s", procCwd, startCwd)
	}

	seen := map[string]int{} // cwd -> how many calls reported it
	for i := 0; i < n; i++ {
		if claudeErrs[i] != nil {
			t.Fatalf("claude Generate[%d]: %v", i, claudeErrs[i])
		}
		if geminiErrs[i] != nil {
			t.Fatalf("gemini Generate[%d]: %v", i, geminiErrs[i])
		}
		if claudeCwds[i] == "" || geminiCwds[i] == "" {
			t.Fatalf("empty cwd reported: claude=%q gemini=%q", claudeCwds[i], geminiCwds[i])
		}
		if claudeCwds[i] == startCwd || geminiCwds[i] == startCwd {
			t.Fatalf("a call saw this process's own cwd instead of a scratch dir: claude=%q gemini=%q startCwd=%q", claudeCwds[i], geminiCwds[i], startCwd)
		}
		if claudeCwds[i] == geminiCwds[i] {
			t.Fatalf("round %d: claude and gemini saw the SAME cwd %q, want distinct scratch dirs", i, claudeCwds[i])
		}
		seen[claudeCwds[i]]++
		seen[geminiCwds[i]]++
	}
	for dir, count := range seen {
		if count != 1 {
			t.Errorf("cwd %q was reported by %d calls, want exactly 1 (each Generate call gets its own scratch dir)", dir, count)
		}
	}
}

func TestGenerate_Codex_ArgvAndOutFile(t *testing.T) {
	fakePATH(t)
	rec := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv("SHELP_FAKE_RECORD", rec)
	// The "real" reply goes to the -o file; stdout carries unrelated noise
	// (banners/warnings/MCP errors) that parse must ignore.
	t.Setenv("SHELP_FAKE_STDOUT", "git log -5 --oneline")
	t.Setenv("SHELP_FAKE_STDOUT_NOISE", "OpenAI Codex v0.144.0\n--------\nnoise\n")

	p := newProvider(codexSpec, "gpt-5.6-terra")
	req := agentTestReq()
	startCwd, _ := os.Getwd()

	got, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "git log -5 --oneline" {
		t.Errorf("Generate() = %q, want clean reply from -o file, not stdout noise", got)
	}
	r := readRecord(t, rec)
	if r.stdinBytes != 0 {
		t.Errorf("stdin bytes = %d, want 0", r.stdinBytes)
	}
	// Generate now sets RunCmdOpts.Dir for every CLI (not just the ones
	// without their own -C flag), so codex's subprocess cwd is the scratch
	// dir even though it also gets an explicit -C <dir> argument below;
	// this process's own cwd (startCwd) must be untouched either way.
	if r.cwd == startCwd {
		t.Error("cwd was not changed to a scratch dir")
	}
	if procCwd, _ := os.Getwd(); procCwd != startCwd {
		t.Errorf("this process's cwd changed to %s, want unchanged %s", procCwd, startCwd)
	}
	if len(r.args) == 0 || r.args[0] != "exec" {
		t.Errorf("args[0] = %v, want \"exec\"", r.args)
	}
	if v, ok := argAfter(r.args, "-s"); !ok || v != "read-only" {
		t.Errorf("-s = %q, %v; want read-only", v, ok)
	}
	dir, ok := argAfter(r.args, "-C")
	if !ok {
		t.Fatal("missing -C <dir>")
	}
	if dir == "" {
		t.Error("-C dir is empty")
	}
	if r.cDirExists != "yes" {
		t.Errorf("-C dir %q did not exist at call time (cDirExists=%q)", dir, r.cDirExists)
	}
	if !contains(r.args, "--skip-git-repo-check") {
		t.Error("missing --skip-git-repo-check (temp dir is not a git repo)")
	}
	if !contains(r.args, "--ephemeral") {
		t.Error("missing --ephemeral (no session persistence)")
	}
	if v, ok := argAfter(r.args, "-m"); !ok || v != "gpt-5.6-terra" {
		t.Errorf("-m = %q, %v; want gpt-5.6-terra", v, ok)
	}
	outFile, ok := argAfter(r.args, "-o")
	if !ok {
		t.Fatal("missing -o <file>")
	}
	if r.args[len(r.args)-1] != req.System+"\n\n"+req.Prompt {
		t.Errorf("last arg = %q, want system+prompt", r.args[len(r.args)-1])
	}
	_ = outFile // already exercised via got == clean reply above
}

func TestGenerate_Codex_NoStreaming(t *testing.T) {
	// codex's stdout is not the reply (banners/MCP errors), so Generate
	// must never forward it to OnChunk even when the caller streams.
	fakePATH(t)
	t.Setenv("SHELP_FAKE_STDOUT", "the reply")
	t.Setenv("SHELP_FAKE_STDOUT_NOISE", "should never reach OnChunk")

	var chunks []string
	req := agentTestReq()
	req.OnChunk = func(s string) { chunks = append(chunks, s) }

	p := newProvider(codexSpec, "")
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("OnChunk called %d times for codex, want 0: %v", len(chunks), chunks)
	}
}

func TestGenerate_Claude_Streams(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_STDOUT", "the reply")

	var chunks []string
	req := agentTestReq()
	req.OnChunk = func(s string) { chunks = append(chunks, s) }

	p := newProvider(claudeSpec, "haiku")
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("OnChunk was never called for claude, want at least one chunk")
	}
}

func TestGenerate_Copilot_ArgvAndCwd(t *testing.T) {
	fakePATH(t)
	rec := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv("SHELP_FAKE_RECORD", rec)
	t.Setenv("SHELP_FAKE_STDOUT", "git log -5 --oneline")

	p := newProvider(copilotSpec, "auto")
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
		t.Errorf("stdin bytes = %d, want 0", r.stdinBytes)
	}
	// See the equivalent comment in TestGenerate_Codex_ArgvAndOutFile: Dir
	// is now set for every CLI, so copilot's subprocess cwd is the scratch
	// dir too, alongside its own -C <dir> argument.
	if r.cwd == startCwd {
		t.Error("cwd was not changed to a scratch dir")
	}
	if procCwd, _ := os.Getwd(); procCwd != startCwd {
		t.Errorf("this process's cwd changed to %s, want unchanged %s", procCwd, startCwd)
	}
	if v, ok := argAfter(r.args, "-p"); !ok || v != req.System+"\n\n"+req.Prompt {
		t.Errorf("-p = %q, %v; want system+prompt", v, ok)
	}
	if !contains(r.args, "-s") {
		t.Error("missing -s (silent)")
	}
	dir, ok := argAfter(r.args, "-C")
	if !ok {
		t.Fatal("missing -C <dir>")
	}
	if dir == "" {
		t.Error("-C dir is empty")
	}
	if r.cDirExists != "yes" {
		t.Errorf("-C dir %q did not exist at call time (cDirExists=%q)", dir, r.cDirExists)
	}
	if !contains(r.args, "--available-tools") {
		t.Error("missing --available-tools (no value => zero tools)")
	}
	if v, ok := argAfter(r.args, "--model"); !ok || v != "auto" {
		t.Errorf("--model = %q, %v; want auto", v, ok)
	}
	if contains(r.args, "--no-auto-update") {
		t.Error("--no-auto-update must be omitted: it corrupts copilot's arg parser together with -p and -C (see agentcli.go)")
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
		{"gemini auth", geminiSpec, "", "Error authenticating: FatalCancellationError: Authentication cancelled by user.", "authentication"},
		{"gemini untrusted", geminiSpec, "", "Gemini CLI is not running in a trusted directory.", "trust"},
		{"codex generic error", codexSpec, "", "some codex failure", "codex exited"},
		{"copilot model unavailable", copilotSpec, "gpt-5.4", "Error: Model \"gpt-5.4\" from --model flag is not available.", "not available"},
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

func TestGenerate_CodexModelNotSupported(t *testing.T) {
	// codex prints the "not supported" error to stdout with exit 0 in some
	// builds (observed live); the outFile is then empty. parse must still
	// map this to Unavailable, not "no output".
	fakePATH(t)
	t.Setenv("SHELP_FAKE_STDOUT_NOISE", `ERROR: {"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'x' model is not supported when using Codex with a ChatGPT account."}}`)
	// leave SHELP_FAKE_STDOUT unset so the -o file stays empty

	p := newProvider(codexSpec, "some-bad-model")
	_, err := p.Generate(context.Background(), agentTestReq())
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("error is %T, want *UnavailableError: %v", err, err)
	}
	if !strings.Contains(ue.Reason, "not available for this account") {
		t.Errorf("Reason = %q, want mention of account model support", ue.Reason)
	}
}

// ---------------- timeout ----------------

func TestGenerate_Timeout(t *testing.T) {
	fakePATH(t)
	t.Setenv("SHELP_FAKE_SLEEP", "5")
	t.Setenv("SHELP_FAKE_STDOUT", "too late")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	p := newProvider(geminiSpec, "")
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
	t.Setenv("SHELP_FAKE_AUTH_JSON", `{"loggedIn":true,"email":"eric@wave-rf.com","subscriptionType":"max"}`)

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

	for _, spec := range []cliSpec{claudeSpec, codexSpec, copilotSpec} {
		st := spec.identify(context.Background())
		if st.Available {
			t.Errorf("%s: Detect() available for an unrelated binary, want unavailable", spec.name)
		}
	}
	t.Setenv("SHELP_FAKE_HELP", "Some Other Tool - not Gemini")
	if st := geminiSpec.identify(context.Background()); st.Available {
		t.Error("gemini: Detect() available for an unrelated binary, want unavailable")
	}
}

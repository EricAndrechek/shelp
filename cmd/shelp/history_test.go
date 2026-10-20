package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/history"
	"github.com/ericandrechek/shelp/internal/style"
)

// fakeBackend serves an OpenAI-compatible endpoint answering `answer`
// (with usage 11/4) and points the config at it as provider "fake:m". It
// also gives the test its own state dir, so the history starts empty.
func fakeBackend(t *testing.T, answer string, extraCfg string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Stream bool }
		json.NewDecoder(r.Body).Decode(&req)
		usage := map[string]int{"prompt_tokens": 11, "completion_tokens": 4}
		if os.Getenv("FAKE_NO_USAGE") == "1" {
			usage = nil
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": answer}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			b, _ = json.Marshal(map[string]any{"choices": []any{}, "usage": usage})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": answer}}}, "usage": usage})
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.toml")
	cfg := fmt.Sprintf("providers = [\"fake:m\"]\n%s\n[[openai.endpoint]]\nname = \"fake\"\nbase_url = %q\napi_key_env = \"SHELP_FAKE_KEY\"\nmodels = [\"m\"]\n", extraCfg, srv.URL)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELP_CONFIG", cfgPath)
	t.Setenv("SHELP_FAKE_KEY", "k")
	t.Setenv("SHELP_PROVIDER", "")
	t.Setenv("SHELP_HISTORY", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	setColorEnv(t, "")
}

func readHistory(t *testing.T) []history.Record {
	t.Helper()
	recs, err := history.Read(historyPath())
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestHistoryRecordsSuggestAndExplain(t *testing.T) {
	fakeBackend(t, "ls -la", "")
	if _, _, err := runOut(t, "list", "files"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runOut(t, "explain", "ls", "-la"); err != nil {
		t.Fatal(err)
	}
	recs := readHistory(t)
	if len(recs) != 2 {
		t.Fatalf("got %d records: %+v", len(recs), recs)
	}
	s, e := recs[0], recs[1]
	if s.Mode != "suggest" || s.Input != "list files" || s.Output != "ls -la" || s.Provider != "fake:m" ||
		s.Tokens == nil || *s.Tokens != (history.Tokens{Input: 11, Output: 4}) || s.Key == "" || s.Template == "" || s.Error != "" {
		t.Errorf("suggest record = %+v", s)
	}
	if _, err := os.Stat(historyPath()); err != nil {
		t.Fatal(err)
	}
	if e.Mode != "explain" || e.Input != "ls -la" || e.Output != "ls -la" || e.FirstChunkMS < 0 {
		t.Errorf("explain record = %+v", e)
	}
	if s.Key == e.Key {
		t.Error("suggest and explain share a key")
	}
	// the same request has the same key
	runOut(t, "list", "files")
	if again := readHistory(t)[2]; again.Key != s.Key {
		t.Errorf("key changed between identical requests: %q vs %q", again.Key, s.Key)
	}
}

func TestHistoryDisabled(t *testing.T) {
	fakeBackend(t, "ls", "[history]\nenabled = false\n")
	runOut(t, "list", "files")
	if _, err := os.Stat(historyPath()); err == nil {
		t.Error("history written although [history] enabled = false")
	}

	fakeBackend(t, "ls", "")
	t.Setenv("SHELP_HISTORY", "0")
	runOut(t, "list", "files")
	if _, err := os.Stat(historyPath()); err == nil {
		t.Error("history written although SHELP_HISTORY=0")
	}
}

func TestHistoryRecordsFailureNotCancel(t *testing.T) {
	fakeBackend(t, "ls", "")
	t.Setenv("SHELP_FAKE_KEY", "") // the endpoint is unavailable
	var out, errb bytes.Buffer
	if err := run(context.Background(), []string{"list", "files"}, &out, &errb); err == nil {
		t.Fatal("want an error")
	}
	recs := readHistory(t)
	if len(recs) != 1 || recs[0].Error == "" || recs[0].Output != "" || recs[0].Provider != "" || len(recs[0].Tried) != 1 {
		t.Fatalf("records = %+v", recs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run(ctx, []string{"list", "files"}, &out, &errb)
	if n := len(readHistory(t)); n != 1 {
		t.Errorf("a cancelled call was recorded (%d records)", n)
	}
}

func seedHistory(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	add := func(mode, in, out, prov string, tok *history.Tokens) {
		history.Append(historyPath(), history.Record{Time: history.Now(), Mode: mode, Input: in, Output: out, Provider: prov, LatencyMS: 1400, Tokens: tok, Key: "k"}, 100)
	}
	add("suggest", "find big files", "find . -size +100M", "fm", nil)
	add("explain", "du -sh *", "Shows disk usage.\nSecond line.", "claude:haiku", &history.Tokens{Input: 300, Output: 112})
	add("suggest", "list ports", "lsof -nP -iTCP -sTCP:LISTEN", "fm", nil)
}

func TestHistoryList(t *testing.T) {
	setColorEnv(t, "")
	seedHistory(t)
	out, errOut, err := runOut(t, "history")
	if err != nil || errOut != "" {
		t.Fatal(err, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("out:\n%s", out)
	}
	if !strings.Contains(lines[0], "find big files") || !strings.Contains(lines[0], "→ find . -size +100M") || !strings.Contains(lines[0], "fm") || !strings.Contains(lines[0], "1.4s") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.Contains(lines[1], "explain") || !strings.Contains(lines[1], "412 tok") || !strings.Contains(lines[1], "→ Shows disk usage.") || strings.Contains(lines[1], "Second line") {
		t.Errorf("line 1 = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "3 ") {
		t.Errorf("newest entry should be last and numbered 3: %q", lines[2])
	}

	out, _, _ = runOut(t, "history", "-n", "1")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "list ports") {
		t.Errorf("-n 1: %q", out)
	}
	out, _, _ = runOut(t, "history", "--search", "DISK")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "du -sh") || !strings.HasPrefix(out, "2 ") {
		t.Errorf("search keeps its own numbering: %q", out)
	}
	out, _, _ = runOut(t, "history", "-s", "tcp") // matches output, case-insensitively
	if !strings.Contains(out, "list ports") {
		t.Errorf("search over output: %q", out)
	}
	out, _, _ = runOut(t, "history", "--provider", "fm", "--all")
	if strings.Count(out, "\n") != 2 || strings.Contains(out, "explain") {
		t.Errorf("--provider: %q", out)
	}
	out, _, _ = runOut(t, "history", "-s", "nothing-like-this")
	if !strings.Contains(out, "no matching history") {
		t.Errorf("no match: %q", out)
	}
}

func TestHistoryJSON(t *testing.T) {
	seedHistory(t)
	out, _, err := runOut(t, "history", "--json", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("out %q", out)
	}
	var r history.Record
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil || r.Input != "du -sh *" || r.Tokens == nil || r.Tokens.Output != 112 {
		t.Errorf("record %+v err %v", r, err)
	}
}

func TestHistoryEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setColorEnv(t, "")
	out, _, err := runOut(t, "history")
	if err != nil || !strings.Contains(out, "no history yet") {
		t.Errorf("%q %v", out, err)
	}
}

func TestHistoryLineTruncates(t *testing.T) {
	line := historyLine(style.Plain(), 1, 1, history.Record{Time: history.Now(), Mode: "suggest", Input: strings.Repeat("x ", 80), Output: "ls", Provider: "fm"})
	got := style.Truncate(line, 50)
	if style.Width(got) != 50 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated to %d cols: %q", style.Width(got), got)
	}
}

func TestHistoryShow(t *testing.T) {
	setColorEnv(t, "")
	seedHistory(t)
	out, _, err := runOut(t, "history", "show", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude:haiku", "300 in, 112 out", "du -sh *", "Shows disk usage.\nSecond line.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"0", "9", "x", "-1"} {
		if _, _, err := runOut(t, "history", "show", bad); err == nil {
			t.Errorf("show %s should fail", bad)
		}
	}
}

func TestHistoryPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/somewhere")
	out, _, err := runOut(t, "history", "path")
	if err != nil || out != "/somewhere/shelp/history.jsonl\n" {
		t.Errorf("%q %v", out, err)
	}
}

func TestHistoryClear(t *testing.T) {
	seedHistory(t)
	// not a terminal, no --yes: refuses and keeps the file
	_, _, err := runOut(t, "history", "clear")
	if err == nil {
		t.Fatal("clear without --yes off a terminal should refuse")
	}
	if len(readHistory(t)) != 3 {
		t.Fatal("history changed by a refused clear")
	}
	if _, _, err := runOut(t, "history", "clear", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(readHistory(t)) != 0 {
		t.Fatal("history survived clear --yes")
	}

	// on a terminal it asks
	realI, realIn := isInteractive, stdin
	t.Cleanup(func() { isInteractive, stdin = realI, realIn })
	isInteractive = func(_ io.Writer) bool { return true }
	seedHistory(t)
	stdin = strings.NewReader("n\n")
	out, _, _ := runOut(t, "history", "clear")
	if !strings.Contains(out, "[y/N]") || len(readHistory(t)) != 3 {
		t.Errorf("answering n should keep the history: %q", out)
	}
	stdin = strings.NewReader("y\n")
	runOut(t, "history", "clear")
	if len(readHistory(t)) != 0 {
		t.Error("answering y should clear")
	}
}

func TestConfigShowHistoryLine(t *testing.T) {
	fakeBackend(t, "ls", "[history]\nmax_entries = 7\n")
	out, _, err := runOut(t, "config")
	if err != nil || !strings.Contains(out, "history") || !strings.Contains(out, "history.jsonl") || !strings.Contains(out, "on, keeps 7") {
		t.Errorf("%q %v", out, err)
	}
}

func TestIsHistoryArgsTask(t *testing.T) {
	if commandFor([]string{"history", "of", "git", "commits", "for", "main.go"}) != "" {
		t.Error("a sentence starting with history is a task")
	}
}

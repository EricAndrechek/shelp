package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// TestMain points XDG_CACHE_HOME at a throwaway directory for the whole
// package test run, so tests never read or write the real
// ~/.cache/shelp/local-models.json, and fakes the shortcut version probe. Individual tests that want an empty,
// per-test cache still call setCacheHome.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shelp-openai-test-cache")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CACHE_HOME", dir)
	// Never run a real installed shortcut from a unit test: the version
	// probe defaults to "current format". Tests that need another answer
	// replace it themselves.
	shortcutMarkerVersionCmd = func(context.Context, string) (int, error) { return ShortcutFormatVersion, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- helpers ---------------------------------------------------------------

func testReq(t *testing.T, onChunk func(string)) Request {
	t.Helper()
	return Request{Mode: Suggest, System: "sys", Prompt: "list files", OnChunk: onChunk}
}

func setCacheHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

// --- non-streaming -----------------------------------------------------

func TestGenerate_NonStreaming(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"find . -type f"}}]}`)
	}))
	defer srv.Close()

	t.Setenv("TEST_KEY", "secret-key")
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, apiKeyEnv: "TEST_KEY", model: "model-a", label: "unit"}

	text, err := p.Generate(context.Background(), testReq(t, nil))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "find . -type f" {
		t.Fatalf("text = %q", text)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
	var req chatRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, gotBody)
	}
	if req.Model != "model-a" || req.Stream || len(req.Messages) != 2 {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "sys" {
		t.Fatalf("system message: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "list files" {
		t.Fatalf("user message: %+v", req.Messages[1])
	}
	if req.MaxTokens != 256 {
		t.Fatalf("suggest max_tokens = %d, want 256", req.MaxTokens)
	}
}

func TestGenerate_ExplainUsesLargerMaxTokens(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"it lists files"}}]}`)
	}))
	defer srv.Close()

	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	req := testReq(t, nil)
	req.Mode = Explain
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var cr chatRequest
	if err := json.Unmarshal([]byte(gotBody), &cr); err != nil {
		t.Fatal(err)
	}
	if cr.MaxTokens != 512 {
		t.Fatalf("explain max_tokens = %d, want 512", cr.MaxTokens)
	}
}

// --- streaming -----------------------------------------------------------

func TestGenerate_Streaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			t.Errorf("expected stream:true in request body")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{"find", " .", " -type f"} {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": chunk}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	var streamed strings.Builder
	text, err := p.Generate(context.Background(), testReq(t, func(s string) { streamed.WriteString(s) }))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	const want = "find . -type f"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if streamed.String() != want {
		t.Fatalf("streamed = %q, want %q", streamed.String(), want)
	}
}

// --- think-stripping -------------------------------------------------------

func TestStripThink_NonStreaming(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<think>reasoning here</think>find . -type f", "find . -type f"},
		{"before<think>reasoning</think>after", "beforeafter"},
		{"no think tags here", "no think tags here"},
		{"<think>only reasoning</think>", ""},
		{"<think>multi\nline\nreasoning</think>ok", "ok"},
	}
	for _, c := range cases {
		if got := stripThink(c.in); got != c.want {
			t.Errorf("stripThink(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGenerate_StripsThinkNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"<think>let me think about this</think>find . -type f"}}]}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	text, err := p.Generate(context.Background(), testReq(t, nil))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "find . -type f" {
		t.Fatalf("text = %q", text)
	}
}

func TestGenerate_StripsThinkStreamingAndNeverForwardsIt(t *testing.T) {
	// Split the <think> tag itself across SSE chunks to exercise the
	// boundary-tolerant filter, and confirm reasoning text never reaches
	// onChunk.
	pieces := []string{"<thi", "nk>reasoning ", "content</th", "ink>find", " .", " -type f"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range pieces {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": chunk}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	var streamed strings.Builder
	text, err := p.Generate(context.Background(), testReq(t, func(s string) {
		if strings.Contains(s, "reasoning") || strings.Contains(s, "think") {
			t.Errorf("reasoning content leaked to OnChunk: %q", s)
		}
		streamed.WriteString(s)
	}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	const want = "find . -type f"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if streamed.String() != want {
		t.Fatalf("streamed = %q, want %q", streamed.String(), want)
	}
}

func TestGenerate_IgnoresReasoningContentField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"","reasoning_content":"secret reasoning"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ls -la"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	var streamed strings.Builder
	text, err := p.Generate(context.Background(), testReq(t, func(s string) { streamed.WriteString(s) }))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "ls -la" || streamed.String() != "ls -la" {
		t.Fatalf("text=%q streamed=%q", text, streamed.String())
	}
}

// --- error mapping -----------------------------------------------------

func TestGenerate_401IsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, apiKeyEnv: "", model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	assertUnavailable(t, err)
}

func TestGenerate_429IsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	assertUnavailable(t, err)
}

func TestGenerate_429IsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	var rle *RateLimitedError
	if !errors.As(err, &rle) {
		t.Fatalf("error = %v (%T), want *RateLimitedError", err, err)
	}
	if rle.ID != "unit:model-a" {
		t.Errorf("ID = %q, want unit:model-a", rle.ID)
	}
	if rle.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 (no Retry-After header)", rle.RetryAfter)
	}
}

func TestGenerate_429WithRetryAfterSecondsIsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	var rle *RateLimitedError
	if !errors.As(err, &rle) {
		t.Fatalf("error = %v (%T), want *RateLimitedError", err, err)
	}
	if rle.RetryAfter != 42*time.Second {
		t.Errorf("RetryAfter = %v, want 42s", rle.RetryAfter)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "120", 120 * time.Second},
		{"zero seconds", "0", 0},
		{"negative seconds", "-5", 0},
		{"garbage", "not-a-duration", 0},
		{"past http-date", "Sun, 06 Nov 1994 08:49:37 GMT", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseRetryAfter(c.in); got != c.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
	// A future HTTP-date should parse to a roughly-correct positive
	// duration; check with tolerance rather than an exact value.
	future := time.Now().Add(90 * time.Second).UTC()
	got := parseRetryAfter(future.Format(http.TimeFormat))
	if got < 80*time.Second || got > 100*time.Second {
		t.Errorf("parseRetryAfter(future http-date) = %v, want ~90s", got)
	}
}

func TestGenerate_404IsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	assertUnavailable(t, err)
}

func TestGenerate_500IsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	defer srv.Close()
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: srv.URL, model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if ue, ok := err.(*UnavailableError); ok {
		t.Fatalf("500 should not be Unavailable, got %v", ue)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should include body excerpt: %v", err)
	}
}

func TestGenerate_ConnectionRefusedIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close() // nothing listens now

	p := &openaiProvider{id: "lmstudio:model-a", kind: LocalServer, baseURL: "http://" + addr, model: "model-a", label: "lmstudio"}
	start := time.Now()
	_, err := p.Generate(context.Background(), testReq(t, nil))
	elapsed := time.Since(start)
	assertUnavailable(t, err)
	if elapsed > 2*time.Second {
		t.Fatalf("connection refused took too long: %s", elapsed)
	}
}

func TestGenerate_MissingAPIKeyIsUnavailable(t *testing.T) {
	t.Setenv("SHELP_TEST_MISSING_KEY", "") // present but empty: same as unset for our purposes
	p := &openaiProvider{id: "unit:model-a", kind: API, baseURL: "http://127.0.0.1:1", apiKeyEnv: "SHELP_TEST_MISSING_KEY", model: "model-a", label: "unit"}
	_, err := p.Generate(context.Background(), testReq(t, nil))
	assertUnavailable(t, err)
	if !strings.Contains(err.Error(), "SHELP_TEST_MISSING_KEY") {
		t.Fatalf("error should name the env var: %v", err)
	}
}

// assertUnavailable checks the "is this provider unavailable" contract the
// way real callers do (errors.As, which also finds an *UnavailableError
// reachable via Unwrap -- e.g. a *RateLimitedError, which is a form of
// unavailability), not a direct type assertion.
func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("expected an error satisfying errors.As(&UnavailableError), got %T: %v", err, err)
	}
}

// --- config decoding / presets -------------------------------------------

func TestConfigDecoding_PresetFillsBaseURLAndKeyEnv(t *testing.T) {
	toml := `
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"
models = ["some/model", "other/model"]
`
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	ps := openaiFamily{}.Providers(cfg)
	var got []string
	for _, p := range ps {
		if p.Kind() == API {
			got = append(got, p.ID())
		}
	}
	want := []string{"openrouter:some/model", "openrouter:other/model"}
	if !equalUnordered(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, p := range ps {
		if p.ID() == "openrouter:some/model" {
			op := p.(*openaiProvider)
			if op.baseURL != "https://openrouter.ai/api/v1" {
				t.Fatalf("base_url = %q", op.baseURL)
			}
			if op.apiKeyEnv != "OPENROUTER_API_KEY" {
				t.Fatalf("api_key_env = %q", op.apiKeyEnv)
			}
			if op.model != "some/model" {
				t.Fatalf("model casing not preserved: %q", op.model)
			}
		}
	}
}

func TestConfigDecoding_ExplicitOverridesPreset(t *testing.T) {
	toml := `
[[openai.endpoint]]
name = "custom"
preset = "openai"
base_url = "https://my-proxy.internal/v1"
api_key_env = "MY_PROXY_KEY"
models = ["gpt-x"]
temperature = 0.7
`
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	ps := apiProvidersOnly(openaiFamily{}.Providers(cfg))
	if len(ps) != 1 {
		t.Fatalf("got %d API providers, want 1: %v", len(ps), idsOf(ps))
	}
	op := ps[0].(*openaiProvider)
	if op.baseURL != "https://my-proxy.internal/v1" {
		t.Fatalf("base_url = %q", op.baseURL)
	}
	if op.apiKeyEnv != "MY_PROXY_KEY" {
		t.Fatalf("api_key_env = %q", op.apiKeyEnv)
	}
	if op.temperature != 0.7 {
		t.Fatalf("temperature = %v", op.temperature)
	}
	if op.id != "custom:gpt-x" {
		t.Fatalf("id = %q", op.id)
	}
}

func TestConfigDecoding_LocalServers(t *testing.T) {
	toml := `
[[openai.local]]
name = "fm-serve"
base_url = "http://127.0.0.1:9999/v1"
`
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	setCacheHome(t)
	ps := openaiFamily{}.Providers(cfg)
	names := map[string]bool{}
	for _, p := range ps {
		names[p.ID()] = true
	}
	for _, want := range []string{"lmstudio", "ollama", "llamacpp", "fm-serve"} {
		if !names[want] {
			t.Errorf("missing bare local candidate %q; have %v", want, names)
		}
	}
}

func TestConfigDecoding_UnknownPresetLeftOutWithNoBaseURL(t *testing.T) {
	toml := `
[[openai.endpoint]]
name = "mystery"
preset = "does-not-exist"
models = ["m"]
`
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	ps := openaiFamily{}.Providers(cfg)
	for _, p := range ps {
		if strings.HasPrefix(p.ID(), "mystery") {
			t.Fatalf("should not build a provider with no resolvable base_url, got %s", p.ID())
		}
	}
}

// --- local model cache ---------------------------------------------------

func TestLocalModelCache_ReadWrite(t *testing.T) {
	dir := setCacheHome(t)
	if err := writeLocalModelCache("lmstudio", "http://127.0.0.1:1234/v1", []string{"qwen3-30b", "llama-3.1-8b"}); err != nil {
		t.Fatalf("writeLocalModelCache: %v", err)
	}
	path := filepath.Join(dir, "shelp", "local-models.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	entries := readLocalModelCache()
	if len(entries) != 1 || entries[0].Name != "lmstudio" || len(entries[0].Models) != 2 {
		t.Fatalf("unexpected cache contents: %+v", entries)
	}
	if entries[0].Models[0] != "qwen3-30b" {
		t.Fatalf("model order not preserved: %v", entries[0].Models)
	}

	// A second write for a different server should not clobber the first.
	if err := writeLocalModelCache("ollama", "http://127.0.0.1:11434/v1", []string{"llama3"}); err != nil {
		t.Fatalf("writeLocalModelCache: %v", err)
	}
	entries = readLocalModelCache()
	if len(entries) != 2 {
		t.Fatalf("expected 2 cache entries, got %d: %+v", len(entries), entries)
	}

	// Providers() should pick up the cached models as fixed candidates.
	cfg, _ := config.Parse("")
	ps := openaiFamily{}.Providers(cfg)
	found := map[string]bool{}
	for _, p := range ps {
		found[p.ID()] = true
	}
	if !found["lmstudio:qwen3-30b"] || !found["ollama:llama3"] {
		t.Fatalf("expected cached models as candidates, got IDs: %v", idsOf(ps))
	}
}

func TestLocalModelCache_MissingFileIsEmpty(t *testing.T) {
	setCacheHome(t)
	entries := readLocalModelCache()
	if entries != nil {
		t.Fatalf("expected nil for missing cache, got %v", entries)
	}
}

// --- Providers() does no network I/O --------------------------------------

func TestProviders_NoNetworkIO(t *testing.T) {
	setCacheHome(t)
	toml := `
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"
models = ["some/model"]

[[openai.local]]
name = "fm-serve"
base_url = "http://127.0.0.1:9/v1"
`
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = openaiFamily{}.Providers(cfg)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Providers() took too long; suspect it did network I/O")
	}
}

// --- ID casing ---------------------------------------------------------

func TestProviderID_LowercasedButModelCasingPreserved(t *testing.T) {
	toml := `
[[openai.endpoint]]
name = "OpenRouter"
preset = "openrouter"
models = ["Meta-Llama/Llama-3.1-8B"]
`
	cfg, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	ps := apiProvidersOnly(openaiFamily{}.Providers(cfg))
	if len(ps) != 1 {
		t.Fatalf("got %d API providers: %v", len(ps), idsOf(ps))
	}
	if ps[0].ID() != "openrouter:meta-llama/llama-3.1-8b" {
		t.Fatalf("id = %q", ps[0].ID())
	}
	op := ps[0].(*openaiProvider)
	if op.model != "Meta-Llama/Llama-3.1-8B" {
		t.Fatalf("model casing not preserved: %q", op.model)
	}
}

// --- misc helpers ----------------------------------------------------------

func apiProvidersOnly(ps []Provider) []Provider {
	var out []Provider
	for _, p := range ps {
		if p.Kind() == API {
			out = append(out, p)
		}
	}
	return out
}

func idsOf(ps []Provider) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ID())
	}
	return out
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]int{}
	for _, s := range a {
		am[s]++
	}
	for _, s := range b {
		am[s]--
	}
	for _, v := range am {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestDetectShortReasonAndFix(t *testing.T) {
	t.Setenv("SHELP_TEST_KEY_UNSET", "")
	p := &openaiProvider{id: "x:m", kind: API, baseURL: "https://api.example.com/v1", apiKeyEnv: "SHELP_TEST_KEY_UNSET", model: "m"}
	st := p.Detect(context.Background())
	if st.Available || st.Reason != "no API key" || st.Fix != "set $SHELP_TEST_KEY_UNSET" {
		t.Errorf("Detect() = %+v", st)
	}
	if LabelOf(p) != "m" {
		t.Errorf("LabelOf = %q", LabelOf(p))
	}
	local := &openaiProvider{id: "lms", kind: LocalServer, baseURL: "http://127.0.0.1:1/v1", label: "lms"}
	st = local.Detect(context.Background())
	if st.Available || st.Reason != "not running (127.0.0.1:1)" || !strings.Contains(st.Detail, "not reachable at http://127.0.0.1:1/v1") {
		t.Errorf("local Detect() = %+v", st)
	}
}

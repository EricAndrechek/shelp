package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// openai: the OpenAI-compatible provider family. It covers two shapes of the
// same Chat Completions API:
//
//   - Hosted endpoints with a key, configured under [[openai.endpoint]] in
//     the TOML config, optionally via a `preset` that fills in base_url and
//     api_key_env for well-known hosts (openai, openrouter, groq, gemini,
//     anthropic, xai, mistral, deepseek, cerebras). One provider per
//     (endpoint, model): ID "<name>:<model>" (lowercased; the model sent to
//     the API keeps its original casing), Kind API.
//   - Local servers auto-discovered on well-known ports (LM Studio, Ollama,
//     llama.cpp's llama-server) plus any [[openai.local]] entries, e.g.
//     macOS 27's `fm serve`. Kind LocalServer.
//
// Providers() must stay cheap (config + one small file read; see
// CLAUDE.md), but a local server's model list only exists on the server. So
// Detect (only called by `shelp providers` and the benchmark) probes
// GET {base}/models and writes $XDG_CACHE_HOME/shelp/local-models.json;
// Providers() reads that cache to emit "<server>:<model>" candidates. A
// bare "<server>" candidate (no model) is always emitted too; its Generate
// resolves the model live (GET /models, first entry) so it keeps working
// even with a cold or stale cache — that live probe is also what makes a
// server that refuses the connection fail fast.
//
// Generate strips <think>...</think> reasoning blocks (Qwen3/DeepSeek-style)
// from the text it returns, including mid-stream, and ignores any
// reasoning_content delta field entirely (it's just never read).

type openaiConfig struct {
	Endpoint []endpointConfig `toml:"endpoint"`
	Local    []localConfig    `toml:"local"`
}

type endpointConfig struct {
	Name        string   `toml:"name"`
	Preset      string   `toml:"preset"`
	BaseURL     string   `toml:"base_url"`
	APIKeyEnv   string   `toml:"api_key_env"`
	Models      []string `toml:"models"`
	Temperature float64  `toml:"temperature"` // optional, default 0
}

type localConfig struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
}

// presetInfo is what a preset fills in when the config leaves base_url or
// api_key_env blank. Base URLs looked up from each provider's own docs
// (2026-09-30), not guessed; see MERGE NOTES for sources.
type presetInfo struct{ BaseURL, APIKeyEnv string }

var presets = map[string]presetInfo{
	"openai":     {"https://api.openai.com/v1", "OPENAI_API_KEY"},
	"openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	"groq":       {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"gemini":     {"https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY"},
	"anthropic":  {"https://api.anthropic.com/v1/", "ANTHROPIC_API_KEY"},
	"xai":        {"https://api.x.ai/v1", "XAI_API_KEY"},
	"mistral":    {"https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	"deepseek":   {"https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	"cerebras":   {"https://api.cerebras.ai/v1", "CEREBRAS_API_KEY"},
}

// localServerDef is one known or configured local OpenAI-compatible server.
type localServerDef struct{ Name, BaseURL string }

func defaultLocalServers() []localServerDef {
	return []localServerDef{
		{"lmstudio", "http://127.0.0.1:1234/v1"},
		{"ollama", "http://127.0.0.1:11434/v1"},
		{"llamacpp", "http://127.0.0.1:8080/v1"},
	}
}

// mergedLocalServers combines the known servers with [[openai.local]]
// entries; a config entry with a name matching a known server overrides its
// base_url (e.g. a non-default port), otherwise it adds a new server (e.g.
// fm-serve).
func mergedLocalServers(cfgLocal []localConfig) []localServerDef {
	servers := defaultLocalServers()
	idx := map[string]int{}
	for i, s := range servers {
		idx[s.Name] = i
	}
	for _, l := range cfgLocal {
		name := strings.ToLower(strings.TrimSpace(l.Name))
		base := strings.TrimSpace(l.BaseURL)
		if name == "" || base == "" {
			continue
		}
		if i, ok := idx[name]; ok {
			servers[i].BaseURL = base
		} else {
			servers = append(servers, localServerDef{Name: name, BaseURL: base})
			idx[name] = len(servers) - 1
		}
	}
	return servers
}

type openaiFamily struct{}

func init() { Register(openaiFamily{}) }

func (openaiFamily) Name() string { return "openai" }

func (openaiFamily) Providers(cfg *config.Config) []Provider {
	var v openaiConfig
	_ = cfg.Section("openai", &v) // malformed [openai] just yields no providers from it

	var out []Provider
	for _, ep := range v.Endpoint {
		out = append(out, buildEndpointProviders(ep)...)
	}

	cache := cacheByName(readLocalModelCache())
	for _, s := range mergedLocalServers(v.Local) {
		out = append(out, buildLocalProviders(s, cache)...)
	}
	return out
}

func buildEndpointProviders(ep endpointConfig) []Provider {
	name := strings.ToLower(strings.TrimSpace(ep.Name))
	presetName := strings.ToLower(strings.TrimSpace(ep.Preset))
	if name == "" {
		name = presetName
	}
	if name == "" {
		return nil // no way to derive an ID
	}
	baseURL := strings.TrimSpace(ep.BaseURL)
	keyEnv := strings.TrimSpace(ep.APIKeyEnv)
	if pr, ok := presets[presetName]; ok {
		if baseURL == "" {
			baseURL = pr.BaseURL
		}
		if keyEnv == "" {
			keyEnv = pr.APIKeyEnv
		}
	}
	if baseURL == "" {
		return nil // nothing to call
	}
	var out []Provider
	for _, m := range ep.Models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		out = append(out, &openaiProvider{
			id:          strings.ToLower(name + ":" + m),
			kind:        API,
			baseURL:     baseURL,
			apiKeyEnv:   keyEnv,
			model:       m,
			label:       name,
			temperature: ep.Temperature,
		})
	}
	return out
}

func buildLocalProviders(s localServerDef, cache map[string]cacheEntry) []Provider {
	out := []Provider{&openaiProvider{
		id:      s.Name,
		kind:    LocalServer,
		baseURL: s.BaseURL,
		label:   s.Name,
	}}
	if e, ok := cache[s.Name]; ok {
		for _, m := range e.Models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			out = append(out, &openaiProvider{
				id:      strings.ToLower(s.Name + ":" + m),
				kind:    LocalServer,
				baseURL: s.BaseURL,
				model:   m,
				label:   s.Name,
			})
		}
	}
	return out
}

// openaiProvider is one callable OpenAI-compatible route: a hosted
// (endpoint, model) pair, or a local server (optionally with a model
// already pinned from the cache).
type openaiProvider struct {
	id          string
	kind        Kind
	baseURL     string
	apiKeyEnv   string // "" when no auth is needed (local servers, keyless endpoints)
	model       string // "" only for a local server's bare, auto-selecting candidate
	label       string // endpoint name or local server name, for Description()
	temperature float64
}

func (p *openaiProvider) ID() string { return p.id }
func (p *openaiProvider) Kind() Kind { return p.kind }

func (p *openaiProvider) Description() string {
	host := hostOf(p.baseURL)
	switch {
	case p.kind == API:
		return fmt.Sprintf("%s endpoint (%s), model %s", p.label, host, p.model)
	case p.model == "":
		return fmt.Sprintf("%s local server (%s), auto-selects the first loaded model", p.label, host)
	default:
		return fmt.Sprintf("%s local server (%s), model %s", p.label, host, p.model)
	}
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func (p *openaiProvider) Detect(ctx context.Context) Status {
	if p.kind == API {
		if p.apiKeyEnv == "" {
			return Status{Available: true}
		}
		if os.Getenv(p.apiKeyEnv) == "" {
			return Status{Reason: fmt.Sprintf("set $%s to use %s", p.apiKeyEnv, p.id)}
		}
		return Status{Available: true}
	}
	models, err := listModels(ctx, localClient, p.baseURL)
	if err != nil {
		return Status{Reason: describeLocalError(p.baseURL, err)}
	}
	_ = writeLocalModelCache(p.label, p.baseURL, models)
	v := ""
	if len(models) > 0 {
		v = models[0]
	}
	return Status{Available: true, Version: v}
}

// localDialTimeout bounds how long we wait to even open a TCP connection to
// a local server. A refused connection on localhost returns almost
// instantly; this mostly guards against a hung or firewalled port.
const localDialTimeout = 300 * time.Millisecond

var (
	hostedClient = &http.Client{}
	localClient  = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: localDialTimeout}).DialContext,
		},
	}
)

func (p *openaiProvider) Generate(ctx context.Context, req Request) (string, error) {
	client := hostedClient
	if p.kind == LocalServer {
		client = localClient
	}

	var key string
	if p.apiKeyEnv != "" {
		key = os.Getenv(p.apiKeyEnv)
		if key == "" {
			return "", Unavailable(p.id, "set $%s to use %s", p.apiKeyEnv, p.id)
		}
	}

	model := p.model
	if model == "" {
		models, err := listModels(ctx, client, p.baseURL)
		if err != nil {
			return "", unavailableFromRequestError(p.id, p.baseURL, err)
		}
		if len(models) == 0 {
			return "", Unavailable(p.id, "%s is running but reports no loaded models", p.baseURL)
		}
		model = models[0]
		_ = writeLocalModelCache(p.label, p.baseURL, models)
	}

	maxTokens := 256 // suggest: a shell command is short
	if req.Mode == Explain {
		maxTokens = 512 // explain: room for a couple of sentences
	}

	payload, err := json.Marshal(chatRequest{
		Model: model,
		Messages: []chatMsg{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.Prompt},
		},
		Temperature: p.temperature,
		MaxTokens:   maxTokens,
		Stream:      req.OnChunk != nil,
	})
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", p.id, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(p.baseURL, "/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("%s: build request: %w", p.id, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", unavailableFromRequestError(p.id, p.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", mapHTTPError(p.id, model, resp)
	}

	if req.OnChunk != nil {
		text, err := readStream(resp.Body, req.OnChunk)
		if err != nil {
			return text, fmt.Errorf("%s: %w", p.id, err)
		}
		return text, nil
	}
	return readNonStream(resp.Body, p.id)
}

func describeLocalError(baseURL string, err error) string {
	return fmt.Sprintf("not reachable at %s: %v", baseURL, err)
}

// unavailableFromRequestError maps any failure to reach the server at all
// (connection refused, DNS failure, TLS failure, timeout) to Unavailable so
// the chain falls through to the next provider, on both local servers and
// hosted endpoints.
func unavailableFromRequestError(id, baseURL string, err error) error {
	return Unavailable(id, "could not reach %s: %v", baseURL, err)
}

func mapHTTPError(id, model string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := extractErrorMessage(body)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return Unavailable(id, "authentication failed (%d): check the API key (%s)", resp.StatusCode, msg)
	case http.StatusTooManyRequests:
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return RateLimited(id, retryAfter, "rate limited (429): %s", msg)
	case http.StatusNotFound:
		return Unavailable(id, "model %q not found: is it loaded/served? (%s)", model, msg)
	default:
		return fmt.Errorf("%s: unexpected status %d: %s", id, resp.StatusCode, excerpt(body))
	}
}

// parseRetryAfter parses an HTTP Retry-After header value, which the spec
// allows as either an integer number of seconds or an HTTP-date. Returns 0
// (no override; the caller applies its own default cooldown) when the
// header is absent, empty, negative, or unparseable in either form.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func extractErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return excerpt(body)
}

func excerpt(body []byte) string {
	s := strings.TrimSpace(string(body))
	const max = 200
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

func joinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// --- Chat Completions wire types -------------------------------------------

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// reasoning_content (DeepSeek/Qwen3-style) is intentionally not
			// decoded here: we only ever return Content.
		} `json:"message"`
	} `json:"choices"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// reasoning_content intentionally omitted; see chatResponse.
		} `json:"delta"`
	} `json:"choices"`
}

func readNonStream(body io.Reader, id string) (string, error) {
	var cr chatResponse
	if err := json.NewDecoder(body).Decode(&cr); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", id, err)
	}
	if len(cr.Choices) == 0 {
		return "", Unavailable(id, "empty response (no choices)")
	}
	return stripThink(cr.Choices[0].Message.Content), nil
}

func readStream(body io.Reader, onChunk func(string)) (string, error) {
	var full strings.Builder
	filter := newThinkFilter(func(s string) {
		if s == "" {
			return
		}
		full.WriteString(s)
		onChunk(s)
	})
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // ignore malformed SSE data lines
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				filter.Write(c.Delta.Content)
			}
		}
	}
	filter.Close()
	if err := scanner.Err(); err != nil {
		return full.String(), err
	}
	return full.String(), nil
}

// --- /models -----------------------------------------------------------

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func listModels(ctx context.Context, client *http.Client, baseURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(baseURL, "/models"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /models: status %d", resp.StatusCode)
	}
	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("GET /models: %w", err)
	}
	ids := make([]string, 0, len(mr.Data))
	for _, d := range mr.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

// --- local model cache ---------------------------------------------------
//
// $XDG_CACHE_HOME/shelp/local-models.json (default ~/.cache/shelp/), a JSON
// array so multiple servers share one file. Written by Detect (and, best
// effort, by Generate's live probe for the model-less candidate); read by
// Providers(), which must not do any network I/O of its own.

type cacheEntry struct {
	Name     string    `json:"name"`
	BaseURL  string    `json:"base_url"`
	Models   []string  `json:"models"`
	ProbedAt time.Time `json:"probed_at"`
}

func cachePath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "shelp", "local-models.json")
}

func readLocalModelCache() []cacheEntry {
	b, err := os.ReadFile(cachePath())
	if err != nil {
		return nil
	}
	var entries []cacheEntry
	if json.Unmarshal(b, &entries) != nil {
		return nil
	}
	return entries
}

func cacheByName(entries []cacheEntry) map[string]cacheEntry {
	m := make(map[string]cacheEntry, len(entries))
	for _, e := range entries {
		m[e.Name] = e
	}
	return m
}

func writeLocalModelCache(name, baseURL string, models []string) error {
	path := cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	entries := readLocalModelCache()
	entry := cacheEntry{Name: name, BaseURL: baseURL, Models: models, ProbedAt: time.Now().UTC()}
	found := false
	for i := range entries {
		if entries[i].Name == name {
			entries[i] = entry
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, entry)
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- <think> stripping ---------------------------------------------------

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// stripThink removes every <think>...</think> block from a complete string.
func stripThink(s string) string {
	var out strings.Builder
	f := newThinkFilter(func(chunk string) { out.WriteString(chunk) })
	f.Write(s)
	f.Close()
	return out.String()
}

// thinkFilter strips <think>...</think> reasoning blocks from a stream of
// text fragments, forwarding only non-think text to sink. It tolerates the
// tags being split across fragment boundaries by holding back a suffix that
// could still be the start of a tag until either it is resolved or Close
// forces a final flush.
type thinkFilter struct {
	buf     strings.Builder
	inThink bool
	sink    func(string)
}

func newThinkFilter(sink func(string)) *thinkFilter { return &thinkFilter{sink: sink} }

func (f *thinkFilter) Write(s string) {
	f.buf.WriteString(s)
	f.process(false)
}

// Close flushes any buffered text that turned out not to be a tag prefix.
// Text still inside an unterminated think block is dropped, not forwarded.
func (f *thinkFilter) Close() { f.process(true) }

func (f *thinkFilter) process(final bool) {
	for {
		text := f.buf.String()
		tag := thinkOpen
		if f.inThink {
			tag = thinkClose
		}
		idx := strings.Index(text, tag)
		if idx == -1 {
			keep := 0
			if !final {
				keep = partialSuffixLen(text, tag)
			}
			if !f.inThink {
				if emit := text[:len(text)-keep]; emit != "" {
					f.sink(emit)
				}
			}
			f.buf.Reset()
			f.buf.WriteString(text[len(text)-keep:])
			return
		}
		if !f.inThink {
			if idx > 0 {
				f.sink(text[:idx])
			}
			f.inThink = true
		} else {
			f.inThink = false
		}
		f.buf.Reset()
		f.buf.WriteString(text[idx+len(tag):])
	}
}

// partialSuffixLen returns the length of the longest suffix of s that is
// also a proper prefix of tag, i.e. text that might continue into tag on
// the next Write.
func partialSuffixLen(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for l := max; l > 0; l-- {
		if strings.HasSuffix(s, tag[:l]) {
			return l
		}
	}
	return 0
}

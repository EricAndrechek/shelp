package provider

import (
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
)

func providersByID(t *testing.T, src string) map[string]Provider {
	t.Helper()
	setCacheHome(t)
	cfg, err := config.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Provider{}
	for _, p := range (openaiFamily{}).Providers(cfg) {
		out[p.ID()] = p
	}
	return out
}

func TestLabels_EndpointAndPerModel(t *testing.T) {
	ps := providersByID(t, `
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"
models = ["anthropic/claude-haiku-4.5"]
label = "Haiku via OpenRouter"

[[openai.endpoint]]
name = "work"
base_url = "https://llm.example.com/v1"
api_key_env = "WORK_KEY"
models = ["Big-Model", "small", "plain"]
label = "Work LLM"
labels = { "big-model" = "Work big", "small" = "Work small" }

[[openai.endpoint]]
name = "nolabel"
preset = "groq"
models = ["llama"]

[[openai.local]]
name = "fm-serve"
base_url = "http://127.0.0.1:11535/v1"
label = "fm serve"

[[openai.local]]
name = "lmstudio"
base_url = "http://127.0.0.1:1235/v1"
`)
	for id, want := range map[string]string{
		"openrouter:anthropic/claude-haiku-4.5": "Haiku via OpenRouter", // one model: the label as is
		"work:big-model":                        "Work big",             // labels, matched case-insensitively
		"work:small":                            "Work small",
		"work:plain":                            "Work LLM · plain", // several models: label · model
		"nolabel:llama":                         "llama",            // no label: the model, as before
		"fm-serve":                              "fm serve",
		"lmstudio":                              "first loaded model",
		"ollama":                                "first loaded model",
	} {
		p, ok := ps[id]
		if !ok {
			t.Errorf("no provider %s", id)
			continue
		}
		if got := LabelOf(p); got != want {
			t.Errorf("%s: label %q, want %q", id, got, want)
		}
	}
	r, _ := RouteOf(ps["work:plain"])
	if r.Local || r.Name != "work" || r.KeyEnv != "WORK_KEY" || r.Model != "plain" || !r.Configured || r.Builtin {
		t.Errorf("work route %+v", r)
	}
	r, _ = RouteOf(ps["openrouter:anthropic/claude-haiku-4.5"])
	if r.Preset != "openrouter" || r.KeyEnv != "OPENROUTER_API_KEY" || r.Label != "Haiku via OpenRouter" {
		t.Errorf("openrouter route %+v", r)
	}
	r, _ = RouteOf(ps["lmstudio"])
	if !r.Local || !r.Builtin || !r.Configured || r.BaseURL != "http://127.0.0.1:1235/v1" {
		t.Errorf("overridden lmstudio %+v", r)
	}
	r, _ = RouteOf(ps["ollama"])
	if !r.Builtin || r.Configured {
		t.Errorf("ollama %+v", r)
	}
	if _, ok := RouteOf(&fmProvider{}); ok {
		t.Error("fm has no route")
	}
}

func TestLabels_LocalModelsFromCache(t *testing.T) {
	setCacheHome(t)
	if err := writeLocalModelCache("box", "http://10.0.0.2:8080/v1", []string{"qwen"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Parse("[[openai.local]]\nname = \"box\"\nbase_url = \"http://10.0.0.2:8080/v1\"\nlabel = \"Mac Studio\"\n")
	for _, p := range (openaiFamily{}).Providers(cfg) {
		if p.ID() == "box:qwen" && LabelOf(p) != "Mac Studio · qwen" {
			t.Errorf("box:qwen label %q", LabelOf(p))
		}
	}
}

func TestCustomRouteBlockRoundTrips(t *testing.T) {
	for _, r := range []CustomRoute{
		{Name: "openrouter", Preset: "openrouter", Models: []string{"anthropic/claude-haiku-4.5"}, Label: `Haiku "via" OpenRouter`},
		{Name: "or2", Preset: "openrouter", KeyEnv: "MY_OR_KEY", Models: []string{"a/b", "c/d"}},
		{Name: "work", BaseURL: "https://llm.example.com/v1", KeyEnv: "WORK_KEY", Models: []string{"Big-Model"}},
		{Name: "lan", BaseURL: "http://10.0.0.5/v1", Models: []string{"m"}}, // keyless
		{Local: true, Name: "fm-serve", BaseURL: "http://127.0.0.1:11535/v1", Label: "fm serve"},
	} {
		block := r.Block()
		cfg, err := config.Parse(block)
		if err != nil {
			t.Fatalf("%s: block does not parse: %v\n%s", r.Name, err, block)
		}
		got := ConfiguredRoutes(cfg)
		if len(got) != 1 || got[0].Name != r.Name || got[0].Label != r.Label || got[0].Local != r.Local {
			t.Errorf("%s: decoded %+v from\n%s", r.Name, got, block)
		}
		ps := map[string]Provider{}
		for _, p := range (openaiFamily{}).Providers(cfg) {
			ps[p.ID()] = p
		}
		for _, id := range r.IDs() {
			p, ok := ps[id]
			if !ok {
				t.Errorf("%s: block yields no provider %s:\n%s", r.Name, id, block)
				continue
			}
			rt, _ := RouteOf(p)
			if rt.KeyEnv != r.EffectiveKeyEnv() || rt.BaseURL != r.EffectiveBaseURL() {
				t.Errorf("%s: route %+v, want key %q url %q", id, rt, r.EffectiveKeyEnv(), r.EffectiveBaseURL())
			}
		}
	}
	// Only what the preset doesn't fill in is written.
	b := (CustomRoute{Name: "openrouter", Preset: "openrouter", KeyEnv: "OPENROUTER_API_KEY", BaseURL: "https://openrouter.ai/api/v1", Models: []string{"x"}}).Block()
	if strings.Contains(b, "base_url") || strings.Contains(b, "api_key_env") {
		t.Errorf("preset defaults written:\n%s", b)
	}
}

func TestCustomRouteValidate(t *testing.T) {
	cfg, err := config.Parse(`
[[openai.endpoint]]
name = "work"
base_url = "https://llm.example.com/v1"
models = ["m"]

[[openai.local]]
name = "box"
base_url = "http://10.0.0.2/v1"
`)
	if err != nil {
		t.Fatal(err)
	}
	setCacheHome(t)
	ok := []CustomRoute{
		{Name: "openrouter", Preset: "openrouter", Models: []string{"anthropic/claude-haiku-4.5"}},
		{Name: "lan", BaseURL: "http://10.0.0.5:8000/v1", KeyEnv: "LAN_KEY", Models: []string{"a", "b"}},
		{Local: true, Name: "lmstudio", BaseURL: "http://127.0.0.1:1235/v1"}, // overrides the built-in
		{Local: true, Name: "fm-serve", BaseURL: "http://127.0.0.1:11535/v1", Label: "fm serve"},
	}
	for _, r := range ok {
		if err := r.Validate(cfg); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := map[string]CustomRoute{
		"already":                  {Name: "work", Preset: "groq", Models: []string{"x"}},
		"already in the config":    {Local: true, Name: "box", BaseURL: "http://x/v1"},
		"taken by shelp":           {Name: "claude", Preset: "anthropic", Models: []string{"haiku"}},
		"built-in local":           {Name: "ollama", Preset: "groq", Models: []string{"x"}},
		"lowercase":                {Name: "Open Router", Preset: "openrouter", Models: []string{"x"}},
		"unknown preset":           {Name: "x", Preset: "nosuch", Models: []string{"x"}},
		"base URL is required":     {Name: "x", Models: []string{"x"}},
		"not an http(s) URL":       {Name: "x", BaseURL: "ftp://h/v1", Models: []string{"x"}},
		"no credentials":           {Name: "x", BaseURL: "https://u:p@h/v1", Models: []string{"x"}},
		"at least one model":       {Name: "x", Preset: "groq"},
		"no spaces, commas":        {Name: "x", Preset: "groq", Models: []string{"a,b"}},
		"listed twice":             {Name: "x", Preset: "groq", Models: []string{"a", "A"}},
		"one line":                 {Name: "x", Preset: "groq", Models: []string{"a"}, Label: "two\nlines"},
		"not an environment":       {Name: "x", Preset: "groq", Models: []string{"a"}, KeyEnv: "MY-KEY"},
		"takes a name, a base URL": {Local: true, Name: "y", BaseURL: "http://h/v1", Models: []string{"m"}},
	}
	for want, r := range bad {
		err := r.Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: got %v, want an error containing %q", r, err, want)
		}
	}
}

// A key pasted where the variable's name goes is refused, and never
// repeated in the error.
func TestValidateKeyEnvRefusesKeys(t *testing.T) {
	for _, s := range []string{
		"sk-proj-abcdefghijklmnop", "gsk_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ",
		"AIzaSyD-abcdefg", "xai-abc", "aB3dEfGhIjKlMnOpQrStUv", strings.Repeat("A", 60),
	} {
		err := ValidateKeyEnv(s)
		if err == nil || !strings.Contains(err.Error(), "looks like an API key") {
			t.Errorf("%q: %v", s, err)
		} else if strings.Contains(err.Error(), s) {
			t.Errorf("error repeats the value: %v", err)
		}
	}
	for _, s := range []string{"OPENROUTER_API_KEY", "WORK_LLM_API_KEY", "my_key", "_X", "GEMINI_API_KEY_2"} {
		if err := ValidateKeyEnv(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestPresetsAndDefaults(t *testing.T) {
	if len(PresetNames()) != len(presets) {
		t.Errorf("presetOrder %v does not cover presets", PresetNames())
	}
	for _, n := range PresetNames() {
		if b, k, ok := PresetDefaults(n); !ok || b == "" || k == "" {
			t.Errorf("preset %s: %q %q %v", n, b, k, ok)
		}
	}
	for in, want := range map[string]string{"work": "WORK_API_KEY", "my-llm.box": "MY_LLM_BOX_API_KEY", "9x": "SHELP_9X_API_KEY"} {
		if got := DefaultKeyEnv(in); got != want {
			t.Errorf("DefaultKeyEnv(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsBuiltinLocal("ollama") || IsBuiltinLocal("fm-serve") || BuiltinLocalURL("lmstudio") != "http://127.0.0.1:1234/v1" {
		t.Error("built-in local servers")
	}
}

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/provider"
)

// routeEnv isolates like isolateCfg and lists the real openai providers of
// the config too, so added routes show up as providers.
func routeEnv(t *testing.T, stdinText string, interactive bool) env {
	t.Helper()
	e := isolateCfg(t, stdinText, interactive)
	t.Setenv("SHELP_PROVIDER", "nosuch:model") // belt and braces: never a real backend
	fakes := listProviders(nil)
	listProviders = func(cfg *config.Config) []provider.Provider {
		ps := append([]provider.Provider{}, fakes...)
		for _, p := range provider.All(cfg) {
			if r, ok := provider.RouteOf(p); ok && r.Configured {
				ps = append(ps, p)
			}
		}
		return ps
	}
	return e
}

const userCfg = `# my shelp config
template = "v2"   # keep me

[shell]
bind = "^G"
`

func TestConfigAddEndpoint(t *testing.T) {
	e := routeEnv(t, "", false)
	write(t, e.cfgPath, userCfg)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-very-secret")
	out, errOut, err := runOut(t, "config", "add", "endpoint", "openrouter", "--preset", "openrouter",
		"--model", "anthropic/claude-haiku-4.5", "--label", "Haiku via OpenRouter")
	t.Logf("stdout:\n%s", out)
	if err != nil {
		t.Fatalf("%v (stderr %q)", err, errOut)
	}
	want := userCfg + `
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"
models = ["anthropic/claude-haiku-4.5"]
label = "Haiku via OpenRouter"
`
	if got := read(t, e.cfgPath); got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	for _, w := range []string{"added openrouter", "openrouter:anthropic/claude-haiku-4.5  cloud  Haiku via OpenRouter", "$OPENROUTER_API_KEY is set in this shell",
		"use it: shelp config chain shortcuts:apple-pro,fm,openrouter:anthropic/claude-haiku-4.5"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
	if strings.Contains(out+errOut, "sk-or-very-secret") {
		t.Error("the key's value was printed")
	}
	// The label shows in `shelp config` too.
	show, _, _ := runOut(t, "config")
	if !strings.Contains(show, "routes") || !strings.Contains(show, `openrouter "Haiku via OpenRouter"`) || !strings.Contains(show, "shelp providers -i") {
		t.Errorf("config:\n%s", show)
	}
	// A second one is appended after it; the same name is refused.
	if _, _, err := runOut(t, "config", "add", "endpoint", "work", "--base-url", "https://llm.example.com/v1", "--model", "big,small"); err != nil {
		t.Fatal(err)
	}
	got := read(t, e.cfgPath)
	if !strings.HasSuffix(got, "[[openai.endpoint]]\nname = \"work\"\nbase_url = \"https://llm.example.com/v1\"\napi_key_env = \"WORK_API_KEY\"\nmodels = [\"big\", \"small\"]\n") {
		t.Errorf("custom endpoint (default key variable):\n%s", got)
	}
	_, _, err = runOut(t, "config", "add", "endpoint", "work", "--preset", "groq", "--model", "x")
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "already in the config") {
		t.Errorf("duplicate: %v", err)
	}
}

func TestConfigAddEndpointUnsetKeySaysWhere(t *testing.T) {
	routeEnv(t, "", false)
	t.Setenv("GROQ_API_KEY", "")
	out, _, err := runOut(t, "config", "add", "endpoint", "groq", "--preset=groq", "--model=llama-3.3-70b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "$GROQ_API_KEY is not set") || !strings.Contains(out, "export GROQ_API_KEY=…") {
		t.Errorf("output:\n%s", out)
	}
}

func TestConfigAddLocal(t *testing.T) {
	e := routeEnv(t, "", false)
	out, _, err := runOut(t, "config", "add", "local", "fm-serve", "--base-url", "http://127.0.0.1:11535/v1", "--label", "fm serve")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.cfgPath); got != "[[openai.local]]\nname = \"fm-serve\"\nbase_url = \"http://127.0.0.1:11535/v1\"\nlabel = \"fm serve\"\n" {
		t.Errorf("file %q", got)
	}
	if !strings.Contains(out, "fm-serve  local  fm serve") || !strings.Contains(out, "use it: shelp config chain shortcuts:apple-pro,fm,fm-serve") {
		t.Errorf("output:\n%s", out)
	}
	// A built-in's name overrides it.
	out, _, err = runOut(t, "config", "add", "local", "lmstudio", "--base-url", "http://127.0.0.1:1235/v1")
	if err != nil || !strings.Contains(out, "replaces the built-in lmstudio") {
		t.Errorf("override: %v\n%s", err, out)
	}
}

func TestConfigAddErrors(t *testing.T) {
	e := routeEnv(t, "", false)
	for want, args := range map[string][]string{
		"needs a kind":           {"config", "add", "endpoint"},
		"unknown preset":         {"config", "add", "endpoint", "x", "--preset", "nosuch", "--model", "m"},
		"at least one model":     {"config", "add", "endpoint", "x", "--preset", "groq"},
		"base URL is required":   {"config", "add", "endpoint", "x", "--model", "m"},
		"--model needs a value":  {"config", "add", "endpoint", "x", "--model"},
		"unknown flag --bogus":   {"config", "add", "endpoint", "x", "--bogus"},
		"takes --base-url and":   {"config", "add", "local", "x", "--base-url", "http://h/v1", "--model", "m"},
		"taken by shelp's":       {"config", "add", "endpoint", "claude", "--preset", "anthropic", "--model", "haiku"},
		"never takes an API key": {"config", "add", "endpoint", "x", "--preset", "groq", "--model", "m", "--api-key=gsk_supersecretvalue"},
		"looks like an API key":  {"config", "add", "endpoint", "x", "--preset", "groq", "--model", "m", "--key-env", "gsk_supersecretvalue0123456789abcdefABCDEF"},
	} {
		out, errOut, err := runOut(t, args...)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want a usage error with %q", args, err, want)
			continue
		}
		if strings.Contains(out+errOut+err.Error(), "supersecret") {
			t.Errorf("%v: a key was repeated back", args)
		}
	}
	// `config add remote ...` is a task (add claims endpoint and local
	// only), so parseAddArgs's own check is tested directly.
	if _, _, err := parseAddArgs([]string{"remote", "x"}); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("unknown kind: %v", err)
	}
	if _, serr := os.Stat(e.cfgPath); serr == nil {
		t.Error("a failed add wrote the file")
	}
	// -h shows config's help.
	if out, _, err := runOut(t, "config", "add", "-h"); err != nil || out != commandHelp["config"] {
		t.Errorf("-h: %v", err)
	}
}

// On a terminal, add offers to put the new providers in the chain, and
// does both in one write.
func TestConfigAddInteractiveChain(t *testing.T) {
	e := routeEnv(t, "\n", true)
	write(t, e.cfgPath, "providers = [\"fm\"]\n")
	out, _, err := runOut(t, "config", "add", "endpoint", "openrouter", "--preset", "openrouter", "--model", "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Also put openrouter:a/b at the end of your chain? [Y/n]") || strings.Contains(out, "use it:") {
		t.Errorf("output:\n%s", out)
	}
	got := read(t, e.cfgPath)
	if !strings.HasPrefix(got, "providers = [\"fm\", \"openrouter:a/b\"]\n") || !strings.Contains(got, "[[openai.endpoint]]") {
		t.Errorf("file:\n%s", got)
	}
}

func TestConfigRemove(t *testing.T) {
	e := routeEnv(t, "", false)
	src := `providers = ["fm", "work:big", "box"]

# the work gateway
[[openai.endpoint]]
name = "work"
base_url = "https://llm.example.com/v1"
models = ["big"]

[[openai.local]]
name = "box"
base_url = "http://10.0.0.2/v1"

[shell]
bind = "^G"
`
	write(t, e.cfgPath, src)
	out, _, err := runOut(t, "config", "remove", "work")
	if err != nil {
		t.Fatal(err)
	}
	want := `providers = ["fm", "box"]

[[openai.local]]
name = "box"
base_url = "http://10.0.0.2/v1"

[shell]
bind = "^G"
`
	if got := read(t, e.cfgPath); got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(out, "removed work") || !strings.Contains(out, "also taken out of your chain: work:big") {
		t.Errorf("output:\n%s", out)
	}
	for want, args := range map[string][]string{
		"no endpoint or local server named \"nosuch\"": {"config", "remove", "nosuch"},
		"the config has: box":                          {"config", "remove", "nosuch"},
		"built-in local server":                        {"config", "remove", "ollama"},
		"usage: shelp config remove NAME":              {"config", "remove"},
	} {
		if _, _, err := runOut(t, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, want %q", args, err, want)
		}
	}
}

// An edit shelp's line edits cannot make safely is refused, file untouched.
func TestConfigAddRefusesUnsafeEdit(t *testing.T) {
	e := routeEnv(t, "", false)
	src := "[openai]\nendpoint = [{ name = \"x\", preset = \"groq\", models = [\"m\"] }]\n"
	write(t, e.cfgPath, src)
	_, _, err := runOut(t, "config", "add", "endpoint", "y", "--preset", "groq", "--model", "m")
	if err == nil || !strings.Contains(err.Error(), "shelp config edit") {
		t.Errorf("got %v", err)
	}
	if read(t, e.cfgPath) != src {
		t.Error("file changed")
	}
}

func TestConfigAddChezmoiManaged(t *testing.T) {
	e := routeEnv(t, "", false)
	write(t, e.cfgPath, userCfg)
	fakeChezmoi(t)
	out, _, err := runOut(t, "config", "add", "endpoint", "openrouter", "--preset", "openrouter", "--model", "a/b")
	t.Logf("managed add:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"managed by chezmoi, so shelp will not write it", "chezmoi edit ~/.config/shelp/config.toml",
		"add this entry at the end:", "    [[openai.endpoint]]", `    name = "openrouter"`, "chezmoi apply ~/.config/shelp/config.toml", "use it: shelp config chain"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q", w)
		}
	}
	if read(t, e.cfgPath) != userCfg {
		t.Error("managed file written")
	}
}

// chezmoiFakes points the chezmoi seams at a temp source file.
func chezmoiFakes(t *testing.T, sourceName, content string) (source string, applied *[]string) {
	t.Helper()
	fakeChezmoi(t) // so fileState reports "managed by chezmoi"
	dir := t.TempDir()
	source = filepath.Join(dir, sourceName)
	write(t, source, content)
	var calls []string
	oldSP, oldAp := chezmoiSourcePath, chezmoiApply
	chezmoiSourcePath = func(context.Context, string) (string, error) { return source, nil }
	chezmoiApply = func(_ context.Context, target string) error { calls = append(calls, target); return nil }
	t.Cleanup(func() { chezmoiSourcePath, chezmoiApply = oldSP, oldAp })
	return source, &calls
}

func TestChezmoiOfferApplies(t *testing.T) {
	e := isolateCfg(t, "y\n", true)
	write(t, e.cfgPath, userCfg)
	source, applied := chezmoiFakes(t, "config.toml", userCfg)
	out, _, err := runOut(t, "config", "chain", "fm", "claude:haiku")
	t.Logf("offer:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "is a plain file. Make the change there and run `chezmoi apply ~/.config/shelp/config.toml`? [y/N]") {
		t.Errorf("no offer:\n%s", out)
	}
	if got := read(t, source); !strings.Contains(got, `providers = ["fm", "claude:haiku"]`) || !strings.Contains(got, "# keep me") {
		t.Errorf("source:\n%s", got)
	}
	if len(*applied) != 1 || (*applied)[0] != e.cfgPath {
		t.Errorf("chezmoi apply calls %v", *applied)
	}
	if read(t, e.cfgPath) != userCfg {
		t.Error("the target was written directly")
	}
}

func TestChezmoiOfferDefaultsToNo(t *testing.T) {
	e := isolateCfg(t, "\n", true)
	write(t, e.cfgPath, userCfg)
	source, applied := chezmoiFakes(t, "config.toml", userCfg)
	if _, _, err := runOut(t, "config", "chain", "fm"); err != nil {
		t.Fatal(err)
	}
	if read(t, source) != userCfg || len(*applied) != 0 {
		t.Error("Enter took the offer")
	}
}

func TestChezmoiNoOfferForTemplatesOrWithoutTerminal(t *testing.T) {
	for _, c := range []struct {
		name        string
		interactive bool
		want        string
	}{
		{"config.toml.tmpl", true, "its chezmoi source is a template"},
		{"encrypted_config.toml.age", true, "its chezmoi source is encrypted"},
		{"modify_config.toml", true, "a chezmoi modify file"},
		{"config.toml", false, "chezmoi apply ~/.config/shelp/config.toml"},
	} {
		e := isolateCfg(t, "y\n", c.interactive)
		write(t, e.cfgPath, userCfg)
		source, applied := chezmoiFakes(t, c.name, userCfg)
		out, _, err := runOut(t, "config", "chain", "fm")
		if err != nil || !strings.Contains(out, c.want) || strings.Contains(out, "[y/N]") {
			t.Errorf("%s: %v\n%s", c.name, err, out)
		}
		if read(t, source) != userCfg || len(*applied) != 0 {
			t.Errorf("%s: source changed or applied", c.name)
		}
	}
}

func TestChainGuideWithoutTerminal(t *testing.T) {
	isolateCfg(t, "", false)
	out, _, err := runOut(t, "config", "chain")
	t.Logf("guide:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"chain  shortcuts:apple-pro → fm  (default)", "fm                   local  Apple on-device",
		"shortcuts:apple-pro  cloud  Apple Intelligence Pro", "shelp config chain ID,ID", "shelp config add endpoint NAME --preset P --model M",
		"shelp config add local NAME --base-url URL", "shelp config remove NAME", "shelp config edit"} {
		if !strings.Contains(out, w) {
			t.Errorf("guide lacks %q", w)
		}
	}
	// Local kinds come first.
	list := out[strings.Index(out, "providers  ("):]
	if strings.Index(list, "  fm ") > strings.Index(list, "  shortcuts:apple-pro") {
		t.Error("cloud listed before local")
	}
	// providers -i without a terminal: a usage error pointing at the same commands.
	_, _, err = runOut(t, "providers", "-i")
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "needs a terminal") {
		t.Errorf("providers -i: %v", err)
	}
}

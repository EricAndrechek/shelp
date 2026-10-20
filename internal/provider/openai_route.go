package provider

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/ericandrechek/shelp/internal/config"
)

// This file is the user-facing side of the openai family's config: what a
// provider manager needs to show a route ([[openai.endpoint]] /
// [[openai.local]]) and to add or remove one. Keys never pass through here:
// a route names the environment variable that holds its key, nothing more.

// Route describes an OpenAI-compatible provider's route.
type Route struct {
	Local      bool   // a local server ([[openai.local]] or built in), else a hosted [[openai.endpoint]]
	Name       string // the endpoint's or server's name: the provider ID's prefix
	Preset     string // the endpoint's preset, "" if none
	BaseURL    string
	KeyEnv     string // the variable holding the key; "" when none is needed
	Model      string // "" for a local server's bare provider
	Label      string // the config's label for this provider, "" if none
	Configured bool   // from a config entry: `shelp config remove NAME` removes it
	Builtin    bool   // a built-in local server (lmstudio, ollama, llamacpp)
}

// RouteOf returns p's route when p belongs to the openai family.
func RouteOf(p Provider) (Route, bool) {
	o, ok := p.(*openaiProvider)
	if !ok {
		return Route{}, false
	}
	return Route{
		Local: o.kind == LocalServer, Name: o.label, Preset: o.preset, BaseURL: o.baseURL,
		KeyEnv: o.apiKeyEnv, Model: o.model, Label: o.userLabel,
		Configured: o.configured, Builtin: o.builtin,
	}, true
}

// presetOrder is the order presets are offered in.
var presetOrder = []string{"openai", "openrouter", "groq", "gemini", "anthropic", "xai", "mistral", "deepseek", "cerebras"}

// PresetNames lists the hosted-endpoint presets in display order.
func PresetNames() []string { return append([]string(nil), presetOrder...) }

// PresetDefaults returns the base URL and key variable a preset fills in.
func PresetDefaults(name string) (baseURL, keyEnv string, ok bool) {
	p, ok := presets[strings.ToLower(strings.TrimSpace(name))]
	return p.BaseURL, p.APIKeyEnv, ok
}

// IsBuiltinLocal reports whether name is a built-in local server, which an
// [[openai.local]] entry of that name overrides.
func IsBuiltinLocal(name string) bool {
	for _, s := range defaultLocalServers() {
		if s.Name == name {
			return true
		}
	}
	return false
}

// BuiltinLocalURL is a built-in local server's default base URL.
func BuiltinLocalURL(name string) string {
	for _, s := range defaultLocalServers() {
		if s.Name == name {
			return s.BaseURL
		}
	}
	return ""
}

// CustomRoute is an OpenAI-compatible route a user adds: a hosted endpoint
// with a key ([[openai.endpoint]]) or a local server ([[openai.local]]).
type CustomRoute struct {
	Local   bool
	Name    string
	Preset  string   // hosted only; "" for a custom base URL
	BaseURL string   // local, and hosted without a preset (or overriding it)
	KeyEnv  string   // hosted only: the variable holding the key; "" means the preset's, or none
	Models  []string // hosted only
	Label   string   // optional
}

// Table is the config table the route lives in.
func (r CustomRoute) Table() string {
	if r.Local {
		return "openai.local"
	}
	return "openai.endpoint"
}

// EffectiveKeyEnv is the variable the route reads its key from ("" for
// none): KeyEnv, else the preset's.
func (r CustomRoute) EffectiveKeyEnv() string {
	if r.Local {
		return ""
	}
	if r.KeyEnv != "" {
		return r.KeyEnv
	}
	_, k, _ := PresetDefaults(r.Preset)
	return k
}

// EffectiveBaseURL is BaseURL, else the preset's.
func (r CustomRoute) EffectiveBaseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	b, _, _ := PresetDefaults(r.Preset)
	return b
}

// IDs are the provider IDs the route adds: one per model for a hosted
// endpoint, the server's own for a local one (its models appear once
// `shelp providers` has probed it).
func (r CustomRoute) IDs() []string {
	if r.Local {
		return []string{strings.ToLower(r.Name)}
	}
	out := make([]string, 0, len(r.Models))
	for _, m := range r.Models {
		out = append(out, strings.ToLower(r.Name+":"+m))
	}
	return out
}

// Block renders the route as the TOML array-table entry `shelp config
// add` appends, with only the keys a preset does not already fill in.
func (r CustomRoute) Block() string {
	var b strings.Builder
	kv := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, config.Quote(v)) }
	b.WriteString("[[" + r.Table() + "]]\n")
	kv("name", r.Name)
	if r.Local {
		kv("base_url", r.BaseURL)
	} else {
		if r.Preset != "" {
			kv("preset", r.Preset)
		}
		pb, pk, _ := PresetDefaults(r.Preset)
		if r.BaseURL != "" && r.BaseURL != pb {
			kv("base_url", r.BaseURL)
		}
		if r.KeyEnv != "" && r.KeyEnv != pk {
			kv("api_key_env", r.KeyEnv)
		}
		q := make([]string, len(r.Models))
		for i, m := range r.Models {
			q[i] = config.Quote(m)
		}
		b.WriteString("models = [" + strings.Join(q, ", ") + "]\n")
	}
	if r.Label != "" {
		kv("label", r.Label)
	}
	return b.String()
}

// ConfiguredRoutes returns the [[openai.endpoint]] and [[openai.local]]
// entries in cfg, names lowercased (an endpoint without a name is named
// by its preset, as its provider IDs are).
func ConfiguredRoutes(cfg *config.Config) []CustomRoute {
	var v openaiConfig
	_ = cfg.Section("openai", &v)
	var out []CustomRoute
	for _, ep := range v.Endpoint {
		name := strings.ToLower(strings.TrimSpace(ep.Name))
		preset := strings.ToLower(strings.TrimSpace(ep.Preset))
		if name == "" {
			name = preset
		}
		out = append(out, CustomRoute{Name: name, Preset: preset, BaseURL: strings.TrimSpace(ep.BaseURL),
			KeyEnv: strings.TrimSpace(ep.APIKeyEnv), Models: ep.Models, Label: strings.TrimSpace(ep.Label)})
	}
	for _, l := range v.Local {
		out = append(out, CustomRoute{Local: true, Name: strings.ToLower(strings.TrimSpace(l.Name)),
			BaseURL: strings.TrimSpace(l.BaseURL), Label: strings.TrimSpace(l.Label)})
	}
	return out
}

// DefaultKeyEnv suggests a key variable for a custom endpoint: NAME_API_KEY.
func DefaultKeyEnv(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		s = "SHELP_" + s
	}
	return s + "_API_KEY"
}

// reservedRouteNames are provider families a route name must not shadow:
// "claude:haiku" must stay claude's even when claude is not installed.
var reservedRouteNames = []string{"fm", "shortcuts", "claude"}

var routeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// ValidateRouteName checks a route name's shape: lowercase letters,
// digits, '.', '_' and '-', at most 40 characters.
func ValidateRouteName(name string) error {
	if name == "" {
		return errors.New("a name is required")
	}
	if !routeNameRE.MatchString(name) {
		return fmt.Errorf("%q: use lowercase letters, digits, '.', '_' and '-' (it becomes the provider IDs' prefix)", name)
	}
	return nil
}

// CheckRouteName reports why name cannot be used for a new route in cfg:
// a bad shape, a reserved family, or a route of that name already there.
// A local server may take a built-in server's name: it overrides it.
func CheckRouteName(cfg *config.Config, name string, local bool) error {
	if err := ValidateRouteName(name); err != nil {
		return err
	}
	for _, r := range reservedRouteNames {
		if name == r {
			return fmt.Errorf("%q is taken by shelp's %s providers", name, r)
		}
	}
	for _, p := range All(cfg) {
		if _, ok := p.(*openaiProvider); ok {
			continue
		}
		fam, _, _ := strings.Cut(p.ID(), ":")
		if fam == name {
			return fmt.Errorf("%q is taken by the %s providers", name, fam)
		}
	}
	for _, r := range ConfiguredRoutes(cfg) {
		if r.Name == name {
			what := "an endpoint"
			if r.Local {
				what = "a local server"
			}
			return fmt.Errorf("%s named %q is already in the config (`shelp config remove %s` removes it)", what, name, name)
		}
	}
	if !local && IsBuiltinLocal(name) {
		return fmt.Errorf("%q is a built-in local server; pick another name", name)
	}
	return nil
}

// ValidateBaseURL checks an endpoint URL: http or https, with a host.
func ValidateBaseURL(s string) error {
	if s == "" {
		return errors.New("a base URL is required, e.g. https://llm.example.com/v1")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an http(s) URL, e.g. https://llm.example.com/v1", s)
	}
	if u.User != nil {
		return errors.New("put no credentials in the URL; name the variable that holds the key instead")
	}
	return nil
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ValidateKeyEnv checks the NAME of the variable holding an API key. It
// refuses anything that looks like a key itself, and never repeats the
// value back: shelp stores no keys, only where to find one.
func ValidateKeyEnv(s string) error {
	if LooksLikeSecret(s) {
		return errors.New("that looks like an API key itself, not the name of a variable: shelp never stores keys -- put `export NAME=<key>` in your shell rc or env file and give shelp NAME")
	}
	if !envNameRE.MatchString(s) {
		return errors.New("not an environment variable name: use letters, digits and _, e.g. OPENROUTER_API_KEY")
	}
	return nil
}

// LooksLikeSecret reports whether s looks like an API key or token rather
// than a variable name: a known key prefix, very long, or long and mixing
// lower case, upper case and digits (variable names are UPPER_SNAKE).
func LooksLikeSecret(s string) bool {
	if len(s) > 48 {
		return true
	}
	for _, p := range []string{"sk-", "sk_", "gsk_", "xai-", "csk-", "AIza", "pk-", "Bearer ", "ghp_", "github_pat_"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	var lower, upper, digit bool
	for _, r := range s {
		lower = lower || unicode.IsLower(r)
		upper = upper || unicode.IsUpper(r)
		digit = digit || unicode.IsDigit(r)
	}
	return len(s) >= 20 && lower && upper && digit
}

// ValidateModel checks one model ID as the API names it. Provider chains
// split on commas and spaces, so a model may contain neither.
func ValidateModel(m string) error {
	if m == "" {
		return errors.New("a model ID is required, e.g. gpt-4o-mini")
	}
	if strings.ContainsAny(m, ", \t\n\"") {
		return fmt.Errorf("%q: a model ID has no spaces, commas or quotes", m)
	}
	return nil
}

// ValidateLabel checks an optional display label: one short line.
func ValidateLabel(s string) error {
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return errors.New("a label is one line of text")
	}
	if n := len([]rune(s)); n > 60 {
		return fmt.Errorf("a label is at most 60 characters (this is %d)", n)
	}
	return nil
}

// Validate checks a new route against cfg: its fields, its name (see
// CheckRouteName) and that none of its provider IDs exists already.
func (r CustomRoute) Validate(cfg *config.Config) error {
	if err := CheckRouteName(cfg, r.Name, r.Local); err != nil {
		return err
	}
	if err := ValidateLabel(r.Label); err != nil {
		return err
	}
	if r.Local {
		if r.Preset != "" || r.KeyEnv != "" || len(r.Models) > 0 {
			return errors.New("a local server takes a name, a base URL and a label only")
		}
		return ValidateBaseURL(r.BaseURL)
	}
	if r.Preset != "" {
		if _, _, ok := PresetDefaults(r.Preset); !ok {
			return fmt.Errorf("unknown preset %q (have: %s)", r.Preset, strings.Join(presetOrder, ", "))
		}
		if r.BaseURL != "" {
			if err := ValidateBaseURL(r.BaseURL); err != nil {
				return err
			}
		}
	} else if err := ValidateBaseURL(r.BaseURL); err != nil {
		return fmt.Errorf("%w (or use a preset: %s)", err, strings.Join(presetOrder, ", "))
	}
	if r.KeyEnv != "" {
		if err := ValidateKeyEnv(r.KeyEnv); err != nil {
			return err
		}
	}
	if len(r.Models) == 0 {
		return errors.New("at least one model is required, e.g. --model gpt-4o-mini")
	}
	seen := map[string]bool{}
	for _, m := range r.Models {
		if err := ValidateModel(m); err != nil {
			return err
		}
		if seen[strings.ToLower(m)] {
			return fmt.Errorf("model %q is listed twice", m)
		}
		seen[strings.ToLower(m)] = true
	}
	existing := map[string]bool{}
	for _, p := range All(cfg) {
		existing[p.ID()] = true
	}
	for _, id := range r.IDs() {
		if existing[id] {
			return fmt.Errorf("provider %s already exists", id)
		}
	}
	return nil
}

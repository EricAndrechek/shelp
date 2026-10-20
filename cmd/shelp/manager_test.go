package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/cooldown"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
	"github.com/ericandrechek/shelp/internal/term"
)

// mgrFakes is a provider set with every class and kind.
func mgrFakes() []provider.Provider {
	return []provider.Provider{
		fakeProv{"shortcuts:apple-pro", "Apple Intelligence Pro", provider.AppleCloud, provider.Status{Available: true}},
		fakeProv{"shortcuts:chatgpt", "ChatGPT", provider.AppleCloud, provider.Status{Reason: "not installed", Fix: "run `shelp setup shortcuts`"}},
		fakeProv{"fm", "Apple on-device", provider.OnDevice, provider.Status{Available: true}},
		fakeProv{"claude:haiku", "Claude Haiku", provider.AgentCLI, provider.Status{Available: true}},
		fakeProv{"claude:sonnet", "Claude Sonnet", provider.AgentCLI, provider.Status{Reason: "not logged in", Fix: "run `claude /login`"}},
		fakeProv{"lmstudio", "first loaded model", provider.LocalServer, provider.Status{Reason: "not running (127.0.0.1:1234)"}},
		fakeProv{"ollama", "first loaded model", provider.LocalServer, provider.Status{Available: true}},
		fakeProv{"groq:llama", "llama", provider.API, provider.Status{Reason: "no API key", Fix: "set $GROQ_API_KEY"}},
	}
}

// testManager builds a manager over the config text src whose providers
// are fakes plus the openai family's (so routes added in a test appear),
// with every probe already answered unless pending.
func testManager(t *testing.T, src string, probe bool) *manager {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// Routes from the config replace a fake of the same ID (lmstudio).
	list := func(cfg *config.Config) []provider.Provider {
		var ps []provider.Provider
		seen := map[string]bool{}
		for _, p := range provider.All(cfg) {
			if r, ok := provider.RouteOf(p); ok && r.Configured {
				ps, seen[p.ID()] = append(ps, p), true
			}
		}
		for _, p := range mgrFakes() {
			if !seen[p.ID()] {
				ps = append(ps, p)
			}
		}
		return ps
	}
	m, err := newManager(style.Plain(), "/home/u/.config/shelp/config.toml", src, list)
	if err != nil {
		t.Fatal(err)
	}
	if probe {
		probeAll(m)
	}
	return m
}

// testProbe is Detect for the fakes and hosted endpoints (an env lookup),
// and "not running" for a local server: no socket is ever opened.
func testProbe(ctx context.Context, ps []provider.Provider, _ map[string]cooldown.Entry) []provider.Status {
	out := make([]provider.Status, len(ps))
	for i, p := range ps {
		if r, ok := provider.RouteOf(p); ok && r.Local {
			out[i] = provider.Status{Reason: "not running (" + hostOfURL(r.BaseURL) + ")"}
			continue
		}
		out[i] = p.Detect(ctx)
	}
	return out
}

// probeAll answers every pending probe effect with testProbe, as the
// runner does, and returns the other effects.
func probeAll(m *manager) []mgrEffect {
	var rest []mgrEffect
	for _, e := range m.takeEffects() {
		if e.kind != "probe" {
			rest = append(rest, e)
			continue
		}
		for _, id := range e.ids {
			if p := m.provs[id]; p != nil {
				m.setProbe(probeRow(context.Background(), testProbe, p, map[string]cooldown.Entry{}))
			}
		}
	}
	return rest
}

func press(m *manager, ks ...term.Key) {
	for _, k := range ks {
		m.Update(k)
	}
}

func viewOf(m *manager) string { return m.View(0, 0) }

func TestManagerLayout(t *testing.T) {
	m := testManager(t, "providers = [\"claude:haiku\", \"fm\", \"nosuch:x\"]\n", true)
	v := viewOf(m)
	t.Logf("view:\n%s", v)
	// The chain, numbered in order, with an unknown ID flagged.
	for _, w := range []string{
		"Your chain · tried in this order",
		"❯  1 ✓ claude:haiku",
		"   2 ✓ fm",
		"   3 ? nosuch:x",
		"unknown here",
	} {
		if !strings.Contains(v, w) {
			t.Errorf("view lacks %q", w)
		}
	}
	if strings.Contains(v, "shelp's default") {
		t.Error("a chain from the file is labelled the default")
	}
	// Others: available, needs setup, unavailable; local before cloud
	// within each.
	want := []string{"shortcuts:apple-pro", "claude:sonnet", "lmstudio"}
	_ = want
	order := m.items()[3:]
	if !slices.Equal(order, []string{"ollama", "shortcuts:apple-pro", "shortcuts:chatgpt", "groq:llama", "claude:sonnet", "lmstudio"}) {
		t.Errorf("others order %v", order)
	}
	for _, w := range []string{"  available", "  needs setup · Enter shows how", "  not available here"} {
		if !strings.Contains(v, w) {
			t.Errorf("view lacks group %q", w)
		}
	}
	// Every row says local or cloud.
	for _, l := range strings.Split(v, "\n") {
		for _, id := range []string{"fm ", "ollama", "lmstudio"} {
			if strings.Contains(l, " "+id) && !strings.Contains(l, "local") {
				t.Errorf("local row without the badge: %q", l)
			}
		}
		for _, id := range []string{"claude:haiku", "shortcuts:apple-pro", "groq:llama"} {
			if strings.Contains(l, " "+id) && !strings.Contains(l, "cloud") {
				t.Errorf("cloud row without the badge: %q", l)
			}
		}
	}
}

func TestManagerChecking(t *testing.T) {
	m := testManager(t, "", false)
	v := viewOf(m)
	if !strings.Contains(v, "shelp's default") || strings.Count(v, "checking…") < len(mgrFakes()) {
		t.Errorf("before probes:\n%s", v)
	}
	// Keys work while checking.
	press(m, keys("j")...)
	if m.idx != 1 {
		t.Errorf("cursor %d", m.idx)
	}
	probeAll(m)
	if v := viewOf(m); strings.Contains(v, "checking…") {
		t.Errorf("after probes:\n%s", v)
	}
}

func TestManagerToggleAndReorder(t *testing.T) {
	m := testManager(t, "", true) // default chain: shortcuts:apple-pro, fm
	// Take apple-pro out: the cursor stays at #1, now fm.
	press(m, keys(" ")...)
	if !slices.Equal(m.chain, []string{"fm"}) || m.cur != "fm" {
		t.Fatalf("chain %v, cursor %s", m.chain, m.cur)
	}
	// Down to the first other (ollama), add it: the cursor follows it.
	press(m, kDown, keys(" ")[0])
	if !slices.Equal(m.chain, []string{"fm", "ollama"}) || m.cur != "ollama" || m.idx != 1 {
		t.Fatalf("chain %v, cursor %s@%d", m.chain, m.cur, m.idx)
	}
	// Every way of moving up and down.
	for _, c := range []struct {
		k    term.Key
		want []string
	}{
		{kShiftUp, []string{"ollama", "fm"}},
		{kShiftDown, []string{"fm", "ollama"}},
		{keys("K")[0], []string{"ollama", "fm"}},
		{keys("J")[0], []string{"fm", "ollama"}},
		{keys("+")[0], []string{"ollama", "fm"}},
		{keys("-")[0], []string{"fm", "ollama"}},
		{keys("-")[0], []string{"fm", "ollama"}}, // at the bottom: no-op
	} {
		press(m, c.k)
		if !slices.Equal(m.chain, c.want) || m.cur != "ollama" {
			t.Errorf("%+v: chain %v, cursor %s", c.k, m.chain, m.cur)
		}
	}
	// Moving a provider that is not in the chain says so.
	press(m, kDown, kDown, kShiftUp)
	if !strings.Contains(m.msg, "only chain entries move") {
		t.Errorf("msg %q", m.msg)
	}
	// Adding one that needs setup warns.
	m.cur = "claude:sonnet"
	m.sync()
	press(m, keys(" ")...)
	if !m.msgWarn || !strings.Contains(m.msg, "Enter shows how") {
		t.Errorf("msg %q", m.msg)
	}
	if o := m.ops(); !slices.Equal(o.chain, []string{"fm", "ollama", "claude:sonnet"}) {
		t.Errorf("ops %+v", o)
	}
	// Emptying the chain warns about the default.
	m = testManager(t, "providers = [\"fm\"]\n", true)
	press(m, keys(" ")...)
	if !strings.Contains(m.msg, "the chain is empty") || m.ops().chain == nil || len(m.ops().chain) != 0 {
		t.Errorf("msg %q ops %+v", m.msg, m.ops())
	}
}

func TestManagerNavigation(t *testing.T) {
	m := testManager(t, "", true)
	n := len(m.items())
	press(m, keys("G")...)
	if m.idx != n-1 {
		t.Errorf("G: %d", m.idx)
	}
	press(m, kDown)
	if m.idx != n-1 {
		t.Error("moved past the end")
	}
	press(m, keys("g")...)
	press(m, kUp, keys("k")[0])
	if m.idx != 0 {
		t.Error("moved before the start")
	}
	press(m, keys("jj")...)
	if m.idx != 2 {
		t.Errorf("jj: %d", m.idx)
	}
	press(m, keys("?")...)
	if !strings.Contains(viewOf(m), "x, Delete") {
		t.Error("? shows no key help")
	}
}

func TestManagerQuit(t *testing.T) {
	m := testManager(t, "", true)
	press(m, keys("q")...)
	if !m.quit || m.code != 0 {
		t.Error("q with nothing changed did not quit")
	}
	for _, q := range []term.Key{keys("q")[0], kEsc} {
		m = testManager(t, "", true)
		press(m, keys("J")[0], q)
		if m.quit || m.mode != modeConfirm || !strings.Contains(viewOf(m), "Discard your unsaved changes? y/n") {
			t.Fatalf("%+v with changes: quit=%v mode=%v", q, m.quit, m.mode)
		}
		press(m, keys("n")...)
		if m.quit || m.mode != modeList {
			t.Error("n did not go back")
		}
		press(m, q, keys("y")[0])
		if !m.quit || m.code != 0 {
			t.Error("y did not quit")
		}
	}
	// s in the question saves, then quits.
	m = testManager(t, "", true)
	press(m, keys("J", "q", "s")...)
	if e := m.takeEffects(); len(e) != 1 || e[0].kind != "save" || !m.quitAfterSave {
		t.Errorf("effects %+v", e)
	}
	m.markSaved("providers = [\"fm\", \"shortcuts:apple-pro\"]\n", "saved")
	if !m.quit || m.dirty() {
		t.Error("not quit after the save")
	}
	// Ctrl-C quits at once, changes and all, with 130 -- in every mode.
	for _, pre := range [][]term.Key{nil, keys("J", "q"), keys("a")} {
		m = testManager(t, "", true)
		press(m, pre...)
		press(m, term.CtrlC)
		if !m.quit || m.code != 130 {
			t.Errorf("after %v: Ctrl-C quit=%v code=%d", pre, m.quit, m.code)
		}
	}
}

func TestManagerSaveAndEnter(t *testing.T) {
	m := testManager(t, "", true)
	press(m, keys("s")...)
	if m.msg != "nothing to save" || len(m.takeEffects()) != 0 {
		t.Errorf("msg %q", m.msg)
	}
	press(m, keys("J", "s")...)
	if e := m.takeEffects(); len(e) != 1 || e[0].kind != "save" {
		t.Errorf("effects %+v", e)
	}
	if !strings.Contains(viewOf(m), "● unsaved changes") {
		t.Error("no unsaved marker")
	}
	m.markSaved("providers = [\"fm\", \"shortcuts:apple-pro\"]\n", "saved fm → shortcuts:apple-pro")
	if m.dirty() || strings.Contains(viewOf(m), "unsaved") || !strings.Contains(viewOf(m), "saved fm") {
		t.Errorf("after save:\n%s", viewOf(m))
	}
	// Enter: a fix effect for a provider that needs setup or is not
	// available; a note for one that works.
	for id, want := range map[string]string{"shortcuts:chatgpt": "fix", "lmstudio": "fix", "groq:llama": "fix", "ollama": ""} {
		m.cur = id
		m.sync()
		press(m, kEnter)
		e := m.takeEffects()
		if want == "" {
			if len(e) != 0 || !strings.Contains(m.msg, "works") {
				t.Errorf("%s: effects %+v msg %q", id, e, m.msg)
			}
			continue
		}
		if len(e) != 1 || e[0].kind != want || e[0].ids[0] != id {
			t.Errorf("%s: effects %+v", id, e)
		}
	}
}

func TestManagerCooldownRow(t *testing.T) {
	m := testManager(t, "", true)
	m.setProbe(providerRow{ID: "ollama", Kind: "local-server", Description: "first loaded model", Status: statusCooldown, Reason: "until 14:32 (usage limit)"})
	if v := viewOf(m); !strings.Contains(v, "◷ ollama") || !strings.Contains(v, "rate-limited until 14:32") {
		t.Errorf("view:\n%s", v)
	}
	if m.rows["ollama"].class() != clsOK {
		t.Error("a cooled-down provider is grouped as available")
	}
}

func TestManagerViewFits(t *testing.T) {
	m := testManager(t, "", true)
	press(m, keys("G")...) // the cursor on the last row
	v := m.View(40, 14)
	lines := strings.Split(v, "\n")
	if len(lines) > 14 {
		t.Errorf("%d lines for a 14-line screen:\n%s", len(lines), v)
	}
	for _, l := range lines {
		if style.Width(l) > 39 {
			t.Errorf("line wider than 39: %q", l)
		}
	}
	if !strings.Contains(v, "❯") || !strings.Contains(v, "↑") {
		t.Errorf("cursor not shown or no 'more' marker:\n%s", v)
	}
	press(m, keys("g")...)
	if v := m.View(40, 14); !strings.Contains(v, "❯") || !strings.Contains(v, "↓") {
		t.Errorf("top:\n%s", v)
	}
}

// With colors the view is the plain view plus escape codes.
func TestManagerViewColorStripsToPlain(t *testing.T) {
	m := testManager(t, "providers = [\"fm\", \"nosuch:x\"]\n", true)
	plain := viewOf(m)
	m.pal = style.Forced(style.TrueColor)
	colored := viewOf(m)
	if colored == plain || style.Strip(colored) != plain {
		t.Errorf("colored view differs from plain once stripped:\n%s\n---\n%s", style.Strip(colored), plain)
	}
}

func TestManagerOfflineAndEnvNotes(t *testing.T) {
	m := testManager(t, "", true)
	m.offline, m.env = true, "fm"
	v := viewOf(m)
	if !strings.Contains(v, "offline: no network route") || !strings.Contains(v, "$SHELP_PROVIDER is set (fm)") {
		t.Errorf("view:\n%s", v)
	}
}

const routedSrc = `providers = ["fm", "work:big", "lmstudio"]

[[openai.endpoint]]
name = "work"
base_url = "https://llm.example.com/v1"
api_key_env = "WORK_KEY"
models = ["big", "small"]
label = "Work"

[[openai.local]]
name = "lmstudio"
base_url = "http://127.0.0.1:1235/v1"
`

func TestManagerRemoveRoute(t *testing.T) {
	m := testManager(t, routedSrc, true)
	if r := m.rows["work:big"]; r == nil || r.Label != "Work · big" {
		t.Fatalf("work:big row %+v", r)
	}
	// A provider with no route of its own cannot be removed.
	m.cur = "fm"
	m.sync()
	press(m, keys("x")...)
	if m.mode != modeList || !strings.Contains(m.msg, "only routes you added") {
		t.Errorf("fm: %q", m.msg)
	}
	m.cur = "ollama"
	m.sync()
	m.rows["ollama"].hasRoute, m.rows["ollama"].route = true, provider.Route{Local: true, Name: "ollama", Builtin: true}
	press(m, kDelete)
	if !strings.Contains(m.msg, "built in") {
		t.Errorf("ollama: %q", m.msg)
	}
	// A configured endpoint: asks, then drops its rows and chain entries.
	m.cur = "work:big"
	m.sync()
	press(m, keys("x")...)
	if m.mode != modeConfirm || !strings.Contains(m.question, "Remove work (work:big, work:small)?") {
		t.Fatalf("question %q", m.question)
	}
	press(m, keys("y")...)
	if m.rows["work:big"] != nil || slices.Contains(m.chain, "work:big") {
		t.Errorf("not removed: chain %v", m.chain)
	}
	if o := m.ops(); !slices.Equal(o.removes, []string{"work"}) || !slices.Equal(o.chain, []string{"fm", "lmstudio"}) {
		t.Errorf("ops %+v", o)
	}
	// The lmstudio override: removing it goes back to the built-in, which
	// stays in the chain.
	m.cur = "lmstudio"
	m.sync()
	press(m, keys("x")...)
	if !strings.Contains(m.question, "back to the built-in 127.0.0.1:1234") {
		t.Errorf("question %q", m.question)
	}
	press(m, keys("n")...)
	if len(m.ops().removes) != 1 {
		t.Error("n removed it")
	}
	press(m, keys("x", "y")...)
	if !slices.Contains(m.chain, "lmstudio") || !slices.Equal(m.ops().removes, []string{"work", "lmstudio"}) {
		t.Errorf("chain %v ops %+v", m.chain, m.ops())
	}
	out, err := m.ops().apply(routedSrc)
	if err != nil || strings.Contains(out, "openai") || !strings.Contains(out, `providers = ["fm", "lmstudio"]`) {
		t.Errorf("apply: %v\n%s", err, out)
	}
}

// The `a` form, hosted: preset, name, key variable, models, label, review,
// chain.
func TestManagerAddEndpoint(t *testing.T) {
	m := testManager(t, "", true)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-secret-value")
	press(m, keys("a")...)
	if m.mode != modeForm || !strings.Contains(viewOf(m), "What kind of route?") {
		t.Fatalf("form not open:\n%s", viewOf(m))
	}
	press(m, kEnter) // hosted endpoint
	if !strings.Contains(viewOf(m), "Which service?") || !strings.Contains(viewOf(m), "openrouter") {
		t.Fatalf("preset step:\n%s", viewOf(m))
	}
	press(m, kDown, kEnter) // openrouter
	if v := viewOf(m); !strings.Contains(v, "Name") || !strings.Contains(v, "openrouter") {
		t.Fatalf("name step (default from the preset):\n%s", v)
	}
	press(m, kEnter)
	v := viewOf(m)
	if !strings.Contains(v, "OPENROUTER_API_KEY") || !strings.Contains(v, "is set in this shell") {
		t.Fatalf("key step:\n%s", v)
	}
	if strings.Contains(v, "sk-or-secret-value") {
		t.Fatal("the key's value is on screen")
	}
	press(m, kEnter)
	press(m, keys("anthropic/claude-haiku-4.5")...)
	press(m, kEnter)
	press(m, keys("Haiku via OpenRouter")...)
	press(m, kEnter)
	v = viewOf(m)
	t.Logf("review:\n%s", v)
	for _, w := range []string{"Add this route?", "[[openai.endpoint]]", `preset = "openrouter"`, `models = ["anthropic/claude-haiku-4.5"]`, `label = "Haiku via OpenRouter"`, "adds openrouter:anthropic/claude-haiku-4.5"} {
		if !strings.Contains(v, w) {
			t.Errorf("review lacks %q", w)
		}
	}
	if strings.Contains(v, "api_key_env") {
		t.Error("the preset's own key variable was written")
	}
	press(m, kEnter)
	if !strings.Contains(viewOf(m), "end of your chain? y/n") {
		t.Fatalf("chain step:\n%s", viewOf(m))
	}
	press(m, keys("y")...)
	id := "openrouter:anthropic/claude-haiku-4.5"
	if m.mode != modeList || !slices.Contains(m.chain, id) || m.cur != id {
		t.Fatalf("after the form: mode %v chain %v cursor %s", m.mode, m.chain, m.cur)
	}
	if r := m.rows[id]; r == nil || !r.isNew || r.Label != "Haiku via OpenRouter" || r.probed {
		t.Errorf("new row %+v", r)
	}
	if e := m.takeEffects(); len(e) != 1 || e[0].kind != "probe" || !slices.Contains(e[0].ids, id) {
		t.Errorf("effects %+v", e)
	}
	if v := viewOf(m); !strings.Contains(v, "(new)") || !strings.Contains(v, "● unsaved") {
		t.Errorf("view:\n%s", v)
	}
	out, err := m.ops().apply("")
	if err != nil || !strings.Contains(out, `name = "openrouter"`) || !strings.Contains(out, id) {
		t.Errorf("apply: %v\n%s", err, out)
	}
	// Removing a route added this session just drops it from the adds.
	press(m, keys("x", "y")...)
	if len(m.ops().adds) != 0 || len(m.ops().removes) != 0 || slices.Contains(m.chain, id) {
		t.Errorf("ops %+v chain %v", m.ops(), m.chain)
	}
}

func TestManagerAddLocalAndValidation(t *testing.T) {
	m := testManager(t, routedSrc, true)
	press(m, keys("a")...)
	press(m, kDown, kEnter) // local server
	// A taken name is refused, then fixed.
	press(m, keys("work")...)
	press(m, kEnter)
	if v := viewOf(m); !strings.Contains(v, `named "work" is already in the config`) {
		t.Fatalf("taken name:\n%s", v)
	}
	press(m, kBackspace, kBackspace, kBackspace, kBackspace)
	press(m, keys("Box")...)
	press(m, kEnter)
	// A bad URL is refused.
	press(m, keys("ftp://box")...)
	press(m, kEnter)
	if v := viewOf(m); !strings.Contains(v, "not an http(s) URL") {
		t.Fatalf("bad url:\n%s", v)
	}
	press(m, term.Key{Code: term.KeyCtrl, Rune: 'u'})
	press(m, keys("http://10.0.0.2:8080/v1")...)
	press(m, kEnter, kEnter) // URL, no label
	if v := viewOf(m); !strings.Contains(v, "[[openai.local]]") || !strings.Contains(v, `name = "box"`) {
		t.Fatalf("review:\n%s", v)
	}
	press(m, kEnter, keys("n")[0])
	if m.rows["box"] == nil || slices.Contains(m.chain, "box") {
		t.Errorf("box row %v, chain %v", m.rows["box"], m.chain)
	}
	// A preset whose name is taken suggests name-2.
	press(m, keys("a")...)
	press(m, kEnter)
	for range 20 {
		press(m, kUp)
	}
	press(m, kEnter) // openai
	m.form.cfg, _ = config.Parse("[[openai.endpoint]]\nname = \"openai\"\npreset = \"openai\"\nmodels = [\"x\"]\n")
	press(m, kBackTab, kEnter)
	if got := m.form.val(stepName); got != "openai-2" {
		t.Errorf("name default %q", got)
	}
	press(m, kEsc)
	// Shift+Tab goes back; Esc cancels.
	press(m, keys("a")...)
	press(m, kEnter, kEnter, kBackTab)
	if m.form.step != stepPreset {
		t.Errorf("back: step %v", m.form.step)
	}
	press(m, kEsc)
	if m.mode != modeList || m.msg != "nothing added" || len(m.ops().adds) != 1 {
		t.Errorf("esc: mode %v msg %q", m.mode, m.msg)
	}
}

// A key pasted where the variable's name goes is masked and refused.
func TestManagerAddRefusesAKey(t *testing.T) {
	m := testManager(t, "", true)
	press(m, keys("a")...)
	press(m, kEnter)                            // hosted
	press(m, keys("G")...)                      // nothing: G is not a list key
	for range len(provider.PresetNames()) + 1 { // custom
		press(m, kDown)
	}
	press(m, kEnter)
	press(m, keys("https://llm.example.com/v1")...)
	press(m, kEnter)
	press(m, keys("work")...)
	press(m, kEnter)
	if v := viewOf(m); !strings.Contains(v, "WORK_API_KEY") {
		t.Fatalf("custom URL default key variable:\n%s", v)
	}
	press(m, term.Key{Code: term.KeyCtrl, Rune: 'u'})
	secret := "sk-proj-abcdefghijklmnopqrstuvwxyz"
	press(m, keys(secret)...)
	v := viewOf(m)
	if strings.Contains(v, secret) || !strings.Contains(v, "looks like an API key") {
		t.Errorf("key step shows the key or no warning:\n%s", v)
	}
	press(m, kEnter)
	if m.form.step != stepKeyEnv || strings.Contains(viewOf(m), secret) {
		t.Error("a key was accepted")
	}
	// An empty variable is fine for a custom URL: no key is sent.
	press(m, term.Key{Code: term.KeyCtrl, Rune: 'u'}, kEnter)
	if m.form.step != stepModels {
		t.Errorf("empty key variable refused: step %v", m.form.step)
	}
}

func TestLineInput(t *testing.T) {
	var l lineInput
	for _, k := range keys("helo", term.Key{Code: term.KeyLeft}, "l", term.Key{Code: term.KeyEnd}, " world") {
		l.update(k)
	}
	if l.value() != "hello world" {
		t.Errorf("%q", l.value())
	}
	l.update(term.Key{Code: term.KeyCtrl, Rune: 'w'})
	if l.value() != "hello" {
		t.Errorf("ctrl-w: %q", l.value())
	}
	l.update(term.Key{Code: term.KeyHome})
	l.update(kDelete)
	// Ctrl-W leaves the space before the word, as readline does.
	if l.value() != "ello" || l.render(style.Plain(), "", false) != "▏ello " {
		t.Errorf("%q %q", l.value(), l.render(style.Plain(), "", false))
	}
	if got := l.render(style.Plain(), "", true); got != "▏•••••" {
		t.Errorf("masked %q", got)
	}
	if got := (&lineInput{}).render(style.Plain(), "e.g. x", false); got != "▏e.g. x" {
		t.Errorf("placeholder %q", got)
	}
}

func TestFixLines(t *testing.T) {
	t.Setenv("WORK_KEY", "")
	m := testManager(t, routedSrc, true)
	m.setProbe(providerRow{ID: "work:big", Kind: "api", Description: m.rows["work:big"].Description, Status: statusUnavailable, Reason: "no API key", Fix: "set $WORK_KEY"})
	m.setProbe(providerRow{ID: "lmstudio", Kind: "local-server", Description: m.rows["lmstudio"].Description, Status: statusUnavailable, Reason: "not running (127.0.0.1:1235)"})
	join := func(id string) string { return strings.Join(fixLines(style.Plain(), m.rows[id]), "\n") }
	for id, wants := range map[string][]string{
		"work:big":          {"no API key", "$WORK_KEY is not set", "export WORK_KEY=…", "never stores it"},
		"lmstudio":          {"not running", "http://127.0.0.1:1235/v1"},
		"shortcuts:chatgpt": {"not installed", "Run `shelp setup shortcuts`."},
		"claude:sonnet":     {"not logged in", "Run `claude /login`."},
	} {
		got := join(id)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s: lacks %q:\n%s", id, w, got)
			}
		}
	}
	fm := &mgrRow{providerRow: providerRow{ID: "fm", Kind: "on-device", Status: statusUnavailable, Reason: "license not accepted", Fix: "run `sudo fm license`"}, probed: true}
	if got := strings.Join(fixLines(style.Plain(), fm), "\n"); !strings.Contains(got, "run `sudo fm license`") || !strings.Contains(got, "never runs sudo") {
		t.Errorf("fm:\n%s", got)
	}
	ol := &mgrRow{providerRow: providerRow{ID: "ollama", Kind: "local-server", Reason: "not running (127.0.0.1:11434)"}, probed: true,
		hasRoute: true, route: provider.Route{Local: true, Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Builtin: true}}
	if got := strings.Join(fixLines(style.Plain(), ol), "\n"); !strings.Contains(got, "ollama serve") {
		t.Errorf("ollama:\n%s", got)
	}
}

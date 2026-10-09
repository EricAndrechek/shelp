package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/style"
)

const siriFix = "run `shelp setup shortcuts`"

func sampleRows() []providerRow {
	notInstalled := func(id, reason string) providerRow {
		return providerRow{ID: id, Kind: "apple-cloud", Status: "unavailable", Reason: reason, Fix: siriFix, Detail: `shortcut "` + id + `" not found`}
	}
	chatgpt := "not installed; needs the ChatGPT extension (System Settings › Siri)"
	local := func(id, addr string) providerRow {
		return providerRow{ID: id, Kind: "local-server", Status: "unavailable", Reason: "not running (" + addr + ")",
			Detail: "not reachable at http://" + addr + "/v1: connection refused"}
	}
	return []providerRow{
		notInstalled("shortcuts:apple", "not installed"),
		{ID: "shortcuts:apple-pro", Kind: "apple-cloud", Status: "ok", Available: true, Label: "Apple Intelligence Pro", Note: "Apple's cloud · larger model"},
		notInstalled("shortcuts:apple-web", "not installed"),
		notInstalled("shortcuts:apple-pro-web", "not installed"),
		notInstalled("shortcuts:chatgpt", chatgpt),
		notInstalled("shortcuts:chatgpt-web", chatgpt),
		{ID: "fm", Kind: "on-device", Status: "ok", Available: true, Label: "Apple on-device model", Note: "this Mac, offline · fastest, weakest"},
		local("llamacpp", "127.0.0.1:8080"),
		local("lmstudio", "127.0.0.1:1234"),
		local("ollama", "127.0.0.1:11434"),
		{ID: "claude:haiku", Kind: "agent-cli", Status: "ok", Available: true, Label: "Claude Haiku", Note: "Anthropic, your Claude plan · faster"},
		{ID: "claude:sonnet", Kind: "agent-cli", Status: "ok", Available: true, Label: "Claude Sonnet", Note: "Anthropic, your Claude plan · stronger"},
	}
}

var sampleChain = []string{"shortcuts:apple-pro", "claude:sonnet", "fm"}

const wantSample = `chain  shortcuts:apple-pro → claude:sonnet → fm   (config)

Apple Shortcuts
  ✓ shortcuts:apple-pro      Apple Intelligence Pro  Apple's cloud · larger model
  · shortcuts:apple          not installed
  · shortcuts:apple-pro-web  not installed
  · shortcuts:chatgpt        not installed; needs the ChatGPT extension (System Settings › Siri)
    add -web to let the model look things up online (slower), e.g. shortcuts:apple-web
    → run ` + "`shelp setup shortcuts`" + `

On-device
  ✓ fm                       Apple on-device model  this Mac, offline · fastest, weakest

Local servers
  · llamacpp                 not running (127.0.0.1:8080)
  · lmstudio                 not running (127.0.0.1:1234)
  · ollama                   not running (127.0.0.1:11434)

Claude Code
  ✓ claude:haiku             Claude Haiku   Anthropic, your Claude plan · faster
  ✓ claude:sonnet            Claude Sonnet  Anthropic, your Claude plan · stronger
`

func TestRenderProvidersPlain(t *testing.T) {
	got := renderProviders(style.Plain(), sampleRows(), sampleChain, "config", false, 0, false)
	if got != wantSample {
		t.Errorf("plain view mismatch:\n%s\nwant:\n%s", got, wantSample)
	}
}

func TestRenderProvidersColorStripsToPlain(t *testing.T) {
	for _, d := range []style.Depth{style.Color16, style.Color256, style.TrueColor} {
		got := renderProviders(style.Forced(d), sampleRows(), sampleChain, "config", false, 0, false)
		if got == wantSample {
			t.Fatal("forced palette produced no color")
		}
		if s := style.Strip(got); s != wantSample {
			t.Errorf("depth %d: stripped color view differs from plain:\n%s", d, s)
		}
	}
}

func TestRenderProvidersChainColors(t *testing.T) {
	rows := []providerRow{
		{ID: "a", Kind: "on-device", Status: "ok", Available: true},
		{ID: "b", Kind: "on-device", Status: "cooldown", Reason: "until 14:32 (usage limit)"},
		{ID: "c", Kind: "on-device", Status: "unavailable", Reason: "not found"},
	}
	p := style.Forced(style.Color16)
	got := renderProviders(p, rows, []string{"a", "b", "c", "nope"}, "$SHELP_PROVIDER", false, 0, false)
	first := strings.SplitN(got, "\n", 2)[0]
	for _, want := range []string{p.Bold("a"), p.Warn("b"), p.Dim("c"), p.Err("nope"), p.Dim(" → ")} {
		if !strings.Contains(first, want) {
			t.Errorf("chain line %q missing %q", first, want)
		}
	}
	if style.Strip(first) != "chain  a → b → c → nope   ($SHELP_PROVIDER)" {
		t.Errorf("chain line = %q", style.Strip(first))
	}
}

func TestRenderProvidersCooldownNoteAndOrder(t *testing.T) {
	rows := []providerRow{
		{ID: "x", Kind: "apple-cloud", Status: "unavailable", Reason: "not installed", Fix: siriFix},
		{ID: "y", Kind: "apple-cloud", Status: "cooldown", Reason: "until 14:32 (usage limit)"},
		{ID: "z", Kind: "apple-cloud", Status: "ok", Available: true, Label: "Zed", Reason: "outdated shortcut (format v1, current v2)", Fix: "run `shelp setup shortcuts` to update"},
		{ID: "claude:a", Kind: "agent-cli", Status: "ok", Label: "a"},
		{ID: "other", Kind: "agent-cli", Status: "ok", Label: "o"},
	}
	want := `Apple Shortcuts
  ✓ z         Zed · outdated shortcut (format v1, current v2)
  ◷ y         cooldown until 14:32 (usage limit)
  · x         not installed
    → run ` + "`shelp setup shortcuts` to update" + `
    → run ` + "`shelp setup shortcuts`" + `

AI CLIs
  ✓ claude:a  a
  ✓ other     o
`
	if got := renderProviders(style.Plain(), rows, nil, "default", false, 0, false); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderProvidersAllShowsDetail(t *testing.T) {
	got := renderProviders(style.Plain(), sampleRows(), sampleChain, "config", true, 0, false)
	if !strings.Contains(got, "  · llamacpp                 not running (127.0.0.1:8080)\n      not reachable at http://127.0.0.1:8080/v1: connection refused\n") {
		t.Errorf("--all view lacks the detail line:\n%s", got)
	}
	if strings.Contains(renderProviders(style.Plain(), sampleRows(), sampleChain, "config", false, 0, false), "connection refused") {
		t.Error("default view must not show details")
	}
}

// The installed-and-current view: three rows, the fold line, no -web twins.
func TestRenderProvidersFoldsWebTwins(t *testing.T) {
	ok := func(id, label, note string) providerRow {
		return providerRow{ID: id, Kind: "apple-cloud", Status: "ok", Available: true, Label: label, Note: note, Group: `"shelp" · v6`}
	}
	rows := []providerRow{
		ok("shortcuts:apple", "Apple Intelligence", "Apple's cloud · smaller model"), ok("shortcuts:apple-web", "Apple Intelligence + web search", "Apple's cloud · smaller model · looks things up online"),
		ok("shortcuts:apple-pro", "Apple Intelligence Pro", "Apple's cloud · larger model"), ok("shortcuts:apple-pro-web", "Apple Intelligence Pro + web search", "x"),
		ok("shortcuts:chatgpt", "ChatGPT", "OpenAI, through Apple's extension"), ok("shortcuts:chatgpt-web", "ChatGPT + web search", "x"),
	}
	want := `Apple Shortcuts  "shelp" · v6
  ✓ shortcuts:apple      Apple Intelligence      Apple's cloud · smaller model
  ✓ shortcuts:apple-pro  Apple Intelligence Pro  Apple's cloud · larger model
  ✓ shortcuts:chatgpt    ChatGPT                 OpenAI, through Apple's extension
    add -web to let the model look things up online (slower), e.g. shortcuts:apple-web
`
	if got := renderProviders(style.Plain(), rows, nil, "default", false, 0, false); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := renderProviders(style.Plain(), rows, nil, "default", true, 0, false); strings.Count(got, "✓") != 6 || strings.Contains(got, "add -web") {
		t.Errorf("--all must show all six rows and no fold line:\n%s", got)
	}
	// A twin with its own status stays visible.
	rows[1].Status, rows[1].Reason = statusCooldown, "until 14:32 (usage limit)"
	got := renderProviders(style.Plain(), rows, nil, "default", false, 0, false)
	if !strings.Contains(got, "◷ shortcuts:apple-web") || !strings.Contains(got, "e.g. shortcuts:apple-pro-web") {
		t.Errorf("differing twin must stay visible:\n%s", got)
	}
}

func TestRenderProvidersTruncates(t *testing.T) {
	for _, p := range []style.Palette{style.Plain(), style.Forced(style.Color16)} {
		got := renderProviders(p, sampleRows(), sampleChain, "config", false, 60, false)
		for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
			if w := style.Width(l); w > 59 {
				t.Errorf("line %q is %d wide, want <= 59", style.Strip(l), w)
			}
		}
		if !strings.Contains(style.Strip(got), "not installed; needs the Chat…\n") {
			t.Errorf("long line not truncated with an ellipsis:\n%s", style.Strip(got))
		}
	}
}

func TestProviderRowJSON(t *testing.T) {
	b, err := json.Marshal(sampleRows()[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ID", "Kind", "Description", "Status", "Reason", "Version", "Available", "Fix", "Detail"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON row lacks field %q: %s", k, b)
		}
	}
	if m["Reason"] != "not installed" || m["Fix"] != siriFix || !strings.Contains(m["Detail"].(string), "not found") {
		t.Errorf("unexpected JSON values: %s", b)
	}
}

func TestRenderProviders_OfflineNote(t *testing.T) {
	on := renderProviders(style.Plain(), sampleRows(), sampleChain, "config", false, 0, true)
	off := renderProviders(style.Plain(), sampleRows(), sampleChain, "config", false, 0, false)
	if !strings.Contains(on, "(config)  offline") {
		t.Errorf("offline note missing:\n%s", on)
	}
	if strings.Contains(off, "(config)  offline") {
		t.Errorf("unexpected offline note:\n%s", off)
	}
}

// A group whose rows agree on a Group note shows it after the heading;
// rows that disagree show none.
func TestRenderProvidersGroupNote(t *testing.T) {
	rows := []providerRow{
		{ID: "shortcuts:apple-pro", Kind: "apple-cloud", Status: statusOK, Label: "Apple Intelligence Pro", Group: `"shelp" · v6`},
		{ID: "shortcuts:apple", Kind: "apple-cloud", Status: statusOK, Label: "Apple Intelligence", Group: `"shelp" · v6`},
		{ID: "fm", Kind: "on-device", Status: statusOK, Label: "Apple on-device model"},
	}
	got := renderProviders(style.Plain(), rows, nil, "default", false, 0, false)
	if !strings.Contains(got, "Apple Shortcuts  \"shelp\" · v6\n") || !strings.Contains(got, "On-device\n") {
		t.Errorf("want the shared Group after the Apple Shortcuts heading only:\n%s", got)
	}
	rows[1].Group = `"other" · v6`
	if got := renderProviders(style.Plain(), rows, nil, "default", false, 0, false); !strings.Contains(got, "Apple Shortcuts\n") {
		t.Errorf("rows that disagree must not show a Group:\n%s", got)
	}
}

// The fold line's example prefers the twin of a base row in the chain.
func TestFoldExample(t *testing.T) {
	hidden := []string{"shortcuts:apple-web", "shortcuts:chatgpt-web"}
	if got := foldExample(hidden, []string{"fm", "shortcuts:chatgpt"}); got != "shortcuts:chatgpt-web" {
		t.Errorf("chain preference: got %q", got)
	}
	if got := foldExample(hidden, []string{"fm"}); got != "shortcuts:apple-web" {
		t.Errorf("fallback: got %q", got)
	}
}

func TestProviderRowJSONNote(t *testing.T) {
	b, _ := json.Marshal(providerRow{ID: "fm", Note: "n"})
	if !strings.Contains(string(b), `"Note":"n"`) {
		t.Errorf("Note missing: %s", b)
	}
	b, _ = json.Marshal(providerRow{ID: "fm"})
	if strings.Contains(string(b), "Note") {
		t.Errorf("empty Note must be omitted: %s", b)
	}
}

func TestProvidersHelpHowTheyDiffer(t *testing.T) {
	h := commandHelp["providers"]
	for _, w := range []string{"How they differ", "shelp's benchmark", "31-36%", "98%", "Private Cloud Compute", "ChatGPT extension", "-web", "headless", "~84%", "local servers"} {
		if !strings.Contains(h, w) {
			t.Errorf("providers help lacks %q", w)
		}
	}
}

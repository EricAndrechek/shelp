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
		{ID: "shortcuts:apple-pro", Kind: "apple-cloud", Status: "ok", Available: true, Label: "Apple Intelligence Pro"},
		notInstalled("shortcuts:apple-web", "not installed"),
		notInstalled("shortcuts:apple-pro-web", "not installed"),
		notInstalled("shortcuts:chatgpt", chatgpt),
		notInstalled("shortcuts:chatgpt-web", chatgpt),
		{ID: "fm", Kind: "on-device", Status: "ok", Available: true, Label: "Apple on-device model"},
		local("llamacpp", "127.0.0.1:8080"),
		local("lmstudio", "127.0.0.1:1234"),
		local("ollama", "127.0.0.1:11434"),
		{ID: "claude:haiku", Kind: "agent-cli", Status: "ok", Available: true, Label: "haiku · headless, no tools"},
		{ID: "claude:sonnet", Kind: "agent-cli", Status: "ok", Available: true, Label: "sonnet · headless, no tools"},
	}
}

var sampleChain = []string{"shortcuts:apple-pro", "claude:sonnet", "fm"}

const wantSample = `chain  shortcuts:apple-pro → claude:sonnet → fm   (config)

Apple Shortcuts
  ✓ shortcuts:apple-pro      Apple Intelligence Pro
  · shortcuts:apple          not installed
  · shortcuts:apple-web      not installed
  · shortcuts:apple-pro-web  not installed
  · shortcuts:chatgpt        not installed; needs the ChatGPT extension (System Settings › Siri)
  · shortcuts:chatgpt-web    not installed; needs the ChatGPT extension (System Settings › Siri)
    → run ` + "`shelp setup shortcuts`" + `

On-device
  ✓ fm                       Apple on-device model

Local servers
  · llamacpp                 not running (127.0.0.1:8080)
  · lmstudio                 not running (127.0.0.1:1234)
  · ollama                   not running (127.0.0.1:11434)

Claude Code
  ✓ claude:haiku             haiku · headless, no tools
  ✓ claude:sonnet            sonnet · headless, no tools
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
	if strings.Contains(off, "offline") {
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

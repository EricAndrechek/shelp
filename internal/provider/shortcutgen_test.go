package provider

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// uuidLikeRE matches an 8-4-4-4-12 hex string, upper- or lower-case.
var uuidLikeRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// normalizeUUIDs walks a generic JSON tree (as produced by
// json.Unmarshal into `any`) and replaces every UUID-shaped string with a
// fixed placeholder, so two workflows that differ only by their randomly
// generated action UUID compare equal.
func normalizeUUIDs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = normalizeUUIDs(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normalizeUUIDs(vv)
		}
		return out
	case string:
		if uuidLikeRE.MatchString(t) {
			return "<UUID>"
		}
		return t
	default:
		return v
	}
}

func mustGenericJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	return v
}

// TestBuildWorkflowMatchesReference checks buildWorkflow's JSON shape
// against testdata/shortcuts/reference.json, which is the "Ask AI"
// shortcut's plist (WFLLMModel "Apple Intelligence Pro", web search off)
// as reverse-engineered on macOS 27.0 -- see CLAUDE.md. UUIDs are
// normalized away since buildWorkflow generates a fresh one every call.
func TestBuildWorkflowMatchesReference(t *testing.T) {
	refBytes, err := os.ReadFile("testdata/shortcuts/reference.json")
	if err != nil {
		t.Fatalf("read reference.json: %v", err)
	}
	ref := normalizeUUIDs(mustGenericJSON(t, refBytes))

	wf := buildWorkflow("Apple Intelligence Pro", false)
	gotBytes, err := json.Marshal(wf)
	if err != nil {
		t.Fatalf("marshal generated workflow: %v", err)
	}
	got := normalizeUUIDs(mustGenericJSON(t, gotBytes))

	if !reflect.DeepEqual(ref, got) {
		t.Errorf("generated workflow does not match reference.\nref:  %#v\ngot:  %#v", ref, got)
	}
}

// TestBuildWorkflowVariants checks the two fields that vary per shelp
// provider variant: WFLLMModel and WFAllowWebSearch, plus that the two
// actions share one UUID (the output action's OutputUUID must reference
// the askllm action's UUID) and that FollowUp is always false.
func TestBuildWorkflowVariants(t *testing.T) {
	for _, tc := range []struct {
		model string
		web   bool
	}{
		{"Apple Intelligence", false},
		{"Apple Intelligence", true},
		{"Apple Intelligence Pro", false},
		{"Apple Intelligence Pro", true},
		{"ChatGPT", false},
		{"ChatGPT", true},
	} {
		wf := buildWorkflow(tc.model, tc.web)
		if len(wf.WFWorkflowActions) != 2 {
			t.Fatalf("%s/web=%v: got %d actions, want 2", tc.model, tc.web, len(wf.WFWorkflowActions))
		}
		ask, ok := wf.WFWorkflowActions[0].WFWorkflowActionParameters.(askLLMParams)
		if !ok {
			t.Fatalf("%s/web=%v: action 0 params are %T, want askLLMParams", tc.model, tc.web, wf.WFWorkflowActions[0].WFWorkflowActionParameters)
		}
		if ask.WFLLMModel != tc.model {
			t.Errorf("%s/web=%v: WFLLMModel = %q", tc.model, tc.web, ask.WFLLMModel)
		}
		if ask.WFAllowWebSearch != tc.web {
			t.Errorf("%s/web=%v: WFAllowWebSearch = %v", tc.model, tc.web, ask.WFAllowWebSearch)
		}
		if ask.FollowUp {
			t.Errorf("%s/web=%v: FollowUp = true, want false", tc.model, tc.web)
		}
		if ask.UUID == "" {
			t.Errorf("%s/web=%v: empty UUID", tc.model, tc.web)
		}
		out, ok := wf.WFWorkflowActions[1].WFWorkflowActionParameters.(outputParams)
		if !ok {
			t.Fatalf("%s/web=%v: action 1 params are %T, want outputParams", tc.model, tc.web, wf.WFWorkflowActions[1].WFWorkflowActionParameters)
		}
		gotOut := out.WFOutput.Value.AttachmentsByRange["{0, 1}"].OutputUUID
		if gotOut != ask.UUID {
			t.Errorf("%s/web=%v: output OutputUUID %q != askllm UUID %q", tc.model, tc.web, gotOut, ask.UUID)
		}
	}
}

func TestNewUUIDLooksLikeAUUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := newUUID()
		if !uuidLikeRE.MatchString(u) {
			t.Fatalf("newUUID() = %q, does not look like a UUID", u)
		}
		if u != strings.ToUpper(u) {
			t.Errorf("newUUID() = %q, want upper-case", u)
		}
		if seen[u] {
			t.Fatalf("newUUID() produced a duplicate: %q", u)
		}
		seen[u] = true
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"shelp · Apple Intelligence Pro + Web": "shelp · Apple Intelligence Pro + Web",
		"a/b":                                  "a-b",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGenerateShortcutPlutilRoundTrip runs the real generator pipeline
// (Go JSON -> plutil -> shortcuts sign) and checks that the intermediate
// unsigned plist -- when read back with `plutil -convert json` -- still
// has real JSON booleans (not "true"/"false" strings) and the U+FFFC
// attachment placeholder intact. Building and signing a shortcut file is
// silent; nothing is ever `open`ed, so no import dialog appears.
func TestGenerateShortcutPlutilRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	if _, ok := LookPath("plutil"); !ok {
		t.Skip("plutil not on PATH")
	}
	if _, ok := LookPath("shortcuts"); !ok {
		t.Skip("shortcuts not on PATH")
	}

	dir := t.TempDir()
	ctx := context.Background()
	name := "shelp · Test + Web"
	signedPath, err := GenerateShortcut(ctx, dir, name, "Apple Intelligence Pro", true)
	if err != nil {
		t.Fatalf("GenerateShortcut: %v", err)
	}
	info, err := os.Stat(signedPath)
	if err != nil {
		t.Fatalf("stat signed shortcut: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("signed shortcut is empty")
	}
	if got := signedPath[len(signedPath)-len(".shortcut"):]; got != ".shortcut" {
		t.Errorf("signed path %q does not end in .shortcut", signedPath)
	}
	base := sanitizeFilename(name)
	if !strings.Contains(signedPath, base) {
		t.Errorf("signed path %q does not derive from shortcut name %q (WFWorkflowName isn't set; the filename IS the name Shortcuts imports under)", signedPath, name)
	}

	unsignedPath := dir + "/" + base + ".unsigned.shortcut"
	res, err := RunCmd(ctx, nil, nil, "plutil", "-convert", "json", unsignedPath, "-o", "-")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("plutil -convert json roundtrip failed: err=%v exit=%d stderr=%s", err, res.ExitCode, res.Stderr)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
		t.Fatalf("unmarshal plutil roundtrip output: %v\n%s", err, res.Stdout)
	}
	actions, _ := got["WFWorkflowActions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("roundtrip: got %d actions, want 2", len(actions))
	}
	askParams, _ := actions[0].(map[string]any)["WFWorkflowActionParameters"].(map[string]any)

	if fu, ok := askParams["FollowUp"].(bool); !ok || fu {
		t.Errorf("FollowUp after plutil roundtrip = %#v (%T), want bool false", askParams["FollowUp"], askParams["FollowUp"])
	}
	if ws, ok := askParams["WFAllowWebSearch"].(bool); !ok || !ws {
		t.Errorf("WFAllowWebSearch after plutil roundtrip = %#v (%T), want bool true", askParams["WFAllowWebSearch"], askParams["WFAllowWebSearch"])
	}

	promptVal, _ := askParams["WFLLMPrompt"].(map[string]any)["Value"].(map[string]any)["string"].(string)
	if promptVal != extensionInputPlaceholder {
		t.Errorf("WFLLMPrompt string after plutil roundtrip = %q, want U+FFFC", promptVal)
	}
}

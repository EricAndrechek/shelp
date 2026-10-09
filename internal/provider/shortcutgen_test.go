package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// uuidLikeRE matches an 8-4-4-4-12 hex string, upper- or lower-case.
var uuidLikeRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// normalizeUUIDs walks a generic JSON tree (as produced by json.Unmarshal
// into `any`), object keys in sorted order, and replaces every UUID-shaped
// string with "<UUID-n>", n counting distinct UUIDs in first-seen order.
// Two workflows that differ only by their randomly generated UUIDs then
// compare equal -- and only if every reference points at the same action
// in both.
func normalizeUUIDs(v any) any {
	seen := map[string]string{}
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out := make(map[string]any, len(t))
			for _, k := range keys {
				out[k] = walk(t[k])
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, vv := range t {
				out[i] = walk(vv)
			}
			return out
		case string:
			if !uuidLikeRE.MatchString(t) {
				return t
			}
			if _, ok := seen[t]; !ok {
				seen[t] = fmt.Sprintf("<UUID-%d>", len(seen)+1)
			}
			return seen[t]
		default:
			return v
		}
	}
	return walk(v)
}

func mustGenericJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	return v
}

// genericActions marshals buildWorkflow() and returns its action array as
// generic JSON: what plutil actually gets.
func genericActions(t *testing.T) []map[string]any {
	t.Helper()
	data, err := json.Marshal(buildWorkflow())
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		WFWorkflowActions []map[string]any
	}
	if err := json.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	return wf.WFWorkflowActions
}

func params(a map[string]any) map[string]any {
	p, _ := a["WFWorkflowActionParameters"].(map[string]any)
	return p
}

func ident(a map[string]any) string {
	s, _ := a["WFWorkflowActionIdentifier"].(string)
	return strings.TrimPrefix(s, "is.workflow.actions.")
}

// TestBuildWorkflowMatchesReference checks buildWorkflow's JSON against
// testdata/shortcuts/reference.json, a reviewed copy of the format 5
// shortcut (see buildWorkflow's doc comment for the shape and shortcutgen.go
// for each action's source). UUIDs are normalized consistently, so every
// reference between actions is compared too.
func TestBuildWorkflowMatchesReference(t *testing.T) {
	refBytes, err := os.ReadFile("testdata/shortcuts/reference.json")
	if err != nil {
		t.Fatalf("read reference.json: %v", err)
	}
	ref := normalizeUUIDs(mustGenericJSON(t, refBytes))

	gotBytes, err := json.Marshal(buildWorkflow())
	if err != nil {
		t.Fatalf("marshal generated workflow: %v", err)
	}
	got := normalizeUUIDs(mustGenericJSON(t, gotBytes))

	if !reflect.DeepEqual(ref, got) {
		pretty, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("generated workflow does not match reference.json; normalized, it is:\n%s", pretty)
	}
}

// TestCurrentShortcutActionCount pins today's generator at 40 actions:
// Text + the marker If/Stop and Output/Otherwise (4), Get Dictionary from
// Input + 2 Get Dictionary Value + Text (4), six If/Use Model/Stop and
// Output/Otherwise branches (24), the fallback Stop and Output (1), and
// seven End Ifs.
func TestCurrentShortcutActionCount(t *testing.T) {
	if got := currentShortcutActionCount(); got != 40 {
		t.Errorf("currentShortcutActionCount() = %d, want 40", got)
	}
}

// outputNames is the output each referenced action type stores under.
var outputNames = map[string]string{
	"gettext":           outputNameText,
	"detect.dictionary": outputNameDictionary,
	"getvalueforkey":    outputNameDictionaryValue,
	"askllm":            outputNameResponse,
}

// TestBuildWorkflowWellFormed checks what Shortcuts' import would not
// catch and only a run would: every control-flow group opens, (at most
// once) branches, and closes, properly nested; every reference names an
// earlier action by its real output name; every If compares a Text
// action's output (an If on Shortcut Input itself failed every run, v3);
// every text token's attachments sit on U+FFFC placeholders.
func TestBuildWorkflowWellFormed(t *testing.T) {
	acts := genericActions(t)
	byUUID := map[string]string{} // UUID -> identifier, of actions seen so far
	type group struct{ mid, end int }
	var stack []string
	groups := map[string]*group{}

	checkRef := func(i int, att map[string]any) {
		t.Helper()
		switch att["Type"] {
		case "ExtensionInput":
		case "ActionOutput":
			id, _ := att["OutputUUID"].(string)
			src, ok := byUUID[id]
			if !ok {
				t.Errorf("action %d references %q, not an earlier action's UUID", i, id)
				return
			}
			if want := outputNames[src]; att["OutputName"] != want {
				t.Errorf("action %d references %s's output as %q, want %q", i, src, att["OutputName"], want)
			}
		default:
			t.Errorf("action %d: unexpected attachment %v", i, att)
		}
	}
	checkToken := func(i int, tok any) {
		t.Helper()
		m, _ := tok.(map[string]any)
		if m["WFSerializationType"] != "WFTextTokenString" {
			t.Errorf("action %d: token %v is not a WFTextTokenString", i, tok)
			return
		}
		v, _ := m["Value"].(map[string]any)
		s, _ := v["string"].(string)
		units := utf16.Encode([]rune(s))
		atts, _ := v["attachmentsByRange"].(map[string]any)
		placeholders := 0
		for _, u := range units {
			if u == 0xFFFC {
				placeholders++
			}
		}
		if placeholders != len(atts) {
			t.Errorf("action %d: %d placeholders, %d attachments in %q", i, placeholders, len(atts), s)
		}
		for rng, att := range atts {
			var off int
			if _, err := fmt.Sscanf(rng, "{%d, 1}", &off); err != nil || off >= len(units) || units[off] != 0xFFFC {
				t.Errorf("action %d: attachment range %q isn't on a placeholder in %q", i, rng, s)
			}
			checkRef(i, att.(map[string]any))
		}
	}
	checkAttachment := func(i int, v any) {
		t.Helper()
		m, _ := v.(map[string]any)
		if m["WFSerializationType"] != "WFTextTokenAttachment" {
			t.Errorf("action %d: %v is not a WFTextTokenAttachment", i, v)
			return
		}
		checkRef(i, m["Value"].(map[string]any))
	}

	for i, a := range acts {
		p := params(a)
		switch id := ident(a); id {
		case "gettext":
			checkToken(i, p["WFTextActionText"])
		case "detect.dictionary":
			checkAttachment(i, p["WFInput"])
		case "getvalueforkey":
			checkAttachment(i, p["WFInput"])
			if k, _ := p["WFDictionaryKey"].(string); k != "model" && k != "prompt" {
				t.Errorf("action %d: WFDictionaryKey %q", i, k)
			}
		case "askllm":
			checkToken(i, p["WFLLMPrompt"])
		case "output":
			checkToken(i, p["WFOutput"])
		case "conditional":
			g, _ := p["GroupingIdentifier"].(string)
			mode, ok := p["WFControlFlowMode"].(float64)
			if !ok || mode != float64(int(mode)) {
				t.Fatalf("action %d: WFControlFlowMode %v is not an integer", i, p["WFControlFlowMode"])
			}
			switch int(mode) {
			case wfControlFlowStart:
				if groups[g] != nil {
					t.Fatalf("action %d: GroupingIdentifier %s reused", i, g)
				}
				groups[g] = &group{mid: -1, end: -1}
				stack = append(stack, g)
				if p["WFCondition"] != float64(conditionIsEquals) || p["WFConditionalActionString"] == nil {
					t.Errorf("action %d: If is not `is <literal>`: %v", i, p)
				}
				in, _ := p["WFInput"].(map[string]any)
				if in["Type"] != "Variable" {
					t.Errorf("action %d: If WFInput lacks the Variable wrapper: %v", i, in)
				}
				checkAttachment(i, in["Variable"])
				att := in["Variable"].(map[string]any)["Value"].(map[string]any)
				if byUUID[att["OutputUUID"].(string)] != "gettext" {
					t.Errorf("action %d: If compares %v, not a Text action's output", i, att)
				}
			case wfControlFlowMiddle:
				if len(stack) == 0 || stack[len(stack)-1] != g || groups[g].mid >= 0 {
					t.Fatalf("action %d: Otherwise out of place (group %s, open %v)", i, g, stack)
				}
				if len(p) != 2 {
					t.Errorf("action %d: plain Otherwise carries extra fields: %v", i, p)
				}
				groups[g].mid = i
			case wfControlFlowEnd:
				if len(stack) == 0 || stack[len(stack)-1] != g {
					t.Fatalf("action %d: End If closes %s, but %v is open", i, g, stack)
				}
				stack = stack[:len(stack)-1]
				groups[g].end = i
			default:
				t.Fatalf("action %d: WFControlFlowMode %v", i, mode)
			}
		default:
			t.Fatalf("action %d: unexpected action %s", i, id)
		}
		if u, ok := p["UUID"].(string); ok {
			if _, dup := byUUID[u]; dup {
				t.Errorf("action %d: UUID %s reused", i, u)
			}
			byUUID[u] = ident(a)
		}
	}
	if len(stack) != 0 {
		t.Errorf("unclosed groups: %v", stack)
	}
	if len(groups) != len(shortcutVariants)+1 {
		t.Errorf("%d If groups, want %d (marker + one per variant)", len(groups), len(shortcutVariants)+1)
	}
}

// --- A tiny interpreter for the generated workflow ------------------------

// fakeModel stands in for Use Model in runWorkflow.
type fakeModel struct{ calls []string }

func (m *fakeModel) answer(model string, web bool, prompt string) string {
	r := fmt.Sprintf("model=%s web=%v prompt=%s", model, web, prompt)
	m.calls = append(m.calls, r)
	return r
}

// runWorkflow executes acts on input the way Shortcuts runs the actions
// buildWorkflow uses: Text renders its token; If compares its subject to
// the literal and, when false, continues after its Otherwise (or End If);
// reaching an Otherwise from the true branch skips to the End If; Get
// Dictionary from Input parses JSON text (anything but an object fails);
// Get Dictionary Value yields "" for a missing key; Use Model calls m;
// Stop and Output ends the run with its text. It checks the semantics of
// the shape, not the plist format (that is TestBuildWorkflowWellFormed's
// and, for real, a live run's job).
func runWorkflow(acts []map[string]any, input string, m *fakeModel) (string, error) {
	groups := map[string]map[int]int{} // GroupingIdentifier -> mode -> index
	for i, a := range acts {
		if ident(a) == "conditional" {
			p := params(a)
			g := p["GroupingIdentifier"].(string)
			if groups[g] == nil {
				groups[g] = map[int]int{}
			}
			groups[g][int(p["WFControlFlowMode"].(float64))] = i
		}
	}
	outputs := map[string]map[string]any{}
	resolve := func(att map[string]any) (any, error) {
		switch att["Type"] {
		case "ExtensionInput":
			return input, nil
		case "ActionOutput":
			v, ok := outputs[att["OutputUUID"].(string)][att["OutputName"].(string)]
			if !ok {
				return nil, fmt.Errorf("no value for %v", att)
			}
			return v, nil
		}
		return nil, fmt.Errorf("unknown attachment %v", att)
	}
	render := func(tok any) (string, error) {
		v := tok.(map[string]any)["Value"].(map[string]any)
		units := utf16.Encode([]rune(v["string"].(string)))
		atts := v["attachmentsByRange"].(map[string]any)
		var out []uint16
		for i, u := range units {
			att, ok := atts[fmt.Sprintf("{%d, 1}", i)]
			if !ok {
				out = append(out, u)
				continue
			}
			val, err := resolve(att.(map[string]any))
			if err != nil {
				return "", err
			}
			s, ok := val.(string)
			if !ok {
				return "", fmt.Errorf("can't render %T into text", val)
			}
			out = append(out, utf16.Encode([]rune(s))...)
		}
		return string(utf16.Decode(out)), nil
	}
	attached := func(v any) (any, error) {
		return resolve(v.(map[string]any)["Value"].(map[string]any))
	}
	set := func(p map[string]any, name string, v any) {
		u := p["UUID"].(string)
		if outputs[u] == nil {
			outputs[u] = map[string]any{}
		}
		outputs[u][name] = v
	}

	for pc := 0; pc < len(acts); pc++ {
		p := params(acts[pc])
		switch ident(acts[pc]) {
		case "gettext":
			s, err := render(p["WFTextActionText"])
			if err != nil {
				return "", err
			}
			set(p, outputNameText, s)
		case "conditional":
			g := groups[p["GroupingIdentifier"].(string)]
			switch int(p["WFControlFlowMode"].(float64)) {
			case wfControlFlowStart:
				subj, err := attached(p["WFInput"].(map[string]any)["Variable"])
				if err != nil {
					return "", err
				}
				if subj != p["WFConditionalActionString"] {
					if mid, ok := g[wfControlFlowMiddle]; ok {
						pc = mid
					} else {
						pc = g[wfControlFlowEnd]
					}
				}
			case wfControlFlowMiddle:
				pc = g[wfControlFlowEnd]
			}
		case "detect.dictionary":
			in, err := attached(p["WFInput"])
			if err != nil {
				return "", err
			}
			var d map[string]any
			if err := json.Unmarshal([]byte(in.(string)), &d); err != nil || d == nil {
				return "", fmt.Errorf("Get Dictionary from Input: %q is no dictionary", in)
			}
			set(p, outputNameDictionary, d)
		case "getvalueforkey":
			in, err := attached(p["WFInput"])
			if err != nil {
				return "", err
			}
			v, _ := in.(map[string]any)[p["WFDictionaryKey"].(string)].(string)
			set(p, outputNameDictionaryValue, v)
		case "askllm":
			prompt, err := render(p["WFLLMPrompt"])
			if err != nil {
				return "", err
			}
			set(p, outputNameResponse, m.answer(p["WFLLMModel"].(string), p["WFAllowWebSearch"].(bool), prompt))
		case "output":
			return render(p["WFOutput"])
		default:
			return "", fmt.Errorf("unknown action %s", ident(acts[pc]))
		}
	}
	return "", errors.New("ran off the end without Stop and Output")
}

func TestWorkflowAnswersMarkerWithoutModel(t *testing.T) {
	var m fakeModel
	got, err := runWorkflow(genericActions(t), shortcutVersionMarker, &m)
	if err != nil || got != shortcutVersionOutputText(ShortcutFormatVersion) {
		t.Fatalf("marker -> %q, %v; want %q", got, err, shortcutVersionOutputText(ShortcutFormatVersion))
	}
	if len(m.calls) != 0 {
		t.Errorf("the marker reached Use Model: %v", m.calls)
	}
	if v, err := parseShortcutMarkerReply(got); err != nil || v != ShortcutFormatVersion {
		t.Errorf("parseShortcutMarkerReply(%q) = %d, %v", got, v, err)
	}
}

// Each variant's request (as Generate encodes it) reaches exactly its own
// Use Model, with the prompt intact.
func TestWorkflowDispatchesEveryVariant(t *testing.T) {
	acts := genericActions(t)
	prompt := "You are shelp.\n\nfind \"big\" files <over> 100MB & sort them -- ünïcödé 🙂 \\ done"
	for _, v := range shortcutVariants {
		var m fakeModel
		body, err := shortcutRequestBody(v.key, prompt)
		if err != nil {
			t.Fatal(err)
		}
		got, err := runWorkflow(acts, body, &m)
		want := fmt.Sprintf("model=%s web=%v prompt=%s", v.model, v.web, prompt)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", v.id, got, err, want)
		}
		if len(m.calls) != 1 {
			t.Errorf("%s: Use Model ran %d times, want 1", v.id, len(m.calls))
		}
	}
}

func TestWorkflowUnknownModelFallsBack(t *testing.T) {
	acts := genericActions(t)
	for body, want := range map[string]string{
		`{"model":"gpt-9","prompt":"x"}`:     shortcutUnknownModelText + "gpt-9",
		`{"prompt":"x"}`:                     shortcutUnknownModelText,
		`{"model":"APPLE-PRO","prompt":"x"}`: shortcutUnknownModelText + "APPLE-PRO",
	} {
		var m fakeModel
		got, err := runWorkflow(acts, body, &m)
		if err != nil || got != want {
			t.Errorf("%s -> %q, %v; want %q", body, got, err, want)
		}
		if len(m.calls) != 0 {
			t.Errorf("%s reached Use Model: %v", body, m.calls)
		}
		if msg, ok := shortcutErrorReply(got); !ok || !strings.HasPrefix(msg, "unknown model") {
			t.Errorf("shortcutErrorReply(%q) = %q, %v", got, msg, ok)
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
// unsigned plist -- read back with `plutil -convert json` and `xml1` --
// still has real booleans (not "true"/"false" strings), integer
// WFControlFlowMode/WFCondition values, and the U+FFFC placeholders
// intact. Building and signing a shortcut file is silent; nothing is ever
// `open`ed, so no import dialog appears. `shortcuts sign` asks Apple's
// server and occasionally fails with an HTTP 500: rerun before concluding
// anything from a failure there.
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
	name := defaultShortcutName
	signedPath, err := GenerateShortcut(ctx, dir, name)
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
	if want := dir + "/" + name + ".shortcut"; signedPath != want {
		t.Errorf("signed path = %q, want %q (WFWorkflowName isn't set; the filename IS the name Shortcuts imports under)", signedPath, want)
	}

	unsignedPath := dir + "/" + name + ".unsigned.shortcut"
	res, err := RunCmd(ctx, nil, nil, "plutil", "-convert", "json", unsignedPath, "-o", "-")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("plutil -convert json roundtrip failed: err=%v exit=%d stderr=%s", err, res.ExitCode, res.Stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
		t.Fatalf("unmarshal plutil roundtrip output: %v\n%s", err, res.Stdout)
	}
	actions, _ := got["WFWorkflowActions"].([]any)
	if len(actions) != currentShortcutActionCount() {
		t.Fatalf("roundtrip: got %d actions, want %d", len(actions), currentShortcutActionCount())
	}
	webByModel := map[string]bool{}
	asks := 0
	for _, a := range actions {
		am := a.(map[string]any)
		if ident(am) != "askllm" {
			continue
		}
		asks++
		p := params(am)
		if fu, ok := p["FollowUp"].(bool); !ok || fu {
			t.Errorf("FollowUp after plutil roundtrip = %#v (%T), want bool false", p["FollowUp"], p["FollowUp"])
		}
		ws, ok := p["WFAllowWebSearch"].(bool)
		if !ok {
			t.Errorf("WFAllowWebSearch after plutil roundtrip = %#v (%T), want a bool", p["WFAllowWebSearch"], p["WFAllowWebSearch"])
		}
		webByModel[fmt.Sprintf("%s/%v", p["WFLLMModel"], ws)] = true
		promptVal, _ := p["WFLLMPrompt"].(map[string]any)["Value"].(map[string]any)["string"].(string)
		if promptVal != extensionInputPlaceholder {
			t.Errorf("WFLLMPrompt string after plutil roundtrip = %q, want U+FFFC", promptVal)
		}
	}
	if asks != len(shortcutVariants) || len(webByModel) != len(shortcutVariants) {
		t.Errorf("roundtrip: %d Use Model actions covering %v, want one per variant", asks, webByModel)
	}

	res, err = RunCmd(ctx, nil, nil, "plutil", "-convert", "xml1", unsignedPath, "-o", "-")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("plutil -convert xml1 failed: err=%v exit=%d stderr=%s", err, res.ExitCode, res.Stderr)
	}
	for _, key := range []string{"WFControlFlowMode", "WFCondition"} {
		all := regexp.MustCompile(`<key>`+key+`</key>\s*<(\w+)>`).FindAllStringSubmatch(res.Stdout, -1)
		if len(all) == 0 {
			t.Errorf("no %s in the plist", key)
		}
		for _, m := range all {
			if m[1] != "integer" {
				t.Errorf("%s stored as <%s>, want <integer>", key, m[1])
			}
		}
	}
	conds := 0
	for _, a := range actions {
		if ident(a.(map[string]any)) == "conditional" {
			conds++
		}
	}
	// If, Otherwise and End If for the marker and for each variant.
	if want := 3 * (len(shortcutVariants) + 1); conds != want {
		t.Errorf("%d conditional actions, want %d", conds, want)
	}
}

// Apple's signing server fails now and then; signShortcut retries.
func TestSignShortcutRetries(t *testing.T) {
	origCmd, origDelays := signCmd, signRetryDelays
	t.Cleanup(func() { signCmd, signRetryDelays = origCmd, origDelays })
	signRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}

	calls := 0
	signCmd = func(ctx context.Context, in, out string) (CmdResult, error) {
		calls++
		if calls < 3 {
			return CmdResult{ExitCode: 1, Stderr: "Error: The file doesn’t exist."}, nil
		}
		return CmdResult{}, nil
	}
	if err := signShortcut(context.Background(), "in", "out"); err != nil || calls != 3 {
		t.Fatalf("got %v after %d calls; want success on the third", err, calls)
	}

	calls = 0
	signCmd = func(ctx context.Context, in, out string) (CmdResult, error) {
		calls++
		return CmdResult{ExitCode: 1, Stderr: "Error: The file doesn’t exist."}, nil
	}
	err := signShortcut(context.Background(), "in", "out")
	if err == nil || calls != 3 || !strings.Contains(err.Error(), "3 times") || !strings.Contains(err.Error(), "doesn’t exist") {
		t.Fatalf("got %v after %d calls; want a 3-attempt failure naming the last error", err, calls)
	}
}

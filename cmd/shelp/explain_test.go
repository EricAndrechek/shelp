package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/style"
)

var oscRE = regexp.MustCompile(`\x1b\]8;;[^\x1b]*\x1b\\`)

// strip removes SGR codes and OSC 8 hyperlinks.
func strip(s string) string { return style.Strip(oscRE.ReplaceAllString(s, "")) }

const sampleExplain = "lsof lists open files. -n and -P skip lookups, (-iTCP:8080) selects TCP port 8080; it permanently Deletes nothing.\nBe careful with rm: it removes files.\n"

const testWidth = 40

func highlight(p style.Palette, chunks []string) string {
	var b bytes.Buffer
	h := newExplainHighlighter(&b, p, "lsof -nP -iTCP:8080", testWidth+1)
	for _, c := range chunks {
		h.Write(c)
	}
	h.Flush()
	return b.String()
}

func TestExplainChunkingInvariant(t *testing.T) {
	p := style.Forced(style.TrueColor)
	whole := highlight(p, []string{sampleExplain})
	var tiny []string
	for i := 0; i < len(sampleExplain); i += 3 {
		tiny = append(tiny, sampleExplain[i:min(i+3, len(sampleExplain))])
	}
	if got := highlight(p, tiny); got != whole {
		t.Errorf("chunked output differs:\n%q\n%q", got, whole)
	}
	var one []string
	for i := 0; i < len(sampleExplain); i++ {
		one = append(one, sampleExplain[i:i+1])
	}
	if got := highlight(p, one); got != whole {
		t.Errorf("byte-chunked output differs")
	}
}

// Raw byte reads can split a multi-byte character across chunks; the
// highlighter must not turn the halves into U+FFFD.
func TestExplainSplitMultibyteRune(t *testing.T) {
	p := style.Forced(style.TrueColor)
	text := "lsof — lists files… -n skips “DNS” lookups.\n"
	whole := highlight(p, []string{text})
	var one []string
	for i := 0; i < len(text); i++ {
		one = append(one, text[i:i+1])
	}
	got := highlight(p, one)
	if got != whole {
		t.Errorf("byte-chunked output differs:\n%q\n%q", got, whole)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("split rune became U+FFFD: %q", got)
	}
}

func TestExplainStyling(t *testing.T) {
	p := style.Forced(style.Color16)
	out := highlight(p, []string{sampleExplain})
	for _, want := range []string{
		p.Accent("lsof"), p.Accent("-n"), p.Accent("-P"), p.Accent("-iTCP:8080") + ")",
		p.Bold(p.Warn("permanently")), p.Bold(p.Warn("Deletes")), p.Bold(p.Warn("careful")),
		p.Bold(p.Warn("removes")), "(" + p.Accent("-iTCP:8080"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, p.Accent("rm")) {
		t.Error("rm is not in the command; must not be accented")
	}
	got := strings.Join(strings.Fields(strip(out)), " ")
	want := strings.Join(strings.Fields("$ lsof -nP -iTCP:8080 "+sampleExplain+" man: lsof"), " ")
	if got != want {
		t.Errorf("stripped words = %q\nwant %q", got, want)
	}
}

// The body is indented and word-wrapped: no line is wider than the wrap
// column, no word is split, and the text's own newlines are kept.
func TestExplainWraps(t *testing.T) {
	out := strip(highlight(style.Forced(style.TrueColor), []string{sampleExplain}))
	lines := strings.Split(out, "\n")
	if lines[0] != "$ lsof -nP -iTCP:8080" || lines[1] != "" {
		t.Fatalf("header = %q, %q; want the command, then a blank line", lines[0], lines[1])
	}
	body := lines[2:]
	for _, l := range body {
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, explainIndent) || strings.HasPrefix(l, explainIndent+" ") {
			t.Errorf("line %q: want exactly the indent", l)
		}
		if n := style.Width(l); n > testWidth {
			t.Errorf("line %q is %d wide, want <= %d", l, n, testWidth)
		}
	}
	if len(body) < 4 {
		t.Errorf("want the text wrapped over several lines at width %d:\n%s", testWidth, out)
	}
	// "...nothing." ends the text's first line; "Be careful" starts a new one.
	found := false
	for i, l := range body {
		if strings.HasSuffix(l, "nothing.") && i+1 < len(body) && strings.HasPrefix(body[i+1], explainIndent+"Be careful") {
			found = true
		}
	}
	if !found {
		t.Errorf("the text's own newline was lost:\n%s", out)
	}
}

func TestExplainLongWordNotBroken(t *testing.T) {
	long := strings.Repeat("x", testWidth+10)
	out := strip(highlight(style.Forced(style.Color16), []string{"see " + long + " there"}))
	if !strings.Contains(out, explainIndent+long+"\n") && !strings.Contains(out, explainIndent+long+" ") {
		t.Errorf("a word wider than the line must go out whole on its own line:\n%s", out)
	}
}

func TestExplainPlainParity(t *testing.T) {
	var b bytes.Buffer
	h := newExplainHighlighter(&b, style.Plain(), "lsof -nP", 0)
	h.Write("a -l remov")
	h.Write("e it")
	h.Flush()
	// The caller only uses the highlighter on a terminal; with a plain
	// palette and no width it still lays out, just without color or wrap.
	if got := b.String(); got != "$ lsof -nP\n\n  a -l remove it\n\n  man: lsof\n" {
		t.Errorf("got %q", got)
	}
}

const mdSample = "Shows disk usage of each item here, sorted by size.\n\n" +
	"- `-s` — summarize each argument\n" +
	"- `-h` — human-readable sizes, which makes this bullet long enough to wrap around\n" +
	"- `sort -h` — orders *numerically* by **size**\n" +
	"1. first step\n" +
	"**Warning:** this removes everything. See [docs](https://example.com/x) and [bad](file:///etc).\n\n\n"

func renderMD(p style.Palette, width int, cmd string, chunks []string) string {
	var b bytes.Buffer
	h := newExplainHighlighter(&b, p, cmd, width)
	for _, c := range chunks {
		h.Write(c)
	}
	h.Flush()
	return b.String()
}

func TestMarkdownRendering(t *testing.T) {
	p := style.Forced(style.Color16)
	out := renderMD(p, 41, "du -sh * | sort -h", []string{mdSample})
	plain := strip(out)
	for _, want := range []string{
		"  Shows disk usage of each item here,\n  sorted by size.\n\n",
		"  • -s — summarize each argument\n",
		"  • -h — human-readable sizes, which\n    makes this bullet long enough to\n    wrap around\n", // hanging indent
		"  • sort -h — orders numerically by size\n",
		"  1. first step\n",
		"  Warning: this removes everything. See\n  docs and bad.\n",
		"\n  man: du · sort\n",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "`") || strings.Contains(plain, "**") {
		t.Errorf("markup left in output:\n%s", plain)
	}
	for _, want := range []string{
		p.Accent("-s"), p.Accent("sort -h"), "\x1b[3m", p.Bold("size"),
		p.Bold(p.Warn("Warning:")), p.Warn("this"), p.Accent("•"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing styled %q in %q", want, out)
		}
	}
	// the link has a real URL; the file: one is plain text
	if !strings.Contains(out, "\x1b]8;;https://example.com/x\x1b\\") || strings.Contains(out, "file:///") {
		t.Errorf("links wrong: %q", out)
	}
	if strings.HasSuffix(plain, "\n\n") || strings.Count(plain, "\n\n\n") > 0 {
		t.Errorf("extra blank lines: %q", plain)
	}
	for _, l := range strings.Split(plain, "\n") {
		if style.Width(l) > 40 {
			t.Errorf("line %q wider than 40", l)
		}
	}
}

func TestMarkdownHeading(t *testing.T) {
	p := style.Forced(style.Color16)
	out := renderMD(p, 0, "ls", []string{"## What it does\nLists files.\n"})
	if !strings.Contains(out, p.Bold(p.Accent("What"))) || !strings.Contains(strip(out), "  What it does\n  Lists files.\n") {
		t.Errorf("heading: %q", out)
	}
}

func TestMarkdownChunkInvariant(t *testing.T) {
	p := style.Forced(style.TrueColor)
	text := mdSample + "naïve **café — “x”** ok\n"
	whole := renderMD(p, 41, "du -sh * | sort -h", []string{text})
	for _, n := range []int{1, 2, 3, 7} {
		var chunks []string
		for i := 0; i < len(text); i += n {
			chunks = append(chunks, text[i:min(i+n, len(text))])
		}
		got := renderMD(p, 41, "du -sh * | sort -h", chunks)
		if got != whole {
			t.Errorf("chunk size %d differs:\n%q\n%q", n, got, whole)
		}
		if strings.ContainsRune(got, '�') {
			t.Errorf("chunk size %d produced U+FFFD", n)
		}
	}
	// split exactly inside ** and inside the multi-byte "é"
	i := strings.Index(text, "**café")
	j := strings.Index(text, "é") + 1
	got := renderMD(p, 41, "du -sh * | sort -h", []string{text[:i+1], text[i+1 : j], text[j:]})
	if got != whole {
		t.Errorf("split inside ** / rune differs")
	}
}

func TestMarkdownPlainParagraphFallback(t *testing.T) {
	text := "This command lists files. It reads only, so it is safe to run and does nothing else at all."
	out := strip(renderMD(style.Forced(style.Color16), 41, "ls", []string{text}))
	body := strings.TrimPrefix(out, "$ ls\n\n")
	if !strings.HasPrefix(body, "  This command lists files. It reads\n  only, so it is safe") {
		t.Errorf("fallback wrapping: %q", out)
	}
	if !strings.HasSuffix(body, "\n\n  man: ls\n") {
		t.Errorf("tail: %q", out)
	}
}

func TestHeaderManLinks(t *testing.T) {
	p := style.Forced(style.Color16)
	out := renderMD(p, 0, "FOO=1 du -sh \"a b\" | sort -h && echo x; ls 2>&1 | head", []string{"ok\n"})
	for _, w := range []string{"du", "sort", "echo", "ls", "head"} {
		if !strings.Contains(out, "\x1b]8;;x-man-page://"+w+"\x1b\\") {
			t.Errorf("no man link for %s in %q", w, out)
		}
	}
	for _, bad := range []string{"x-man-page://FOO", "x-man-page://-sh", "x-man-page://1", "x-man-page://x"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected link %s", bad)
		}
	}
	if !strings.HasSuffix(strip(out), "man: du · sort · echo · ls · head\n") {
		t.Errorf("footer: %q", strip(out))
	}
	// no command word -> no footer, one trailing newline
	if got := strip(renderMD(p, 0, "FOO=1", []string{"ok\n"})); got != "$ FOO=1\n\n  ok\n" {
		t.Errorf("no-footer output %q", got)
	}
	// a plain palette emits no escapes at all
	if got := renderMD(style.Plain(), 0, "ls | wc", []string{"ok"}); strings.Contains(got, "\x1b") {
		t.Errorf("plain palette emitted escapes: %q", got)
	}
}

func TestSplitCommand(t *testing.T) {
	got := commandWords("sudo rm -rf 'a | b' || cat f > out; (cd x && make)")
	want := []string{"sudo", "cat", "make"} // rm follows sudo (not a segment start); cd is a keyword
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("commandWords = %v, want %v", got, want)
	}
}

// Off a terminal the model's text must reach stdout untouched.
func TestNonTerminalOutputIsRawModelText(t *testing.T) {
	raw := "Shows disk usage.\n\n- `-s` — summarize\n- **bold** and [l](https://x.io)\n\n**Warning:** careful"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Stream bool }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, part := range []string{raw[:10], raw[10:30], raw[30:]} {
				b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": part}}}})
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": raw}}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.toml")
	os.WriteFile(cfgPath, []byte(fmt.Sprintf("providers = [\"fake:m\"]\n[[openai.endpoint]]\nname = \"fake\"\nbase_url = %q\napi_key_env = \"SHELP_FAKE_KEY\"\nmodels = [\"m\"]\n", srv.URL)), 0o644)
	t.Setenv("SHELP_CONFIG", cfgPath)
	t.Setenv("SHELP_FAKE_KEY", "k")
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("CLICOLOR_FORCE", "")
	var out, errb bytes.Buffer
	if err := runGenerate(context.Background(), provider.Explain, []string{"du", "-sh"}, &out, &errb); err != nil {
		t.Fatalf("%v (stderr %q)", err, errb.String())
	}
	if out.String() != raw+"\n" {
		t.Errorf("stdout = %q, want raw text + newline", out.String())
	}
}

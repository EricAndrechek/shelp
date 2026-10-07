package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/style"
)

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
	got := strings.Join(strings.Fields(style.Strip(out)), " ")
	want := strings.Join(strings.Fields("$ lsof -nP -iTCP:8080 "+sampleExplain), " ")
	if got != want {
		t.Errorf("stripped words = %q\nwant %q", got, want)
	}
}

// The body is indented and word-wrapped: no line is wider than the wrap
// column, no word is split, and the text's own newlines are kept.
func TestExplainWraps(t *testing.T) {
	out := style.Strip(highlight(style.Forced(style.TrueColor), []string{sampleExplain}))
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
	out := style.Strip(highlight(style.Forced(style.Color16), []string{"see " + long + " there"}))
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
	if got := b.String(); got != "$ lsof -nP\n\n  a -l remove it" {
		t.Errorf("got %q", got)
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/style"
)

const sampleExplain = "lsof lists open files. -n and -P skip lookups, (-iTCP:8080) selects TCP port 8080; it permanently Deletes nothing.\nBe careful with rm: it removes files.\n"

func highlight(p style.Palette, chunks []string) string {
	var b bytes.Buffer
	h := newExplainHighlighter(&b, p, "lsof -nP -iTCP:8080")
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
	if style.Strip(out) != highlightPlainBody(sampleExplain) {
		t.Error("stripping colors should give the original text plus header")
	}
}

func highlightPlainBody(s string) string { return "lsof -nP -iTCP:8080\n" + s }

func TestExplainPlainParity(t *testing.T) {
	var b bytes.Buffer
	h := newExplainHighlighter(&b, style.Plain(), "lsof -nP")
	h.Write("a -l remov")
	h.Write("e it")
	h.Flush()
	// Header is only written by the caller when colored; a plain palette
	// would still emit it here, so the caller must not use the highlighter
	// at all -- but the body must be untouched.
	if got := strings.TrimPrefix(b.String(), "lsof -nP\n"); got != "a -l remove it" {
		t.Errorf("got %q", got)
	}
}

package style

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func clearColorEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"NO_COLOR", "CLICOLOR_FORCE", "TERM", "COLORTERM"} {
		t.Setenv(k, "")
	}
}

func TestEnabled(t *testing.T) {
	clearColorEnv(t)
	var buf bytes.Buffer
	if Enabled(&buf) {
		t.Error("a buffer is not a terminal: want plain")
	}
	t.Setenv("CLICOLOR_FORCE", "1")
	if !Enabled(&buf) {
		t.Error("CLICOLOR_FORCE=1: want color even for a buffer")
	}
	t.Setenv("NO_COLOR", "1")
	if Enabled(&buf) {
		t.Error("NO_COLOR wins over CLICOLOR_FORCE")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "0")
	if Enabled(&buf) {
		t.Error("CLICOLOR_FORCE=0 does not force")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Enabled(f) {
		t.Error("a regular file is not a terminal: want plain")
	}
}

func TestDetectDepth(t *testing.T) {
	clearColorEnv(t)
	if got := DetectDepth(); got != Color16 {
		t.Errorf("no hints: got %v, want Color16", got)
	}
	t.Setenv("TERM", "xterm-256color")
	if got := DetectDepth(); got != Color256 {
		t.Errorf("TERM=xterm-256color: got %v, want Color256", got)
	}
	t.Setenv("COLORTERM", "truecolor")
	if got := DetectDepth(); got != TrueColor {
		t.Errorf("COLORTERM=truecolor: got %v, want TrueColor", got)
	}
}

func TestPlainPaletteIsIdentity(t *testing.T) {
	p := Plain()
	for _, f := range []func(string) string{p.Bold, p.Dim, p.OK, p.Warn, p.Err, p.Accent,
		func(s string) string { return p.Gradient(s, BrandStart, BrandEnd) }} {
		if got := f("ls -la"); got != "ls -la" {
			t.Errorf("plain palette changed text: %q", got)
		}
	}
}

func TestColorsStripBackToText(t *testing.T) {
	for _, d := range []Depth{Color16, Color256, TrueColor} {
		p := Forced(d)
		for _, s := range []string{p.Bold("x y"), p.OK("x y"), p.Accent("x y"), p.Gradient("x y", BrandStart, BrandEnd)} {
			if !strings.Contains(s, "\x1b[") {
				t.Errorf("depth %v: %q has no color code", d, s)
			}
			if got := Strip(s); got != "x y" {
				t.Errorf("depth %v: Strip(%q) = %q, want %q", d, s, got, "x y")
			}
			if w := Width(s); w != 3 {
				t.Errorf("depth %v: Width(%q) = %d, want 3", d, s, w)
			}
		}
	}
}

func TestGradientEndpoints(t *testing.T) {
	s := Forced(TrueColor).Gradient("ab", RGB{0, 0, 0}, RGB{255, 255, 255})
	if !strings.HasPrefix(s, "\x1b[38;2;0;0;0ma") || !strings.Contains(s, "\x1b[38;2;255;255;255mb") {
		t.Errorf("gradient endpoints wrong: %q", s)
	}
}

func TestTo256(t *testing.T) {
	for c, want := range map[RGB]int{{0, 0, 0}: 16, {255, 255, 255}: 231, {255, 0, 0}: 196, {0, 95, 135}: 24} {
		if got := To256(c); got != want {
			t.Errorf("To256(%v) = %d, want %d", c, got, want)
		}
	}
}

func TestPadRight(t *testing.T) {
	p := Forced(TrueColor)
	if got := Width(PadRight(p.OK("ok"), 6)); got != 6 {
		t.Errorf("padded width = %d, want 6", got)
	}
	if got := PadRight("toolong", 3); got != "toolong" {
		t.Errorf("PadRight never truncates: got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("hello world", 8); got != "hello w…" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("short", 8); got != "short" {
		t.Errorf("got %q", got)
	}
	p := Forced(Color16)
	got := Truncate(p.Dim("hello world"), 6)
	if Strip(got) != "hello…" || Width(got) != 6 {
		t.Errorf("got %q", got)
	}
	if Truncate("abc", 0) != "abc" {
		t.Error("max 0 must not truncate")
	}
}

func TestTermWidthNotTerminal(t *testing.T) {
	if w := TermWidth(&strings.Builder{}); w != 0 {
		t.Errorf("TermWidth(non-file) = %d", w)
	}
}

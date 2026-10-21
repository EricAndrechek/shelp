package style

import (
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	got := Wrap("  one two three four five", 12, "    ")
	want := []string{"  one two", "    three", "    four", "    five"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("plain: %q", got)
	}
	if got := Wrap("fits", 10, ""); len(got) != 1 || got[0] != "fits" {
		t.Errorf("fits: %q", got)
	}
	if got := Wrap("a verylongwordindeed b", 6, ""); strings.Join(got, "|") != "a|verylongwordindeed|b" {
		t.Errorf("long word: %q", got)
	}
	// A color open at a break is closed, then reopened after the indent;
	// stripped, the lines are the plain wrap.
	p := Forced(Color16)
	s := "x " + p.Warn("one two three") + " " + p.Dim("four")
	got = Wrap(s, 9, "  ")
	plain := Wrap(Strip(s), 9, "  ")
	for i := range got {
		if Strip(got[i]) != plain[i] {
			t.Errorf("line %d: %q, plain %q", i, Strip(got[i]), plain[i])
		}
		if Width(got[i]) > 9 {
			t.Errorf("line %d too wide: %q", i, got[i])
		}
	}
	if len(got) < 2 || !strings.HasSuffix(got[0], "\x1b[0m") || !strings.HasPrefix(got[1], "  \x1b[33m") {
		t.Errorf("colors not carried over: %q", got)
	}
}

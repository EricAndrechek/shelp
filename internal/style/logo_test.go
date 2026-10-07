package style

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogoGlyphs(t *testing.T) {
	for _, r := range logoText {
		g := logoGlyph(r)
		w := len(g[0])
		for y, row := range g {
			if len(row) != w {
				t.Errorf("%c row %d: width %d, want %d", r, y, len(row), w)
			}
			if strings.Trim(row, "#.") != "" {
				t.Errorf("%c row %d: %q has characters other than '#' and '.'", r, y, row)
			}
		}
	}
}

func TestLogoPlain(t *testing.T) {
	logo := Logo(Plain())
	if strings.Contains(logo, "\x1b") {
		t.Fatalf("plain logo has escape codes: %q", logo)
	}
	if strings.HasSuffix(logo, "\n") {
		t.Error("logo ends with a newline")
	}
	lines := strings.Split(logo, "\n")
	if len(lines) != logoHeight/4 {
		t.Fatalf("got %d lines, want %d", len(lines), logoHeight/4)
	}
	want := Width(lines[0])
	if want == 0 || want > 40 {
		t.Errorf("logo is %d columns wide, want 1-40", want)
	}
	for i, l := range lines {
		if w := Width(l); w != want {
			t.Errorf("line %d: width %d, want %d", i, w, want)
		}
		for _, r := range l {
			if r < 0x2800 || r > 0x28FF {
				t.Errorf("line %d: %U is not a Braille pattern", i, r)
			}
		}
	}
}

func TestLogoColored(t *testing.T) {
	plain := Logo(Plain())
	for _, d := range []Depth{Color16, Color256, TrueColor} {
		colored := Logo(Forced(d))
		if !strings.Contains(colored, "\x1b[") {
			t.Errorf("depth %v: no color codes", d)
		}
		if got := Strip(colored); got != plain {
			t.Errorf("depth %v: Strip(colored) != plain:\n%s\nwant:\n%s", d, got, plain)
		}
		for i, l := range strings.Split(colored, "\n") {
			if w, want := Width(l), utf8.RuneCountInString(strings.Split(plain, "\n")[i]); w != want {
				t.Errorf("depth %v line %d: width %d, want %d", d, i, w, want)
			}
		}
	}
}

// TestLogoGradient checks that color follows the column: the first and
// last columns get the brand's two ends, and every row of a column the
// same color.
func TestLogoGradient(t *testing.T) {
	p := Forced(TrueColor)
	lines := strings.Split(Logo(p), "\n")
	cols := Width(lines[0])
	colorAt := map[int]string{} // column -> SGR code seen there
	for i, l := range lines {
		col := 0
		for len(l) > 0 {
			code := ""
			if strings.HasPrefix(l, "\x1b[") {
				end := strings.IndexByte(l, 'm')
				code, l = l[:end+1], l[end+1:]
			}
			r, n := utf8.DecodeRuneInString(l)
			l = l[n:]
			if r == 0x2800 {
				if code != "" {
					t.Errorf("line %d col %d: blank cell is colored", i, col)
				}
				col++
				continue
			}
			if code == "" {
				t.Fatalf("line %d col %d: %c has no color", i, col, r)
			}
			if prev, ok := colorAt[col]; ok && prev != code {
				t.Errorf("col %d: line %d is %q, an earlier line %q", col, i, code, prev)
			}
			colorAt[col] = code
			l = strings.TrimPrefix(l, "\x1b[0m")
			col++
		}
	}
	first := "\x1b[" + p.fg(BrandStart) + "m"
	last := "\x1b[" + p.fg(BrandEnd) + "m"
	if colorAt[0] != first {
		t.Errorf("column 0: %q, want BrandStart %q", colorAt[0], first)
	}
	if got := colorAt[cols-1]; got != last {
		t.Errorf("last column: %q, want BrandEnd %q", got, last)
	}
}

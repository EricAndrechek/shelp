// Package style colors shelp's human-facing output. A Palette colors only
// when its writer is a terminal: never when NO_COLOR is set (no-color.org)
// or TERM is "dumb", always when CLICOLOR_FORCE is set to anything but
// "0". Output meant for a program or for the command line -- the
// suggested command, --json, `shelp init` -- never goes through it.
//
// Semantic colors (OK, Warn, Err, Dim, Bold) use the terminal's own
// 16-color palette so they follow the user's theme; only the brand colors
// (Accent, Gradient) are fixed RGB, sent as 24-bit when the terminal says
// it supports it (COLORTERM) and as the nearest 256-color otherwise.
package style

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RGB is a 24-bit color.
type RGB struct{ R, G, B uint8 }

// shelp's brand gradient: the logo fades from BrandStart to BrandEnd, and
// Accent is BrandStart.
var (
	BrandStart = RGB{0, 196, 180}   // teal
	BrandEnd   = RGB{150, 100, 255} // violet
)

// Depth is how many colors a terminal can show.
type Depth int

const (
	Color16 Depth = iota
	Color256
	TrueColor
)

// Palette formats text for one writer. The zero value is plain: every
// method returns its input unchanged.
type Palette struct {
	on    bool
	depth Depth
}

// For returns the Palette for w, deciding from the environment and whether
// w is a terminal (see the package doc).
func For(w io.Writer) Palette {
	if !Enabled(w) {
		return Palette{}
	}
	return Palette{on: true, depth: DetectDepth()}
}

// Plain returns a Palette that never colors.
func Plain() Palette { return Palette{} }

// Forced returns a coloring Palette regardless of the environment, for
// tests and previews.
func Forced(d Depth) Palette { return Palette{on: true, depth: d} }

// Enabled reports whether output to w should be colored.
func Enabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// DetectDepth reads the terminal's color depth from COLORTERM and TERM.
func DetectDepth() Depth {
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return TrueColor
	}
	if strings.Contains(os.Getenv("TERM"), "256color") {
		return Color256
	}
	return Color16
}

// On reports whether p colors at all.
func (p Palette) On() bool { return p.on }

// Depth is p's color depth (meaningless when !p.On()).
func (p Palette) Depth() Depth { return p.depth }

func (p Palette) sgr(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Palette) Bold(s string) string { return p.sgr("1", s) }
func (p Palette) Dim(s string) string  { return p.sgr("2", s) }

// OK, Warn and Err are the terminal theme's green, yellow and red.
func (p Palette) OK(s string) string   { return p.sgr("32", s) }
func (p Palette) Warn(s string) string { return p.sgr("33", s) }
func (p Palette) Err(s string) string  { return p.sgr("31", s) }

// Accent is shelp's brand color (cyan on a 16-color terminal), for
// headings and highlighted commands.
func (p Palette) Accent(s string) string {
	if p.on && p.depth == Color16 {
		return p.sgr("36", s)
	}
	return p.RGB(BrandStart, s)
}

// RGB colors s with c (or its nearest 256-color, or cyan on a 16-color
// terminal).
func (p Palette) RGB(c RGB, s string) string { return p.sgr(p.fg(c), s) }

func (p Palette) fg(c RGB) string {
	switch p.depth {
	case TrueColor:
		return "38;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
	case Color256:
		return "38;5;" + strconv.Itoa(To256(c))
	}
	return "36"
}

// Gradient colors each non-space rune of s along the line from `from` to
// `to`, one step per rune. On a 16-color terminal it is Accent instead.
func (p Palette) Gradient(s string, from, to RGB) string {
	if !p.on || s == "" {
		return s
	}
	if p.depth == Color16 {
		return p.Accent(s)
	}
	n := utf8.RuneCountInString(s)
	var b strings.Builder
	i := 0
	for _, r := range s {
		if r == ' ' {
			b.WriteRune(r)
		} else {
			t := 0.0
			if n > 1 {
				t = float64(i) / float64(n-1)
			}
			b.WriteString("\x1b[" + p.fg(Lerp(from, to, t)) + "m")
			b.WriteRune(r)
		}
		i++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// Lerp returns the color t (0..1) of the way from a to b.
func Lerp(a, b RGB, t float64) RGB {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return RGB{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B)}
}

// cubeLevels are the channel values of xterm's 6x6x6 color cube.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// To256 maps c to the nearest color in xterm's 256-color cube (16-231).
func To256(c RGB) int {
	idx := func(v uint8) int {
		best, bestD := 0, 1<<30
		for i, l := range cubeLevels {
			if d := (int(v) - l) * (int(v) - l); d < bestD {
				best, bestD = i, d
			}
		}
		return best
	}
	return 16 + 36*idx(c.R) + 6*idx(c.G) + idx(c.B)
}

var sgrRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Strip removes the color codes this package adds.
func Strip(s string) string { return sgrRE.ReplaceAllString(s, "") }

// Width is s's width in terminal columns, ignoring color codes (one column
// per rune: shelp's own text has no wide characters).
func Width(s string) int { return utf8.RuneCountInString(Strip(s)) }

// PadRight pads s with spaces to width visible columns. Use it instead of
// text/tabwriter for colored cells: tabwriter counts escape codes as text.
func PadRight(s string, width int) string {
	if w := Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

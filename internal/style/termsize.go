package style

import (
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// winsize mirrors the kernel's struct winsize (TIOCGWINSZ).
type winsize struct{ Row, Col, X, Y uint16 }

// TermWidth is the width in columns of the terminal w writes to: an ioctl
// on w's file descriptor, then $COLUMNS. It returns 0 when w is not a
// terminal or the width is unknown (callers then don't truncate).
func TermWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !isTerminal(f) {
		return 0
	}
	var ws winsize
	if ioctlWinsz(f.Fd(), &ws) && ws.Col > 0 {
		return int(ws.Col)
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 0
}

// Truncate shortens s (which may carry color codes) to at most max visible
// columns, replacing the cut tail with "…" and closing any open color. A
// max < 1 or an s that already fits is returned unchanged.
func Truncate(s string, max int) string {
	if max < 1 || Width(s) <= max {
		return s
	}
	var b strings.Builder
	cols, sawEsc := 0, false
	for i := 0; i < len(s); {
		if loc := sgrRE.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			sawEsc = true
			continue
		}
		if cols == max-1 {
			break
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += n
		cols++
	}
	b.WriteString("…")
	if sawEsc {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

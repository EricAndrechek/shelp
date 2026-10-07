// Package markdown is a tiny, forgiving Markdown reader for shelp's
// terminal explanations: block detection for one line (bullets, ordered
// items, headings) and an inline parser (`code`, **bold**, *em*, _em_,
// [text](url)). It knows nothing about terminals; callers decide how to
// draw the pieces. Anything it does not recognize stays literal text, so a
// model that ignores the requested format still reads fine.
package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BlockKind is the kind of one source line.
type BlockKind int

const (
	Paragraph BlockKind = iota
	Bullet
	Ordered
	Heading
	Fence // a ``` line; callers normally drop it
)

// Block is one classified source line.
type Block struct {
	Kind   BlockKind
	Indent int    // leading spaces in the source (nesting depth hint)
	Marker string // "-" / "*" / "+" for Bullet, "1." for Ordered
	Level  int    // Heading: number of '#'
	Text   string // the line with the block marker removed
}

// ParseBlock classifies a single line (no trailing newline).
func ParseBlock(line string) Block {
	line = strings.TrimRight(line, " \t\r")
	trim := strings.TrimLeft(line, " \t")
	indent := len(line) - len(trim)
	b := Block{Kind: Paragraph, Indent: indent, Text: trim}
	switch {
	case strings.HasPrefix(trim, "```"):
		b.Kind = Fence
	case len(trim) >= 2 && (trim[0] == '-' || trim[0] == '*' || trim[0] == '+') && trim[1] == ' ':
		b.Kind, b.Marker, b.Text = Bullet, trim[:1], strings.TrimLeft(trim[2:], " ")
	case trim[:min(len(trim), 1)] == "#":
		n := 0
		for n < len(trim) && trim[n] == '#' {
			n++
		}
		if n <= 6 && n < len(trim) && trim[n] == ' ' {
			b.Kind, b.Level, b.Text = Heading, n, strings.TrimSpace(strings.TrimRight(trim[n:], "# "))
		}
	default:
		n := 0
		for n < len(trim) && trim[n] >= '0' && trim[n] <= '9' {
			n++
		}
		if n > 0 && n <= 3 && n+1 < len(trim) && (trim[n] == '.' || trim[n] == ')') && trim[n+1] == ' ' {
			b.Kind, b.Marker, b.Text = Ordered, trim[:n]+".", strings.TrimLeft(trim[n+2:], " ")
		}
	}
	return b
}

// Span is a run of inline text with one style.
type Span struct {
	Text string
	Code bool
	Bold bool
	Em   bool
	URL  string // non-empty only for an http(s) link
}

// ParseInline splits s into styled spans. Adjacent spans with identical
// styling are merged.
func ParseInline(s string) []Span {
	return merge(parse(s, Span{}))
}

func merge(in []Span) []Span {
	var out []Span
	for _, sp := range in {
		if sp.Text == "" {
			continue
		}
		if n := len(out); n > 0 {
			last := out[n-1]
			if !last.Code && !sp.Code && last.Bold == sp.Bold && last.Em == sp.Em && last.URL == sp.URL {
				out[n-1].Text += sp.Text
				continue
			}
		}
		out = append(out, sp)
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func parse(s string, base Span) []Span {
	var out []Span
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			sp := base
			sp.Text = lit.String()
			out = append(out, sp)
			lit.Reset()
		}
	}
	prev := rune(0) // rune before s[i]
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		rest := s[i:]
		switch {
		case r == '`':
			if j := strings.IndexByte(rest[1:], '`'); j > 0 {
				flush()
				sp := base
				sp.Text, sp.Code = rest[1:1+j], true
				out = append(out, sp)
				i += j + 2
				prev = '`'
				continue
			}
		case strings.HasPrefix(rest, "**") || strings.HasPrefix(rest, "__"):
			d := rest[:2]
			if d == "__" && isWordRune(prev) {
				break
			}
			if j := strings.Index(rest[2:], d); j > 0 && !strings.HasPrefix(rest[2:], " ") {
				flush()
				inner := base
				inner.Bold = true
				out = append(out, parse(rest[2:2+j], inner)...)
				i += j + 4
				prev = rune(d[0])
				continue
			}
		case r == '*' || r == '_':
			if r == '_' && isWordRune(prev) {
				break
			}
			if len(rest) < 3 || rest[1] == ' ' || rest[1] == byte(r) {
				break
			}
			if j := closeEm(rest[1:], byte(r)); j > 0 {
				flush()
				inner := base
				inner.Em = true
				out = append(out, parse(rest[1:1+j], inner)...)
				i += j + 2
				prev = r
				continue
			}
		case r == '[':
			if j := strings.Index(rest, "]("); j > 1 {
				if k := strings.IndexByte(rest[j+2:], ')'); k >= 0 {
					flush()
					url := strings.TrimSpace(rest[j+2 : j+2+k])
					inner := base
					if isHTTP(url) {
						inner.URL = url
					}
					out = append(out, parse(rest[1:j], inner)...)
					i += j + 2 + k + 1
					prev = ')'
					continue
				}
			}
		}
		lit.WriteString(s[i : i+size])
		prev = r
		i += size
	}
	flush()
	return out
}

// closeEm finds the closing single delimiter d in s (the text after the
// opener): not preceded by a space, not doubled, and for '_' not followed
// by a word character. It returns its index, or -1.
func closeEm(s string, d byte) int {
	for j := 0; j < len(s); j++ {
		if s[j] == '`' { // don't look inside code spans
			if k := strings.IndexByte(s[j+1:], '`'); k >= 0 {
				j += k + 1
			}
			continue
		}
		if s[j] != d || (j > 0 && s[j-1] == ' ') {
			continue
		}
		if j+1 < len(s) && s[j+1] == d {
			j++
			continue
		}
		if d == '_' {
			if r, _ := utf8.DecodeRuneInString(s[j+1:]); j+1 < len(s) && isWordRune(r) {
				continue
			}
		}
		if j == 0 {
			return -1
		}
		return j
	}
	return -1
}

func isHTTP(u string) bool {
	l := strings.ToLower(u)
	return (strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")) && !strings.ContainsAny(u, " \x1b\a")
}

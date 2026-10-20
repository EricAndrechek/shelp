package config

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Quote renders s as a TOML basic string: backslash and double quote
// escaped, control characters as \uXXXX (TOML has no \x escape, so Go's
// %q is not safe for arbitrary text).
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f || r == utf8.RuneError:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Block is one [[name]] array-table entry in a config file's text: lines
// [Start, End) of the file split after each "\n". It starts at the comment
// lines directly above its header (they describe it) and ends before the
// next header that is not one of its own sub-tables ([name.sub] or
// [[name.sub]]), leaving out trailing blank lines and the comment lines
// that belong to that next header. Text is those lines joined.
type Block struct {
	Start, End int
	Text       string
}

// ArrayTableBlocks finds every [[name]] entry in src, e.g. name
// "openai.endpoint". Headers are matched with spaces removed, so
// "[[ openai.endpoint ]]" counts; a quoted key form does not.
func ArrayTableBlocks(src, name string) []Block {
	lines := splitLines(src)
	headers := headerLines(lines)
	want := "[[" + name + "]]"
	own := func(h string) bool {
		return h == want || strings.HasPrefix(h, "["+name+".") || strings.HasPrefix(h, "[["+name+".")
	}
	var out []Block
	for hi, i := range headers {
		if headerKey(lines[i]) != want {
			continue
		}
		end := len(lines)
		for _, j := range headers[hi+1:] {
			if !own(headerKey(lines[j])) || headerKey(lines[j]) == want {
				end = j
				break
			}
		}
		// Comments directly above the next header are that header's.
		if end < len(lines) {
			for end > i+1 && isComment(lines[end-1]) {
				end--
			}
		}
		for end > i+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		start := i
		for start > 0 && isComment(lines[start-1]) {
			start--
		}
		out = append(out, Block{Start: start, End: end, Text: strings.Join(lines[start:end], "")})
	}
	return out
}

// RemoveBlock returns src without b's lines, and without one of the blank
// lines that separated it from its neighbors.
func RemoveBlock(src string, b Block) string {
	lines := splitLines(src)
	if b.Start < 0 || b.End > len(lines) || b.Start >= b.End {
		return src
	}
	start, end := b.Start, b.End
	blank := func(i int) bool { return i >= 0 && i < len(lines) && strings.TrimSpace(lines[i]) == "" }
	switch {
	case blank(end):
		end++
	case blank(start-1) && (end == len(lines) || start == 1):
		start--
	}
	out := strings.Join(lines[:start], "") + strings.Join(lines[end:], "")
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return out
}

// AppendBlock returns src with block appended at the end, after one blank
// line, so a new [[table]] entry never lands inside an existing table's
// keys. block should end in "\n" (one is added if not).
func AppendBlock(src, block string) string {
	if !strings.HasSuffix(block, "\n") {
		block += "\n"
	}
	if strings.TrimSpace(src) == "" {
		return block
	}
	if !strings.HasSuffix(src, "\n") {
		src += "\n"
	}
	if !strings.HasSuffix(src, "\n\n") {
		src += "\n"
	}
	return src + block
}

func splitLines(src string) []string {
	lines := strings.SplitAfter(src, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func isComment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

// headerKey is a header line's [name] or [[name]], spaces and any trailing
// comment removed.
func headerKey(l string) string {
	t := strings.TrimSpace(l)
	if i := strings.LastIndex(t, "]"); i >= 0 {
		t = t[:i+1]
	}
	return strings.ReplaceAll(strings.ReplaceAll(t, " ", ""), "\t", "")
}

// headerLines returns the indexes of table header lines: a line starting
// with "[" outside any multi-line array or string.
func headerLines(lines []string) []int {
	var out []int
	depth := 0
	inML := "" // the delimiter of an open multi-line string
	for i, l := range lines {
		if inML != "" {
			if strings.Count(l, inML)%2 == 1 {
				inML = ""
			}
			continue
		}
		t := strings.TrimSpace(l)
		if depth == 0 && strings.HasPrefix(t, "[") {
			out = append(out, i)
			continue
		}
		for _, d := range []string{`"""`, `'''`} {
			if strings.Count(l, d)%2 == 1 {
				inML = d
			}
		}
		if inML == "" {
			depth = scanDepth(l, depth)
		}
	}
	return out
}

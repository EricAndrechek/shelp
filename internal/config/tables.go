package config

import (
	"fmt"
	"slices"
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

// ReplaceBlock returns src with b's lines replaced by text (which should
// end in "\n"; one is added if not). Everything around b is left as is.
func ReplaceBlock(src string, b Block, text string) string {
	lines := splitLines(src)
	if b.Start < 0 || b.End > len(lines) || b.Start >= b.End {
		return src
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return strings.Join(lines[:b.Start], "") + text + strings.Join(lines[b.End:], "")
}

// KeyEdit sets one key of a table to Value, a TOML value as text (e.g.
// Quote(s) or `["a", "b"]`); an empty Value removes the key.
type KeyEdit struct{ Key, Value string }

// EditTableKeys returns block -- one [[name]] entry, as ArrayTableBlocks
// finds it -- with edits made to the keys of its own table (not of its
// sub-tables). A key's line is replaced where it is (a multi-line value's
// lines with it; a comment after it on the line is kept) or removed; a
// key that is not there is added after the table's last key ("name" right
// after the header). Comments, blank lines, other keys and sub-tables are
// left as they are.
func EditTableKeys(block string, edits []KeyEdit) string {
	lines := splitLines(block)
	for _, e := range edits {
		header, spans := tableKeys(lines)
		if header < 0 {
			return block
		}
		i := slices.IndexFunc(spans, func(s keySpan) bool { return s.key == e.Key })
		switch {
		case i >= 0 && e.Value == "":
			lines = slices.Delete(lines, spans[i].start, spans[i].end)
		case i >= 0:
			sp := spans[i]
			first := lines[sp.start]
			indent := first[:len(first)-len(strings.TrimLeft(first, " \t"))]
			line := indent + e.Key + " = " + e.Value
			if sp.end == sp.start+1 {
				line += trailingComment(first)
			}
			lines = slices.Replace(lines, sp.start, sp.end, line+"\n")
		case e.Value != "":
			at, indent := header+1, ""
			if len(spans) > 0 {
				first := lines[spans[0].start]
				indent = first[:len(first)-len(strings.TrimLeft(first, " \t"))]
				if e.Key != "name" {
					at = spans[len(spans)-1].end
				}
			}
			if at > 0 && !strings.HasSuffix(lines[at-1], "\n") {
				lines[at-1] += "\n"
			}
			lines = slices.Insert(lines, at, indent+e.Key+" = "+e.Value+"\n")
		}
	}
	return strings.Join(lines, "")
}

// keySpan is one key of a table: lines [start, end).
type keySpan struct {
	key        string
	start, end int
}

// tableKeys finds the first table header in lines and the keys of that
// table, up to the next header.
func tableKeys(lines []string) (header int, spans []keySpan) {
	hs := headerLines(lines)
	if len(hs) == 0 {
		return -1, nil
	}
	header = hs[0]
	end := len(lines)
	if len(hs) > 1 {
		end = hs[1]
	}
	depth, inML := 0, ""
	for i := header + 1; i < end; i++ {
		l := lines[i]
		switch {
		case inML != "":
			if strings.Count(l, inML)%2 == 1 {
				inML = ""
			}
			spans[len(spans)-1].end = i + 1
			continue
		case depth > 0:
			depth = scanDepth(l, depth)
			spans[len(spans)-1].end = i + 1
			continue
		}
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, val, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') && k[len(k)-1] == k[0] {
			k = k[1 : len(k)-1]
		}
		spans = append(spans, keySpan{key: k, start: i, end: i + 1})
		for _, d := range []string{`"""`, `'''`} {
			if strings.Count(val, d)%2 == 1 {
				inML = d
			}
		}
		if inML == "" {
			depth = scanDepth(val, 0)
		}
	}
	return header, spans
}

// trailingComment is the "# ..." at the end of a one-line assignment, with
// the space before it ("" if none), found outside strings.
func trailingComment(l string) string {
	var quote rune
	esc := false
	for i, r := range l {
		switch {
		case quote != 0:
			switch {
			case esc:
				esc = false
			case r == '\\' && quote == '"':
				esc = true
			case r == quote:
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			j := i
			for j > 0 && (l[j-1] == ' ' || l[j-1] == '\t') {
				j--
			}
			return strings.TrimRight(l[j:], "\r\n")
		}
	}
	return ""
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

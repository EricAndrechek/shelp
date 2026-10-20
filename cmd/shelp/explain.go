package main

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ericandrechek/shelp/internal/markdown"
	"github.com/ericandrechek/shelp/internal/style"
)

// warningWords are lowercase words an explanation uses to flag a risky
// command; the highlighter makes them stand out.
var warningWords = map[string]bool{
	"delete": true, "deletes": true, "deleted": true, "deleting": true,
	"remove": true, "removes": true, "removed": true,
	"irreversible": true, "irreversibly": true,
	"permanent": true, "permanently": true,
	"overwrite": true, "overwrites": true, "overwritten": true,
	"dangerous": true, "risky": true, "careful": true, "caution": true, "warning": true,
	"destroy": true, "destroys": true, "wipe": true, "wipes": true,
	"unrecoverable": true,
}

// explainHighlighter renders an explanation for a terminal as it streams
// in: a "$ command" header (command words link to their man pages), a
// blank line, then the model's Markdown -- bullets, `code`, **bold**,
// *emphasis*, links, headings -- laid out indented and word-wrapped to
// explainMaxWidth (or the terminal, if narrower) with hanging indents on
// list items, then a dim "man:" footer. The text is processed one line at
// a time: a partial line is held back until "\n" or Flush, so a chunk
// boundary never splits a styled token and the output is the same however
// the text is chunked. A model that ignores the Markdown format still
// renders as wrapped text.
type explainHighlighter struct {
	w     io.Writer
	p     style.Palette
	cmd   map[string]bool // words of the explained command
	line  strings.Builder // the partial line
	tail  string          // an incomplete UTF-8 sequence from the last chunk
	begun bool
	input string
	width int // wrap column; 0: don't wrap
	blank int // blank lines held back until more text follows
	wrote bool
	out   strings.Builder // scratch for one rendered line
	// footer, set by SetFooter, is the dim "provider · 1.4s" line under
	// the man-page links.
	footer string
}

// explainMaxWidth caps the wrap column on a wide terminal: long lines are
// hard to read.
const explainMaxWidth = 90

const explainIndent = "  "

// newExplainHighlighter lays text out for a terminal termWidth columns
// wide (0 if unknown: indent, but don't wrap).
func newExplainHighlighter(w io.Writer, p style.Palette, command string, termWidth int) *explainHighlighter {
	h := &explainHighlighter{w: w, p: p, cmd: map[string]bool{}, input: command}
	if termWidth > 0 {
		// one column spare: a line that exactly fills the terminal makes
		// some terminals wrap before the newline arrives
		h.width = min(termWidth-1, explainMaxWidth)
	}
	for _, f := range strings.Fields(command) {
		if len(f) >= 2 {
			h.cmd[f] = true
		}
	}
	return h
}

// Header prints the explained command once, before any explanation. Each
// command word that starts a pipeline segment links to its man page.
func (h *explainHighlighter) Header() {
	if h.begun {
		return
	}
	h.begun = true
	var b strings.Builder
	b.WriteString(h.p.Dim("$ "))
	for _, c := range splitCommand(h.input) {
		s := h.p.Bold(h.p.Accent(c.text))
		if c.cmd && h.p.On() {
			s = hyperlink(manURL(c.text), s)
		}
		b.WriteString(s)
	}
	io.WriteString(h.w, b.String()+"\n\n")
}

// Write feeds a chunk of explanation. Subprocess output arrives in raw
// byte reads (provider.RunCmdOpts), which can split a multi-byte
// character, so an incomplete trailing sequence waits for the next chunk.
func (h *explainHighlighter) Write(s string) {
	h.Header()
	s, h.tail = splitIncomplete(h.tail + s)
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		h.line.WriteString(s[:i])
		h.finishLine()
		s = s[i+1:]
	}
	h.line.WriteString(s)
}

// finishLine renders the buffered line. Blank lines are held back so that
// leading and trailing ones never reach the output.
func (h *explainHighlighter) finishLine() {
	line := h.line.String()
	h.line.Reset()
	if strings.TrimSpace(line) == "" {
		if h.wrote {
			h.blank++
		}
		return
	}
	h.render(line)
}

// SetFooter sets the dim line Flush prints last (who answered, how fast).
func (h *explainHighlighter) SetFooter(s string) { h.footer = s }

// Flush renders the held-back partial line, ends the output with exactly
// one newline, and prints the footer: the man-page links, then the
// SetFooter line, after one blank line.
func (h *explainHighlighter) Flush() {
	h.Header()
	h.line.WriteString(h.tail)
	h.tail = ""
	h.finishLine()
	var lines []string
	if words := commandWords(h.input); len(words) > 0 {
		links := make([]string, len(words))
		for i, w := range words {
			links[i] = w
			if h.p.On() {
				links[i] = hyperlink(manURL(w), w)
			}
		}
		lines = append(lines, explainIndent+h.p.Dim("man: ")+h.dimJoin(links))
	}
	if h.footer != "" {
		lines = append(lines, explainIndent+h.p.Dim(h.footer))
	}
	if len(lines) == 0 {
		return
	}
	sep := "\n"
	if !h.wrote {
		sep = ""
	}
	io.WriteString(h.w, sep+strings.Join(lines, "\n")+"\n")
}

func (h *explainHighlighter) dimJoin(links []string) string {
	return strings.Join(links, h.p.Dim(" · "))
}

// piece is a run of identically styled text inside a word.
type piece struct {
	text string
	sp   markdown.Span
}

// render lays out one non-blank source line.
func (h *explainHighlighter) render(line string) {
	b := markdown.ParseBlock(line)
	if b.Kind == markdown.Fence {
		return
	}
	var first, cont string
	extra := strings.Repeat(" ", min(b.Indent, 8))
	switch b.Kind {
	case markdown.Bullet:
		first = explainIndent + extra + h.p.Accent("•") + " "
		cont = explainIndent + extra + "  "
	case markdown.Ordered:
		first = explainIndent + extra + h.p.Dim(b.Marker) + " "
		cont = explainIndent + extra + strings.Repeat(" ", len(b.Marker)+1)
	default:
		first, cont = explainIndent, explainIndent
	}
	spans := markdown.ParseInline(b.Text)
	warn := b.Kind == markdown.Paragraph && len(spans) > 0 && spans[0].Bold &&
		strings.HasPrefix(strings.ToLower(spans[0].Text), "warning")
	words := splitWords(spans)
	if len(words) == 0 {
		return
	}

	o := &h.out
	o.Reset()
	o.WriteString(strings.Repeat("\n", h.blank))
	h.blank = 0
	h.wrote = true
	o.WriteString(first)
	prefixW := utf8.RuneCountInString(style.Strip(first))
	col := prefixW
	for i, w := range words {
		n := 0
		var s strings.Builder
		for _, pc := range w {
			n += utf8.RuneCountInString(pc.text)
			s.WriteString(h.style(pc, b.Kind == markdown.Heading, warn))
		}
		switch {
		case i == 0:
		case h.width > 0 && col+1+n > h.width:
			o.WriteString("\n" + cont)
			col = utf8.RuneCountInString(cont)
		default:
			o.WriteByte(' ')
			col++
		}
		o.WriteString(s.String())
		col += n
	}
	o.WriteByte('\n')
	io.WriteString(h.w, o.String())
}

// splitWords cuts spans into words (runs without whitespace). A code span
// is never split, so `du -sh` stays together when wrapping.
func splitWords(spans []markdown.Span) [][]piece {
	var words [][]piece
	var cur []piece
	end := func() {
		if len(cur) > 0 {
			words = append(words, cur)
			cur = nil
		}
	}
	for _, sp := range spans {
		if sp.Code {
			cur = append(cur, piece{strings.Join(strings.Fields(sp.Text), " "), sp})
			continue
		}
		start := -1
		for i, r := range sp.Text {
			if unicode.IsSpace(r) {
				if start >= 0 {
					cur = append(cur, piece{sp.Text[start:i], sp})
					start = -1
				}
				end()
			} else if start < 0 {
				start = i
			}
		}
		if start >= 0 {
			cur = append(cur, piece{sp.Text[start:], sp})
		}
	}
	end()
	return words
}

// sgr wraps s in an SGR sequence when the palette colors.
func (h *explainHighlighter) sgr(code, s string) string {
	if !h.p.On() || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// style renders one piece.
func (h *explainHighlighter) style(pc piece, heading, warn bool) string {
	sp := pc.sp
	var s string
	switch {
	case sp.Code:
		s = h.p.Accent(pc.text)
	case heading:
		s = h.p.Bold(h.p.Accent(pc.text))
	case warn:
		s = h.p.Warn(pc.text)
		if sp.Bold {
			s = h.p.Bold(s)
		}
	default:
		s = h.word(pc.text)
		if sp.Bold {
			s = h.p.Bold(s)
		}
	}
	if sp.Em {
		s = h.sgr("3", s)
	}
	if sp.URL != "" && h.p.On() {
		s = hyperlink(sp.URL, h.sgr("4", s))
	}
	return s
}

// splitIncomplete splits s into its complete UTF-8 prefix and an
// incomplete multi-byte sequence at its end (empty if there is none).
func splitIncomplete(s string) (string, string) {
	for i := len(s) - 1; i >= 0 && i >= len(s)-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			if utf8.FullRuneInString(s[i:]) {
				return s, ""
			}
			return s[:i], s[i:]
		}
	}
	return s, ""
}

const (
	leadPunct  = "(\"'`["
	transPunct = ",.;:)\"'`]!?"
)

// word styles one whitespace-free token, leaving surrounding punctuation
// unstyled.
func (h *explainHighlighter) word(tok string) string {
	core := strings.TrimLeft(tok, leadPunct)
	lead := tok[:len(tok)-len(core)]
	trimmed := strings.TrimRight(core, transPunct)
	trail := core[len(trimmed):]
	core = trimmed
	if core == "" {
		return tok
	}
	switch {
	case warningWords[strings.ToLower(core)]:
		return lead + h.p.Bold(h.p.Warn(core)) + trail
	case isFlag(core), len(core) >= 2 && h.cmd[core]:
		return lead + h.p.Accent(core) + trail
	}
	return tok
}

// isFlag reports whether s looks like a command-line flag: -l, -nP,
// --force, -iTCP:8080.
func isFlag(s string) bool {
	rest := strings.TrimPrefix(s, "--")
	if rest == s {
		rest = strings.TrimPrefix(s, "-")
	}
	if rest == s || rest == "" {
		return false
	}
	r := rune(rest[0])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

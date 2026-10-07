package main

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

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

// explainHighlighter colors and lays out an explanation as it streams
// in: a "$ command" header, a blank line, then the text indented and
// word-wrapped to explainMaxWidth (or the terminal, if narrower), so the
// terminal never breaks a word across lines. Text is cut into
// whitespace-separated words; a partial word is held back until
// whitespace or Flush, so a chunk boundary never splits a styled token and
// the output is the same however the text is chunked. Newlines in the
// text are kept; other whitespace runs become single spaces.
type explainHighlighter struct {
	w     io.Writer
	p     style.Palette
	cmd   map[string]bool // words of the explained command
	buf   strings.Builder // the partial word
	tail  string          // an incomplete UTF-8 sequence from the last chunk
	begun bool
	input string
	width int  // wrap column; 0: don't wrap
	col   int  // visible column of the current line
	space bool // a space is owed before the next word on this line
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

// Header prints the explained command once, before any explanation.
func (h *explainHighlighter) Header() {
	if h.begun {
		return
	}
	h.begun = true
	io.WriteString(h.w, h.p.Dim("$ ")+h.p.Bold(h.p.Accent(h.input))+"\n\n")
}

// emit lays out one finished word.
func (h *explainHighlighter) emit(out *strings.Builder, tok string) {
	n := utf8.RuneCountInString(tok)
	switch {
	case h.col == 0:
		out.WriteString(explainIndent)
		h.col = len(explainIndent)
	case h.space && h.width > 0 && h.col+1+n > h.width:
		out.WriteString("\n" + explainIndent)
		h.col = len(explainIndent)
	case h.space:
		out.WriteByte(' ')
		h.col++
	}
	out.WriteString(h.word(tok))
	h.col += n
	h.space = false
}

// Write feeds a chunk of explanation. Subprocess output arrives in raw
// byte reads (provider.RunCmdOpts), which can split a multi-byte
// character, so an incomplete trailing sequence waits for the next chunk.
func (h *explainHighlighter) Write(s string) {
	h.Header()
	s, h.tail = splitIncomplete(h.tail + s)
	var out strings.Builder
	for _, r := range s {
		if !unicode.IsSpace(r) {
			h.buf.WriteRune(r)
			continue
		}
		if h.buf.Len() > 0 {
			h.emit(&out, h.buf.String())
			h.buf.Reset()
		}
		if r == '\n' {
			out.WriteByte('\n')
			h.col, h.space = 0, false
		} else if h.col > 0 {
			h.space = true
		}
	}
	if out.Len() > 0 {
		io.WriteString(h.w, out.String())
	}
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

// Flush writes the held-back partial word.
func (h *explainHighlighter) Flush() {
	h.buf.WriteString(h.tail)
	h.tail = ""
	if h.buf.Len() > 0 {
		var out strings.Builder
		h.emit(&out, h.buf.String())
		io.WriteString(h.w, out.String())
		h.buf.Reset()
	}
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

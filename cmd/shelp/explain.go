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

// explainHighlighter colors an explanation as it streams in. Text is cut
// into whitespace-separated words; a partial word is held back until
// whitespace or Flush, so a chunk boundary never splits a styled token and
// the output is the same however the text is chunked.
type explainHighlighter struct {
	w     io.Writer
	p     style.Palette
	cmd   map[string]bool // words of the explained command
	buf   strings.Builder // the partial word
	tail  string          // an incomplete UTF-8 sequence from the last chunk
	begun bool
	input string
}

func newExplainHighlighter(w io.Writer, p style.Palette, command string) *explainHighlighter {
	h := &explainHighlighter{w: w, p: p, cmd: map[string]bool{}, input: command}
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
	io.WriteString(h.w, h.p.Bold(h.p.Accent(h.input))+"\n")
}

// Write feeds a chunk of explanation. Subprocess output arrives in raw
// byte reads (provider.RunCmdOpts), which can split a multi-byte
// character, so an incomplete trailing sequence waits for the next chunk.
func (h *explainHighlighter) Write(s string) {
	h.Header()
	s, h.tail = splitIncomplete(h.tail + s)
	var out strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			if h.buf.Len() > 0 {
				out.WriteString(h.word(h.buf.String()))
				h.buf.Reset()
			}
			out.WriteRune(r)
			continue
		}
		h.buf.WriteRune(r)
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
		io.WriteString(h.w, h.word(h.buf.String()))
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

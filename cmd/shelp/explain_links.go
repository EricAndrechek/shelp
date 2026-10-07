package main

import (
	"strings"
)

// shellKeywords are words that start a pipeline segment but are not
// commands with a man page worth linking.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "in": true, "function": true, "time": true,
	"select": true, "end": true, "begin": true, "cd": true, "export": true,
	"set": true, "unset": true, "alias": true, "source": true, "exit": true,
}

// isCommandName reports whether w looks like a plain command name.
func isCommandName(w string) bool {
	if w == "" || shellKeywords[w] {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '+' || c == '-'
		if !ok {
			return false
		}
	}
	return w[0] != '-' && w[0] != '.'
}

// cmdPiece is a stretch of the explained command: a plain run, or a
// command word that starts a pipeline segment.
type cmdPiece struct {
	text string
	cmd  bool // a linkable command word
}

// splitCommand cuts a shell command into plain runs and the command word
// at the start of each pipeline segment (the first word, and the first
// after |, &&, ||, ; or a newline). It is a light scanner, not a parser:
// quotes are respected, VAR=value prefixes are skipped, and anything that
// is not a plain command name stays in a plain run.
func splitCommand(s string) []cmdPiece {
	var out []cmdPiece
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, cmdPiece{text: plain.String()})
			plain.Reset()
		}
	}
	atStart := true
	var quote byte
	for i := 0; i < len(s); {
		c := s[i]
		if quote != 0 {
			plain.WriteByte(c)
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(s) {
				i++
				plain.WriteByte(s[i])
			}
			i++
			continue
		}
		switch {
		case c == ' ' || c == '\t':
			plain.WriteByte(c)
			i++
		case c == '|' || c == ';' || c == '&' || c == '\n' || c == '(' || c == '{':
			// 2>&1 and >&2 are redirects, not a new segment
			if !(c == '&' && i > 0 && (s[i-1] == '>' || s[i-1] == '<')) {
				atStart = true
			}
			plain.WriteByte(c)
			i++
		case c == '\'' || c == '"':
			quote = c
			atStart = false
			plain.WriteByte(c)
			i++
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t|;&\n()'\"<>", rune(s[j])) {
				j++
			}
			if j == i { // a redirect or other operator
				plain.WriteByte(c)
				i++
				continue
			}
			w := s[i:j]
			switch {
			case atStart && strings.Contains(w, "="):
				// VAR=value prefix: the command comes next
				plain.WriteString(w)
			case atStart && isCommandName(w) && (j >= len(s) || !strings.ContainsRune("'\"", rune(s[j]))):
				flush()
				out = append(out, cmdPiece{text: w, cmd: true})
				atStart = false
			default:
				plain.WriteString(w)
				atStart = false
			}
			i = j
		}
	}
	flush()
	return out
}

// commandWords lists the distinct linkable command words, in order.
func commandWords(s string) []string {
	var words []string
	seen := map[string]bool{}
	for _, p := range splitCommand(s) {
		if p.cmd && !seen[p.text] {
			seen[p.text] = true
			words = append(words, p.text)
		}
	}
	return words
}

// manURL is the macOS URL that opens a command's man page.
func manURL(word string) string { return "x-man-page://" + word }

// hyperlink wraps s in an OSC 8 terminal hyperlink to url.
func hyperlink(url, s string) string {
	return "\x1b]8;;" + url + "\x1b\\" + s + "\x1b]8;;\x1b\\"
}

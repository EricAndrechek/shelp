package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// NormalizeSuggest implements the suggest-mode answer normalization used for
// grade keys and reference matching: trim, collapse runs of whitespace
// outside single/double quotes to one space, drop a trailing ';'.
func NormalizeSuggest(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	var inSingle, inDouble, lastWasSpace bool
	for _, r := range s {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteRune(r)
			lastWasSpace = false
		case r == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteRune(r)
			lastWasSpace = false
		case !inSingle && !inDouble && isSpace(r):
			if !lastWasSpace {
				b.WriteRune(' ')
				lastWasSpace = true
			}
		default:
			b.WriteRune(r)
			lastWasSpace = false
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.TrimSuffix(out, ";")
	return strings.TrimSpace(out)
}

func isSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

var wsRun = regexp.MustCompile(`\s+`)

// NormalizeExplain implements the explain-mode answer normalization: trim +
// collapse whitespace.
func NormalizeExplain(s string) string {
	return strings.TrimSpace(wsRun.ReplaceAllString(s, " "))
}

// Normalize applies the mode-appropriate normalization.
func Normalize(mode, s string) string {
	if mode == "explain" {
		return NormalizeExplain(s)
	}
	return NormalizeSuggest(s)
}

// GradeKey computes the hex sha256 grade key for an answer: sha256 of
// "case_id | shell | os | normalized_answer". mode selects the
// normalization; it is not otherwise part of the hashed string, since the
// case id already determines the mode.
func GradeKey(caseID, shell, osKey, mode, answer string) string {
	norm := Normalize(mode, answer)
	sum := sha256.Sum256([]byte(caseID + "|" + shell + "|" + osKey + "|" + norm))
	return hex.EncodeToString(sum[:])
}

// MatchesReference reports whether answer normalizes equal to any of refs
// (also normalized).
func MatchesReference(mode, answer string, refs []string) bool {
	na := Normalize(mode, answer)
	for _, r := range refs {
		if Normalize(mode, r) == na {
			return true
		}
	}
	return false
}

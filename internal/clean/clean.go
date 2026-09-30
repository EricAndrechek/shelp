// Package clean post-processes model output.
package clean

import (
	"regexp"
	"strings"
)

var (
	fenceLine  = regexp.MustCompile("^\\s*```")
	wrapTicks  = regexp.MustCompile("^`([^`].*)`$")
	promptSign = regexp.MustCompile(`^(\$|%|❯|>)\s+`)
	labelLine  = regexp.MustCompile(`^(?i:command|cmd)\s*:\s*`)
)

// Command turns a suggest-mode reply into the bare command: drops markdown
// fence lines, leading/trailing blank lines, a single pair of wrapping
// backticks, a leading "Command:" label and a leading prompt sign
// ("$ ", "% "). Multi-line commands are
// kept as-is otherwise.
func Command(s string) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if fenceLine.MatchString(l) {
			continue
		}
		lines = append(lines, strings.TrimRight(l, " \t"))
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 1 {
		l := strings.TrimSpace(lines[0])
		if m := wrapTicks.FindStringSubmatch(l); m != nil {
			l = m[1]
		}
		l = labelLine.ReplaceAllString(l, "")
		if m := wrapTicks.FindStringSubmatch(l); m != nil {
			l = m[1]
		}
		l = promptSign.ReplaceAllString(l, "")
		lines[0] = l
	}
	return strings.Join(lines, "\n")
}

// Text trims an explain-mode reply.
func Text(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

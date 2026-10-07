package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/ericandrechek/shelp/internal/style"
)

const versionTagline = "turn a task into a shell command, or explain one"

// printVersion is `shelp version`. On a terminal it shows the logo, the
// version, and the tagline; anywhere else it is exactly "shelp
// <version>\n", which scripts and the Homebrew formula's test read.
func printVersion(w io.Writer) {
	p := style.For(w)
	if !p.On() {
		fmt.Fprintln(w, "shelp", version)
		return
	}
	fmt.Fprintf(w, "%s\n\nshelp %s\n%s\n", style.Logo(p), p.Bold(p.Accent(version)), p.Dim(versionTagline))
}

// printUsage writes the usage text to w. On a terminal the logo goes on
// top and the headings are colored; the text itself is the same, and
// anywhere else it is exactly usage.
func printUsage(w io.Writer) {
	p := style.For(w)
	if !p.On() {
		fmt.Fprint(w, usage)
		return
	}
	fmt.Fprint(w, style.Logo(p)+"\n\n"+styleUsage(p, usage))
}

// styleUsage colors u's first-line "shelp" and its section headings
// (unindented lines ending in ':'), changing nothing else.
func styleUsage(p style.Palette, u string) string {
	lines := strings.Split(u, "\n")
	for i, l := range lines {
		switch {
		case i == 0 && strings.HasPrefix(l, "shelp:"):
			lines[i] = p.Bold(p.Accent("shelp")) + strings.TrimPrefix(l, "shelp")
		case l != "" && l[0] != ' ' && strings.HasSuffix(l, ":"):
			lines[i] = p.Bold(p.Accent(l))
		}
	}
	return strings.Join(lines, "\n")
}

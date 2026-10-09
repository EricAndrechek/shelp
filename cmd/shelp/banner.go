package main

import (
	"fmt"
	"io"

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

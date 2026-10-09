package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
	"github.com/ericandrechek/shelp/internal/shellinit"
)

// runCompletion handles `shelp completion <shell>`: it prints the tab
// completion script, the same text `shelp init <shell>` embeds.
func runCompletion(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		printHelp(stdout, "completion")
		return nil
	}
	if len(args) != 1 {
		printHelp(stderr, "completion")
		return usageError{fmt.Errorf("completion needs one shell: %s", strings.Join(shellinit.Shells(), ", "))}
	}
	s, err := shellinit.Completion(args[0])
	if err != nil {
		printHelp(stderr, "completion")
		return usageError{err}
	}
	_, err = io.WriteString(stdout, s)
	return err
}

// runComplete is the hidden `shelp __complete <providers|templates>` the
// completion scripts call: one value per line. It is on the Tab path, so
// it reads the config and nothing else (provider.All is config + LookPath;
// no Detect, no network) and never fails noisily: a bad config or an
// unknown kind just prints nothing.
func runComplete(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return nil
	}
	switch args[0] {
	case "providers":
		cfg, err := loadConfig()
		if err != nil {
			return nil
		}
		for _, p := range provider.All(cfg) {
			fmt.Fprintln(stdout, p.ID())
		}
	case "templates":
		for _, n := range prompt.Names() {
			fmt.Fprintln(stdout, n)
		}
	}
	return nil
}

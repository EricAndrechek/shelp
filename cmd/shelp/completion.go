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

// runComplete is the hidden `shelp __complete KIND [ARGS]` the completion
// scripts call, one value per line:
//
//	providers | templates      provider IDs, template names
//	commands                   name<TAB>description of every command
//	flags [CMD [SUB]]          the flags of CMD (top level when absent);
//	                           empty for a word that is no command
//	subcommands CMD            CMD's positional subcommands
//
// It is on the Tab path, so it reads the config and nothing else
// (provider.All is config + LookPath; no Detect, no network) and never
// fails noisily: a bad config or an unknown kind just prints nothing. The
// scripts get commands, flags and subcommands from here, not from a list
// rendered into them, so a shell that loaded its completion before an
// upgrade still offers the new commands.
func runComplete(args []string, stdout io.Writer) error {
	if len(args) == 0 {
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
	case "commands":
		for _, c := range shellinit.CompletionCommands {
			fmt.Fprintf(stdout, "%s\t%s\n", c.Name, c.Desc)
		}
	case "flags":
		for _, f := range shellinit.CompletionFlagsFor(args[1:]) {
			fmt.Fprintln(stdout, f)
		}
	case "subcommands":
		if len(args) == 2 {
			for _, w := range shellinit.CompletionSubcommands(args[1]) {
				fmt.Fprintln(stdout, w)
			}
		}
	}
	return nil
}

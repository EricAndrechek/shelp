package main

import (
	"context"
	"fmt"
	"io"

	"github.com/ericandrechek/shelp/internal/provider"
)

// commandFor names the subcommand run hands args to, or returns "" when
// args are a task for suggest ("shelp find big files"). It is the one place
// that decides which argument shapes a command claims -- help, version and
// config claim only some, so "shelp help me find big files" is a task --
// and both run and the hidden `shelp __kind` use it, so they can never
// disagree.
//
// The shell hooks ask `shelp __kind` rather than keeping a list of command
// names: a hook is rendered once, at shell start, and a long-running shell
// keeps it across upgrades, so a list in the hook goes stale -- after an
// upgrade added `config`, an old zsh hook sent `shelp config` to the model
// as the task "config".
func commandFor(args []string) string {
	if len(args) == 0 {
		return "help"
	}
	rest := args[1:]
	switch args[0] {
	case "-h", "--help":
		return "help"
	case "help":
		// Bare "help" is help, and so is "help <command>" (or "help -h").
		// Anything else ("shelp help me find big files") is a task.
		if len(rest) == 0 {
			return "help"
		}
		if len(rest) == 1 && (isHelpFlag(rest[0]) || isHelpTopic(rest[0])) {
			return "help"
		}
	case "version", "--version":
		// Likewise "shelp version of node installed" is a task.
		if len(rest) == 0 || len(rest) == 1 && isHelpFlag(rest[0]) {
			return "version"
		}
	case "config":
		// Likewise "shelp config of nginx" is a task, and so are "config add
		// a cron job" and "config remove old kernels": add claims only
		// `add endpoint|local ...`, remove only one NAME.
		if len(rest) == 0 || rest[0] == "chain" || rest[0] == "edit" || isHelpFlag(rest[0]) {
			return "config"
		}
		switch sub := rest[1:]; rest[0] {
		case "add":
			if len(sub) == 0 || isHelpFlag(sub[0]) || sub[0] == "endpoint" || sub[0] == "local" {
				return "config"
			}
		case "remove":
			if len(sub) <= 1 {
				return "config"
			}
		}
	case "history":
		// Only history's own shapes; "shelp history of git commits" is a task.
		if isHistoryArgs(rest) {
			return "history"
		}
	case "explain", "-e":
		return "explain"
	case "suggest", "init", "completion", "providers", "setup", "__complete", "__kind":
		return args[0]
	}
	return ""
}

func isHelpFlag(s string) bool { return s == "-h" || s == "--help" }

// isHelpTopic reports whether `shelp help <s>` shows a command's help.
func isHelpTopic(s string) bool {
	_, ok := helpTopic(s)
	return ok && s != "" // "" is the top-level usage, not a topic
}

// commands runs each subcommand commandFor names. args are run's own, the
// command's name included (help and version need to know which spelling
// was used).
var commands = map[string]func(ctx context.Context, args []string, stdout, stderr io.Writer) error{
	"help": func(_ context.Context, args []string, stdout, _ io.Writer) error {
		topic := ""
		if len(args) == 2 && args[0] == "help" {
			topic = args[1]
			if isHelpFlag(topic) {
				topic = "help"
			}
		}
		printHelp(stdout, topic)
		return nil
	},
	"version": func(_ context.Context, args []string, stdout, _ io.Writer) error {
		if len(args) > 1 { // version -h
			printHelp(stdout, "version")
			return nil
		}
		printVersion(stdout)
		return nil
	},
	"suggest": func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return runGenerate(ctx, provider.Suggest, args[1:], stdout, stderr)
	},
	"explain": func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return runGenerate(ctx, provider.Explain, args[1:], stdout, stderr)
	},
	"init": func(_ context.Context, args []string, stdout, stderr io.Writer) error {
		return runInit(args[1:], stdout, stderr)
	},
	"completion": func(_ context.Context, args []string, stdout, stderr io.Writer) error {
		return runCompletion(args[1:], stdout, stderr)
	},
	"__complete": func(_ context.Context, args []string, stdout, _ io.Writer) error {
		return runComplete(args[1:], stdout)
	},
	"__kind": func(_ context.Context, args []string, stdout, _ io.Writer) error {
		return runKind(args[1:], stdout)
	},
	"providers": func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return runProviders(ctx, args[1:], stdout, stderr)
	},
	"setup": func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return runSetup(ctx, args[1:], stdout, stderr)
	},
	"history": func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return runHistory(ctx, args[1:], stdout, stderr)
	},
	"config": func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return runConfig(ctx, args[1:], stdout, stderr)
	},
}

// runTask handles every argument list commandFor doesn't claim: the whole
// of it is a task for suggest, flags included ("shelp -p fm find big
// files").
var runTask = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runGenerate(ctx, provider.Suggest, args, stdout, stderr)
}

// runKind is the hidden `shelp __kind ARGS...` the shell hooks ask before
// treating `shelp ARGS...` as a task: it prints "command" when run would
// hand ARGS to a subcommand and "task" when it would suggest a command for
// them, and exits 0 either way. It runs on Enter, so it reads nothing (no
// config, no providers) and never prints anything else.
func runKind(args []string, stdout io.Writer) error {
	kind := "task"
	if commandFor(args) != "" {
		kind = "command"
	}
	_, err := fmt.Fprintln(stdout, kind)
	return err
}

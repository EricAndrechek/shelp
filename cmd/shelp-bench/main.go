// Command shelp-bench runs shelp's benchmark: cases x shells x os x
// providers x templates x samples, with cheap automatic checks, a
// human/Claude-graded answer store, and a markdown report.
//
// Grading itself is done by a human-supervised Claude reviewer, not by this
// tool: shelp-bench runs trials, applies automatic checks, stores grades,
// and reports. See bench/README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

const usage = `shelp-bench: run and grade shelp's benchmark.

Usage:
  shelp-bench run [flags]                        run trials, write results.jsonl + run.json
  shelp-bench pending [flags]                     list unique ungraded answers
  shelp-bench grade [flags] [file.jsonl]          record grades (or stdin)
  shelp-bench report [flags]                      write a markdown report
  shelp-bench latency [flags]                     end-to-end CLI latency, not just Generate

Run 'shelp-bench <command> -h' for flags on one command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "shelp-bench:", err)
		var ue usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type usageError struct{ error }

func run(ctx context.Context, args []string, stdout, stderr *os.File) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return usageError{errors.New("no command given")}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	case "run":
		return cmdRun(ctx, args[1:], stdout, stderr)
	case "pending":
		return cmdPending(ctx, args[1:], stdout, stderr)
	case "grade":
		return cmdGrade(ctx, args[1:], stdout, stderr)
	case "report":
		return cmdReport(ctx, args[1:], stdout, stderr)
	case "latency":
		return cmdLatency(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return usageError{fmt.Errorf("unknown command %q", args[0])}
	}
}

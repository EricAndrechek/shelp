package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/ericandrechek/shelp/internal/bench"
	"github.com/ericandrechek/shelp/internal/provider"
)

const latencyUsage = `shelp-bench latency: end-to-end shelp CLI latency (what a user feels).

Usage:
  shelp-bench latency --providers a,b [flags]

Runs 'shelp -p <id> --json <prompt>' N times per provider, sequentially,
as a real subprocess (process spawn -> output), and reports cold (first
call) vs warm p50/p90. Appends one line per provider to bench/latency.jsonl.

Flags:
  --providers ids   comma-separated provider ids (required)
  --n N             calls per provider (default: 10)
  --binary path     use this shelp binary instead of building one
  --prompt text     suggest-mode prompt to send (default: %q)
`

func cmdLatency(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("latency", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintf(stderr, latencyUsage, bench.DefaultLatencyPrompt) }
	var providersFlag, binary, promptText, latencyPath string
	var n int
	fs.StringVar(&providersFlag, "providers", "", "")
	fs.IntVar(&n, "n", 10, "")
	fs.StringVar(&binary, "binary", "", "")
	fs.StringVar(&promptText, "prompt", bench.DefaultLatencyPrompt, "")
	fs.StringVar(&latencyPath, "latency-file", bench.DefaultLatencyPath, "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	ids := bench.SplitList(providersFlag)
	if len(ids) == 0 {
		fs.Usage()
		return usageError{fmt.Errorf("--providers is required")}
	}
	if n < 1 {
		n = 1
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := provider.Get(cfg, id); err != nil {
			return err
		}
	}

	if binary == "" {
		fmt.Fprintln(stderr, "shelp-bench: building ./cmd/shelp ...")
		b, cleanup, err := bench.BuildShelp()
		if err != nil {
			return err
		}
		defer cleanup()
		binary = b
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tN\tCOLD\tWARM P50\tWARM P90\tERRORS")
	for _, id := range ids {
		fmt.Fprintf(stderr, "shelp-bench: timing %s (%d calls)...\n", id, n)
		res := bench.MeasureLatency(binary, id, promptText, n)
		if err := bench.AppendLatency(latencyPath, res); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%d\t%dms\t%dms\t%dms\t%d\n", res.Provider, res.N, res.ColdMS, res.WarmP50MS, res.WarmP90MS, res.Errors)
	}
	return tw.Flush()
}

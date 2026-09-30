package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ericandrechek/shelp/internal/bench"
)

const reportUsage = `shelp-bench report: write a markdown report.

Usage:
  shelp-bench report [flags]

Flags:
  --run dir        a run directory (repeatable, comma-separated); default:
                   every dir under bench/runs/ with a results.jsonl
  --cases-dir dir  where *.toml case files live (default: bench/cases)
  --grades file    grades store (default: bench/grades.jsonl)
  --out file       report output (default: bench/RESULTS.md)
  --readme file    also replace the text between <!-- bench:start --> and
                   <!-- bench:end --> in this file with the summary table
`

func cmdReport(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, reportUsage) }
	var runs runFlag
	var casesDir, gradesPath, out, readme string
	fs.Var(&runs, "run", "")
	fs.StringVar(&casesDir, "cases-dir", defaultCasesDir, "")
	fs.StringVar(&gradesPath, "grades", bench.DefaultGradesPath, "")
	fs.StringVar(&out, "out", "bench/RESULTS.md", "")
	fs.StringVar(&readme, "readme", "", "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}

	dirs := runs.dirs
	if len(dirs) == 0 {
		var err error
		dirs, err = defaultRunDirs()
		if err != nil {
			return err
		}
	}
	if len(dirs) == 0 {
		return fmt.Errorf("no run directories found under %s (pass --run)", defaultRunsDir)
	}
	results, _, err := bench.LoadRuns(dirs)
	if err != nil {
		return err
	}
	cases, err := loadCases(casesDir, "")
	if err != nil {
		return err
	}
	grades, err := bench.LoadGrades(gradesPath)
	if err != nil {
		return err
	}
	rep := bench.Compute(results, cases, grades)
	now := time.Now().UTC()
	if err := os.WriteFile(out, []byte(rep.Render(now)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "shelp-bench: wrote %s (%d trials, %d run dir(s))\n", out, len(results), len(dirs))
	if readme != "" {
		if err := bench.UpdateReadme(readme, rep, now); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "shelp-bench: updated %s\n", readme)
	}
	return nil
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ericandrechek/shelp/internal/bench"
)

const pendingUsage = `shelp-bench pending: unique ungraded answers, ready to review.

Usage:
  shelp-bench pending [flags]

Flags:
  --run dir                a run directory (repeatable, comma-separated);
                           default: every dir under bench/runs/ with a
                           results.jsonl
  --cases-dir dir          where *.toml case files live (default: bench/cases)
  --grades file            grades store (default: bench/grades.jsonl)
  --limit N                cap the number of records returned (default: no cap)
  --format jsonl|md|compact  jsonl (default; feed straight into 'grade' once
                           scored), md (readable review sheet), or compact
                           (dense sheet: one header per case, one line per
                           answer with a [n] index -- see 'grade --index')
  --index path             with --format compact, also write the [n] ->
                           grade_key sidecar mapping 'grade --index' needs
                           to grade a compact sheet (default:
                           pending.index.tsv in the current dir)
`

func cmdPending(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("pending", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, pendingUsage) }
	var runs runFlag
	var casesDir, gradesPath, format, indexPath string
	var limit int
	fs.Var(&runs, "run", "")
	fs.StringVar(&casesDir, "cases-dir", defaultCasesDir, "")
	fs.StringVar(&gradesPath, "grades", bench.DefaultGradesPath, "")
	fs.IntVar(&limit, "limit", 0, "")
	fs.StringVar(&format, "format", "jsonl", "")
	fs.StringVar(&indexPath, "index", "pending.index.tsv", "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if format != "jsonl" && format != "md" && format != "compact" {
		return usageError{fmt.Errorf("--format must be jsonl, md, or compact, got %q", format)}
	}

	dirs := runs.dirs
	if len(dirs) == 0 {
		var err error
		dirs, err = defaultRunDirs()
		if err != nil {
			return err
		}
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
	recs := bench.BuildPending(results, cases, grades, limit)
	switch format {
	case "md":
		return bench.WritePendingMarkdown(stdout, recs)
	case "compact":
		if err := bench.WritePendingCompact(stdout, recs); err != nil {
			return err
		}
		idxFile, err := os.Create(indexPath)
		if err != nil {
			return err
		}
		defer idxFile.Close()
		if err := bench.WriteIndex(idxFile, recs); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "shelp-bench: wrote %s (%d entries)\n", indexPath, len(recs))
		return nil
	default:
		return bench.WritePendingJSONL(stdout, recs)
	}
}

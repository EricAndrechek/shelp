package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ericandrechek/shelp/internal/bench"
)

const gradeUsage = `shelp-bench grade: record grades for pending answers.

Usage:
  shelp-bench grade [flags] [file.jsonl]

Reads jsonl lines (from file.jsonl, or stdin when omitted) shaped like
'pending --format jsonl' output, with "score" (0, 1, or 2) filled in and
"note" optionally set. Appends one record per line to the grades store; a
later line for the same grade_key overrides an earlier one (regrading).

Flags:
  --grades file    grades store to append to (default: bench/grades.jsonl)
  --grader name    who/what is grading (default: claude)
  --force          accept lines with unrecognized fields instead of
                   rejecting them
`

func cmdGrade(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, gradeUsage) }
	var gradesPath, grader string
	var force bool
	fs.StringVar(&gradesPath, "grades", bench.DefaultGradesPath, "")
	fs.StringVar(&grader, "grader", "claude", "")
	fs.BoolVar(&force, "force", false, "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() > 1 {
		return usageError{fmt.Errorf("usage: shelp-bench grade [flags] [file.jsonl]")}
	}

	var r *os.File = os.Stdin
	if fs.NArg() == 1 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	inputs, err := bench.ParseGradeInputs(r, force)
	if err != nil {
		return err
	}
	if err := bench.AppendGrades(gradesPath, inputs, grader, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "shelp-bench: recorded %d grade(s) to %s\n", len(inputs), gradesPath)
	return nil
}

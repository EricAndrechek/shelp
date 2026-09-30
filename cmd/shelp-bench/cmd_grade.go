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
  shelp-bench grade [flags] [file]

Reads jsonl lines (from file, or stdin when omitted) shaped like
'pending --format jsonl' output, with "score" (0, 1, or 2) filled in and
"note" optionally set. Appends one record per line to the grades store; a
later line for the same grade_key overrides an earlier one (regrading).

With --index, file (or stdin) is instead read as a plain text sheet, one
line per graded answer: "n score note..." (score 0, 1, or 2; note is
optional free text to the end of the line; blank lines and lines starting
with "#" are ignored) -- the format a reviewer fills in against a
'pending --format compact' sheet. --index names that sheet's sidecar
index file (its own --index, or the default pending.index.tsv), which
maps each n back to a grade_key.

Flags:
  --grades file    grades store to append to (default: bench/grades.jsonl)
  --grader name    who/what is grading (default: claude)
  --force          accept lines with unrecognized fields instead of
                   rejecting them (jsonl mode only)
  --index path     read file/stdin as a plain-text "n score note..." sheet
                   instead of jsonl, mapping n through this sidecar index
                   file (as written by 'pending --format compact --index')
`

func cmdGrade(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, gradeUsage) }
	var gradesPath, grader, indexPath string
	var force bool
	fs.StringVar(&gradesPath, "grades", bench.DefaultGradesPath, "")
	fs.StringVar(&grader, "grader", "claude", "")
	fs.BoolVar(&force, "force", false, "")
	fs.StringVar(&indexPath, "index", "", "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() > 1 {
		return usageError{fmt.Errorf("usage: shelp-bench grade [flags] [file]")}
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

	var inputs []bench.GradeInput
	if indexPath != "" {
		idxFile, err := os.Open(indexPath)
		if err != nil {
			return err
		}
		defer idxFile.Close()
		idx, err := bench.ReadIndex(idxFile)
		if err != nil {
			return err
		}
		lines, err := bench.ParseGradeIndexLines(r)
		if err != nil {
			return err
		}
		inputs, err = bench.GradeInputsFromIndex(lines, idx)
		if err != nil {
			return err
		}
	} else {
		var err error
		inputs, err = bench.ParseGradeInputs(r, force)
		if err != nil {
			return err
		}
	}
	if err := bench.AppendGrades(gradesPath, inputs, grader, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "shelp-bench: recorded %d grade(s) to %s\n", len(inputs), gradesPath)
	return nil
}

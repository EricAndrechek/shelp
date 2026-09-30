package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ericandrechek/shelp/internal/bench"
)

const runUsage = `shelp-bench run: execute benchmark trials.

Usage:
  shelp-bench run [flags]

Flags:
  --providers ids|available   comma-separated provider ids, or "available"
                               (probe Detect in parallel; default)
  --templates names           comma-separated prompt template names
                               (default: the CLI's default template only)
  --cases selector             comma-separated: case ids, tag:x, mode:suggest,
                               or a glob against the case id (default: all)
  --cases-dir dir              where *.toml case files live (default: bench/cases)
  --samples N                  repeats per trial (default: 1)
  --context fields             os,shell,cwd,tools (default: os,shell)
  --concurrency N              per-provider concurrency limit (default: 1)
  --out dir                    output dir (default: bench/runs/<UTC timestamp>)
  --resume dir                 resume an interrupted run: skip completed trials
`

func cmdRun(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, runUsage) }
	var providersFlag, templatesFlag, casesFlag, casesDir, contextFlag, outDir, resumeDir string
	var samples, concurrency int
	fs.StringVar(&providersFlag, "providers", "available", "")
	fs.StringVar(&templatesFlag, "templates", "", "")
	fs.StringVar(&casesFlag, "cases", "", "")
	fs.StringVar(&casesDir, "cases-dir", defaultCasesDir, "")
	fs.StringVar(&contextFlag, "context", "os,shell", "")
	fs.IntVar(&samples, "samples", 1, "")
	fs.IntVar(&concurrency, "concurrency", 1, "")
	fs.StringVar(&outDir, "out", "", "")
	fs.StringVar(&resumeDir, "resume", "", "")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cases, err := loadCases(casesDir, casesFlag)
	if err != nil {
		return err
	}

	ps, skipped, err := bench.ResolveProviders(ctx, cfg, providersFlag)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintf(stderr, "shelp-bench: skipping %s: %s\n", s.ID, s.Reason)
	}
	if len(ps) == 0 {
		return fmt.Errorf("no available providers (see skip reasons above, or pass --providers explicitly)")
	}
	templates, err := bench.ResolveTemplates(templatesFlag)
	if err != nil {
		return err
	}
	contextFields := bench.SplitList(contextFlag)

	trials, err := bench.Expand(cases, ps, templates, samples)
	if err != nil {
		return err
	}

	resume := resumeDir != ""
	dir := outDir
	if resume {
		dir = resumeDir
	} else if dir == "" {
		dir = defaultRunsDir + "/" + time.Now().UTC().Format("20060102T150405Z")
	}

	var provManifests []bench.ProviderManifest
	for _, p := range ps {
		provManifests = append(provManifests, bench.ProviderManifestOf(ctx, p))
	}
	var templateNames []string
	for _, t := range templates {
		templateNames = append(templateNames, t.Name)
	}
	var caseIDs []string
	for _, c := range cases {
		caseIDs = append(caseIDs, c.ID)
	}
	manifest := bench.Manifest{
		Args: os.Args, HostOS: hostOSKey(), GitCommit: gitCommit(),
		Providers: provManifests, Templates: templateNames, Cases: caseIDs,
		Samples: samples, Context: contextFields, Concurrency: concurrency,
		StartedAt: time.Now().UTC(), Trials: len(trials), Resumed: resume,
	}
	if err := bench.SaveManifest(dir, manifest); err != nil {
		return err
	}

	fmt.Fprintf(stderr, "shelp-bench: %d trials -> %s\n", len(trials), dir)
	err = bench.Run(ctx, bench.RunConfig{
		Trials: trials, ContextFields: contextFields, OutDir: dir,
		Concurrency: concurrency, HostOSKey: hostOSKey(), Resume: resume,
		OnProgress: func(p bench.Progress) {
			bench.WriteProgress(stderr, p)
		},
	})
	fmt.Fprintln(stderr)
	manifest.EndedAt = time.Now().UTC()
	if serr := bench.SaveManifest(dir, manifest); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n", dir)
	return nil
}

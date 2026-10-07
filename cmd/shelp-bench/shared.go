package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ericandrechek/shelp/internal/bench"
	"github.com/ericandrechek/shelp/internal/config"
)

// defaultCasesDir is where bench cases live, relative to the repo root.
// shelp-bench (like `go build ./cmd/shelp`) is meant to be run from there.
const defaultCasesDir = "bench/cases"

// defaultRunsDir holds one subdirectory per `run` invocation.
const defaultRunsDir = "bench/runs"

func loadConfig() (*config.Config, error) { return config.LoadDefault() }

func loadCases(dir string, selector string) ([]bench.Case, error) {
	if dir == "" {
		dir = defaultCasesDir
	}
	cases, err := bench.LoadCases(dir)
	if err != nil {
		return nil, err
	}
	return bench.SelectCases(cases, selector)
}

// hostOSKey maps runtime.GOOS to the case os vocabulary ("macos"/"linux"),
// or "" when this host isn't one of them (commands_exist is then never
// checked, since there's nothing to check it against).
func hostOSKey() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "linux":
		return "linux"
	default:
		return ""
	}
}

// gitCommit best-effort identifies the shelp commit a run was produced
// with; empty when unavailable (no git, not a repo, etc).
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// defaultRunDirs globs every subdirectory of defaultRunsDir that has a
// results.jsonl, sorted (oldest first, since run dirs are UTC timestamps).
func defaultRunDirs() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(defaultRunsDir, "*", "results.jsonl"))
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, m := range matches {
		dirs = append(dirs, filepath.Dir(m))
	}
	sort.Strings(dirs)
	return dirs, nil
}

// runFlag accumulates repeated -run flags, splitting each on commas too.
type runFlag struct{ dirs []string }

func (f *runFlag) String() string { return strings.Join(f.dirs, ",") }
func (f *runFlag) Set(s string) error {
	f.dirs = append(f.dirs, bench.SplitList(s)...)
	return nil
}

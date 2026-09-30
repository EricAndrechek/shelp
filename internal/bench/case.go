// Package bench runs shelp's benchmark: providers x prompt templates x
// cases x samples, with automatic checks, a human/Claude-graded answer
// store, and a markdown report.
//
// This file fixes the on-disk case format; see bench/README.md.
package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// Case is one benchmark prompt. Files in bench/cases/*.toml hold a list of
// [[case]] tables.
type Case struct {
	ID   string `toml:"id"`   // unique, kebab-case
	Mode string `toml:"mode"` // "suggest" | "explain"
	// Input is the natural-language task (suggest) or the command to
	// explain (explain).
	Input string `toml:"input"`
	// Shells and OS are the target environments to run the case under;
	// every combination is a separate trial. Defaults: ["zsh"], ["macos"].
	Shells []string `toml:"shells"`
	OS     []string `toml:"os"` // "macos" | "linux"
	// Difficulty is "easy" | "medium" | "hard".
	Difficulty string   `toml:"difficulty"`
	Tags       []string `toml:"tags"`
	// Rubric is what a grader checks: what a correct answer must do, what
	// is acceptable, and the known wrong answers.
	Rubric string `toml:"rubric"`
	// Reference answers known to be fully correct. A normalized exact match
	// is auto-graded 2 without review.
	Reference []string `toml:"reference"`
	// Setup describes the fixture the command assumes (files, a git repo),
	// for graders and for any future execution-based check.
	Setup string `toml:"setup"`
}

type caseFile struct {
	Case []Case `toml:"case"`
}

// LoadCases reads every *.toml under dir, applies defaults and validates.
func LoadCases(dir string) ([]Case, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []Case
	seen := map[string]string{}
	for _, p := range paths {
		var f caseFile
		if _, err := toml.DecodeFile(p, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for i := range f.Case {
			c := &f.Case[i]
			if c.ID == "" || c.Input == "" {
				return nil, fmt.Errorf("%s: case %d needs id and input", p, i)
			}
			if prev, dup := seen[c.ID]; dup {
				return nil, fmt.Errorf("%s: duplicate case id %q (also in %s)", p, c.ID, prev)
			}
			seen[c.ID] = p
			switch c.Mode {
			case "suggest", "explain":
			case "":
				c.Mode = "suggest"
			default:
				return nil, fmt.Errorf("%s: case %q: bad mode %q", p, c.ID, c.Mode)
			}
			if len(c.Shells) == 0 {
				c.Shells = []string{"zsh"}
			}
			if len(c.OS) == 0 {
				c.OS = []string{"macos"}
			}
			out = append(out, *c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no cases found in %s", dir)
	}
	return out, nil
}

// Exists reports whether path exists.
func Exists(path string) bool { _, err := os.Stat(path); return err == nil }

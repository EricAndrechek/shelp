package bench

import (
	"path/filepath"
	"strings"

	"github.com/ericandrechek/shelp/internal/prompt"
)

// ResolveTemplates turns the --templates flag value into concrete
// templates. Empty means just prompt.Default (the CLI's default), not every
// registered template, so a plain `run` without --templates compares
// providers, not templates.
func ResolveTemplates(selector string) ([]prompt.Template, error) {
	names := SplitList(selector)
	if len(names) == 0 {
		names = []string{prompt.Default}
	}
	var out []prompt.Template
	for _, n := range names {
		t, err := prompt.Get(n)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// SelectCases filters cases by a comma-separated list of selectors. Each
// selector is one of:
//   - "tag:x"    cases with tag x
//   - "mode:x"   cases with mode x ("suggest" | "explain")
//   - a glob     matched against the case id (filepath.Match syntax)
//   - anything else: an exact case id
//
// A case matches the whole selector list when it matches ANY one selector
// (OR). An empty/blank selector string selects every case. Cases are
// returned in their original (file, then in-file) order.
func SelectCases(cases []Case, selector string) ([]Case, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return cases, nil
	}
	var toks []string
	for _, t := range strings.Split(selector, ",") {
		if t = strings.TrimSpace(t); t != "" {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 {
		return cases, nil
	}
	var out []Case
	for _, c := range cases {
		if caseMatchesAny(c, toks) {
			out = append(out, c)
		}
	}
	return out, nil
}

func caseMatchesAny(c Case, toks []string) bool {
	for _, t := range toks {
		if caseMatches(c, t) {
			return true
		}
	}
	return false
}

func caseMatches(c Case, tok string) bool {
	switch {
	case strings.HasPrefix(tok, "tag:"):
		want := strings.TrimPrefix(tok, "tag:")
		for _, tag := range c.Tags {
			if tag == want {
				return true
			}
		}
		return false
	case strings.HasPrefix(tok, "mode:"):
		return c.Mode == strings.TrimPrefix(tok, "mode:")
	case strings.ContainsAny(tok, "*?["):
		ok, err := filepath.Match(tok, c.ID)
		return err == nil && ok
	default:
		return c.ID == tok
	}
}

// SplitList splits a comma-separated flag value into trimmed, non-empty
// parts.
func SplitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

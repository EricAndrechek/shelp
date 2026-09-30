package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// PendingRecord is one unique, ungraded answer, aggregated across every
// trial (any run, provider, template, or sample) that produced it. Its
// fields are exactly GradeInput's minus Score/Note, so a reviewer can add
// those two fields and feed the same line straight into `shelp-bench
// grade`.
type PendingRecord struct {
	GradeKey  string          `json:"grade_key"`
	Case      string          `json:"case"`
	Mode      string          `json:"mode"`
	Shell     string          `json:"shell"`
	OS        string          `json:"os"`
	Input     string          `json:"input"`
	Rubric    string          `json:"rubric,omitempty"`
	Reference []string        `json:"reference,omitempty"`
	Answer    string          `json:"answer"`
	Checks    json.RawMessage `json:"checks,omitempty"`
	Count     int             `json:"count"`
	Providers []string        `json:"providers"`
}

// BuildPending groups every trial across results into unique
// (grade_key)-deduplicated answers, excluding anything already resolvable
// (reference match or present in grades). Records are sorted by case id,
// then by descending count, then by grade_key, for a stable,
// review-priority order; limit <= 0 means no limit.
func BuildPending(results []TrialResult, cases []Case, grades map[string]GradeRecord, limit int) []PendingRecord {
	idx := CaseIndex(cases)
	type agg struct {
		rec       PendingRecord
		providers map[string]bool
	}
	byKey := map[string]*agg{}
	var order []string
	for _, r := range results {
		if r.Unavailable || r.Error != "" {
			continue
		}
		c, ok := idx[r.Case]
		if !ok {
			continue
		}
		g := Resolve(r, c, grades)
		if g.Graded {
			continue
		}
		a, seen := byKey[r.GradeKey]
		if !seen {
			checksJSON, _ := json.Marshal(r.Checks)
			a = &agg{
				rec: PendingRecord{
					GradeKey: r.GradeKey, Case: r.Case, Mode: r.Mode, Shell: r.Shell, OS: r.OS,
					Input: c.Input, Rubric: c.Rubric, Reference: c.Reference,
					Answer: r.Output, Checks: checksJSON,
				},
				providers: map[string]bool{},
			}
			byKey[r.GradeKey] = a
			order = append(order, r.GradeKey)
		}
		a.rec.Count++
		a.providers[r.Provider] = true
	}
	out := make([]PendingRecord, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		var ps []string
		for p := range a.providers {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		a.rec.Providers = ps
		out = append(out, a.rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Case != out[j].Case {
			return out[i].Case < out[j].Case
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].GradeKey < out[j].GradeKey
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// WritePendingJSONL writes one JSON object per line, ready to be edited
// (add "score", optionally "note") and fed to `shelp-bench grade`.
func WritePendingJSONL(w io.Writer, recs []PendingRecord) error {
	enc := json.NewEncoder(w)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// WritePendingMarkdown writes a readable review sheet, grouped by case.
func WritePendingMarkdown(w io.Writer, recs []PendingRecord) error {
	fmt.Fprintln(w, "# Pending grades")
	fmt.Fprintln(w)
	if len(recs) == 0 {
		fmt.Fprintln(w, "Nothing pending.")
		return nil
	}
	var curCase string
	for _, r := range recs {
		if r.Case != curCase {
			curCase = r.Case
			fmt.Fprintf(w, "## %s\n\n", curCase)
			fmt.Fprintf(w, "**input:** %s\n\n", r.Input)
			if r.Rubric != "" {
				fmt.Fprintf(w, "**rubric:** %s\n\n", r.Rubric)
			}
			if len(r.Reference) > 0 {
				fmt.Fprintf(w, "**reference:** %s\n\n", strings.Join(r.Reference, " | "))
			}
		}
		fmt.Fprintf(w, "- `%s` (%s/%s, x%d, %s)\n", r.Answer, r.Shell, r.OS, r.Count, strings.Join(r.Providers, ", "))
		fmt.Fprintf(w, "  - grade_key: `%s`\n", r.GradeKey)
		if len(r.Checks) > 0 && string(r.Checks) != "{}" {
			fmt.Fprintf(w, "  - checks: `%s`\n", string(r.Checks))
		}
	}
	return nil
}

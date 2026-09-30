package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
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

// compactFlags renders the subset of a trial's checks worth a reviewer's
// attention as short "key=value" fragments, joined by ", ": parses=false,
// missing_commands=<comma-joined>, gnuisms=<comma-joined>, and
// dangerous=<comma-joined> -- each included only when it actually fired
// (a clean, parseable, non-dangerous, non-GNU-ism answer renders no flags
// at all). checks is the same json.RawMessage-encoded Checks struct stored
// on PendingRecord; a missing/unparseable blob yields no flags rather than
// an error, since flags are advisory, not load-bearing.
func compactFlags(checks json.RawMessage) string {
	if len(checks) == 0 {
		return ""
	}
	var c Checks
	if err := json.Unmarshal(checks, &c); err != nil {
		return ""
	}
	var parts []string
	if c.Parses != nil && !*c.Parses {
		parts = append(parts, "parses=false")
	}
	if len(c.MissingCommands) > 0 {
		parts = append(parts, "missing_commands="+strings.Join(c.MissingCommands, ","))
	}
	if len(c.Gnuisms) > 0 {
		parts = append(parts, "gnuisms="+strings.Join(c.Gnuisms, ","))
	}
	if len(c.Dangerous) > 0 {
		parts = append(parts, "dangerous="+strings.Join(c.Dangerous, ","))
	}
	return strings.Join(parts, ", ")
}

// WritePendingCompact writes a dense review sheet designed to fit far more
// answers on screen than WritePendingMarkdown: one header per case
// (`### id [mode] :: input`, then `RUBRIC:`/`REF:` lines instead of a
// repeated rubric per answer), then one line per answer:
// `[n] (shell/os xCOUNT providers) answer {flags}` -- n a 1-based index
// running across the WHOLE sheet (not reset per case), so it lines up
// 1:1, in order, with WriteIndex's sidecar n -> grade_key mapping for the
// same recs slice. Newlines inside an answer are rendered as " ⏎ " to keep
// one answer to one line; {flags} (see compactFlags) is omitted entirely
// when empty.
func WritePendingCompact(w io.Writer, recs []PendingRecord) error {
	fmt.Fprintln(w, "# Pending grades (compact)")
	fmt.Fprintln(w)
	if len(recs) == 0 {
		fmt.Fprintln(w, "Nothing pending.")
		return nil
	}
	var curCase string
	for i, r := range recs {
		if r.Case != curCase {
			if curCase != "" {
				fmt.Fprintln(w)
			}
			curCase = r.Case
			fmt.Fprintf(w, "### %s [%s] :: %s\n", r.Case, r.Mode, r.Input)
			if r.Rubric != "" {
				fmt.Fprintf(w, "RUBRIC: %s\n", r.Rubric)
			}
			if len(r.Reference) > 0 {
				fmt.Fprintf(w, "REF: %s\n", strings.Join(r.Reference, " | "))
			}
			fmt.Fprintln(w)
		}
		answer := strings.ReplaceAll(r.Answer, "\n", " ⏎ ")
		line := fmt.Sprintf("[%d] (%s/%s x%d %s) %s", i+1, r.Shell, r.OS, r.Count, strings.Join(r.Providers, ", "), answer)
		if flags := compactFlags(r.Checks); flags != "" {
			line += " {" + flags + "}"
		}
		fmt.Fprintln(w, line)
	}
	return nil
}

// WriteIndex writes recs' 1-based-index -> grade_key mapping, one
// tab-separated "n\tgrade_key" line per record, in the same order
// WritePendingCompact numbered them -- the sidecar file `grade --index`
// reads to turn a graded compact sheet's "n score note" lines back into
// grade_keys.
func WriteIndex(w io.Writer, recs []PendingRecord) error {
	for i, r := range recs {
		if _, err := fmt.Fprintf(w, "%d\t%s\n", i+1, r.GradeKey); err != nil {
			return err
		}
	}
	return nil
}

// ReadIndex reads a WriteIndex-shaped sidecar file into an n -> grade_key
// map.
func ReadIndex(r io.Reader) (map[int]string, error) {
	out := map[int]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n, key, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("index line %d: want \"n\\tgrade_key\", got %q", lineNo, line)
		}
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return nil, fmt.Errorf("index line %d: bad n %q: %w", lineNo, n, err)
		}
		out[i] = strings.TrimSpace(key)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GradeIndexLine is one parsed line of a plain-text "n score note..." grade
// sheet -- what a reviewer writes by hand (or a Claude reviewer emits)
// against a `pending --format compact` sheet and its sidecar index file.
type GradeIndexLine struct {
	N     int
	Score int
	Note  string
}

var gradeIndexLineRE = regexp.MustCompile(`^(\d+)\s+([0-2])(?:\s+(.*))?$`)

// ParseGradeIndexLines parses r as a plain-text "n score note..." sheet:
// one line per graded answer, n matching a `pending --format compact`
// sheet's `[n]` and its sidecar index file, score 0/1/2, and an optional
// free-text note filling the rest of the line. Blank lines and lines
// starting with "#" are ignored (so a reviewer can leave a comment or
// separator between cases).
func ParseGradeIndexLines(r io.Reader) ([]GradeIndexLine, error) {
	var out []GradeIndexLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := gradeIndexLineRE.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("line %d: want \"n score note...\" (score 0, 1, or 2), got %q", lineNo, line)
		}
		n, _ := strconv.Atoi(m[1])
		score, _ := strconv.Atoi(m[2])
		out = append(out, GradeIndexLine{N: n, Score: score, Note: strings.TrimSpace(m[3])})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GradeInputsFromIndex maps each parsed "n score note" line through idx
// (an n -> grade_key mapping loaded via ReadIndex) into GradeInputs ready
// for AppendGrades. Case/Answer are left empty (the plain-text sheet never
// carried them) -- both are informational-only fields on GradeRecord, not
// required by anything that reads grades.jsonl back.
func GradeInputsFromIndex(lines []GradeIndexLine, idx map[int]string) ([]GradeInput, error) {
	out := make([]GradeInput, 0, len(lines))
	for _, l := range lines {
		key, ok := idx[l.N]
		if !ok {
			return nil, fmt.Errorf("no index entry for [%d] (regenerate the index file from the same pending run, or check for a typo)", l.N)
		}
		score := l.Score
		out = append(out, GradeInput{GradeKey: key, Score: &score, Note: l.Note})
	}
	return out, nil
}

package bench

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPendingGroupsAndExcludesGraded(t *testing.T) {
	cases := []Case{
		{ID: "c1", Mode: "suggest", Input: "find big files", Reference: []string{"find ~ -type f -size +100M"}},
		{ID: "c2", Mode: "suggest", Input: "list dir"},
	}
	mk := func(caseID, provider, output string) TrialResult {
		return TrialResult{
			Case: caseID, Mode: "suggest", Shell: "zsh", OS: "macos", Provider: provider,
			Template: "v1", Output: output,
			GradeKey: GradeKey(caseID, "zsh", "macos", "suggest", output),
		}
	}

	results := []TrialResult{
		// c1: exact reference match -> auto-graded, excluded from pending.
		mk("c1", "p1", "find ~ -type f -size +100M"),
		// c1: two providers produce the SAME normalized (but differently
		// whitespaced) wrong answer -> one pending record, count 2, both
		// providers listed.
		mk("c1", "p1", "find  ~  -type f  -size +50M"),
		mk("c1", "p2", "find ~ -type f -size +50M"),
		// c2: one ungraded answer.
		mk("c2", "p1", "ls -la"),
		// c2: unavailable trial must never appear in pending.
		{Case: "c2", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p3", Unavailable: true},
	}

	recs := BuildPending(results, cases, map[string]GradeRecord{}, 0)
	if len(recs) != 2 {
		t.Fatalf("got %d pending records, want 2: %+v", len(recs), recs)
	}
	// Sorted by case id, so c1's duplicate-answer record comes first.
	if recs[0].Case != "c1" || recs[0].Count != 2 {
		t.Errorf("recs[0] = %+v, want case c1 count 2", recs[0])
	}
	if len(recs[0].Providers) != 2 || recs[0].Providers[0] != "p1" || recs[0].Providers[1] != "p2" {
		t.Errorf("recs[0].Providers = %v, want [p1 p2]", recs[0].Providers)
	}
	if recs[1].Case != "c2" || recs[1].Count != 1 {
		t.Errorf("recs[1] = %+v, want case c2 count 1", recs[1])
	}
}

func TestBuildPendingExcludesAlreadyGraded(t *testing.T) {
	cases := []Case{{ID: "c1", Mode: "suggest", Input: "x"}}
	r := TrialResult{Case: "c1", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p1", Output: "y"}
	r.GradeKey = GradeKey("c1", "zsh", "macos", "suggest", r.Output)
	grades := map[string]GradeRecord{r.GradeKey: {GradeKey: r.GradeKey, Score: 1, Grader: "claude"}}

	recs := BuildPending([]TrialResult{r}, cases, grades, 0)
	if len(recs) != 0 {
		t.Fatalf("already-graded trial should be excluded from pending, got %+v", recs)
	}
}

func TestBuildPendingLimit(t *testing.T) {
	cases := []Case{{ID: "c1", Mode: "suggest", Input: "x"}}
	var results []TrialResult
	for _, out := range []string{"a", "b", "c"} {
		results = append(results, TrialResult{
			Case: "c1", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p1", Output: out,
			GradeKey: GradeKey("c1", "zsh", "macos", "suggest", out),
		})
	}
	recs := BuildPending(results, cases, map[string]GradeRecord{}, 2)
	if len(recs) != 2 {
		t.Fatalf("limit not applied: got %d records, want 2", len(recs))
	}
}

// compactFixture builds two cases worth of pending records: one whose
// answer has an embedded newline and no flags, one with a flagged
// (missing-command) answer, to exercise WritePendingCompact/WriteIndex's
// full formatting contract.
func compactFixture() []PendingRecord {
	cleanChecks, _ := json.Marshal(Checks{FormatClean: true})
	flaggedChecks, _ := json.Marshal(Checks{FormatClean: true, MissingCommands: []string{"battctl"}})
	return []PendingRecord{
		{
			GradeKey: "key-explain", Case: "explain-lsof", Mode: "explain",
			Shell: "zsh", OS: "macos", Input: "lsof -nP -iTCP:8080",
			Rubric: "should mention listening TCP port 8080",
			Answer: "Lists\nprocesses", Checks: cleanChecks,
			Count: 1, Providers: []string{"p1"},
		},
		{
			GradeKey: "key-find-1", Case: "find-big", Mode: "suggest",
			Shell: "zsh", OS: "macos", Input: "find files over 100MB",
			Rubric:    "must use find with -size +100M",
			Reference: []string{"find ~ -type f -size +100M"},
			Answer:    "find ~ -type f -size +50M", Checks: cleanChecks,
			Count: 2, Providers: []string{"p1", "p2"},
		},
		{
			GradeKey: "key-find-2", Case: "find-big", Mode: "suggest",
			Shell: "zsh", OS: "macos", Input: "find files over 100MB",
			Rubric:    "must use find with -size +100M",
			Reference: []string{"find ~ -type f -size +100M"},
			Answer:    "battctl show", Checks: flaggedChecks,
			Count: 1, Providers: []string{"p3"},
		},
	}
}

func TestWritePendingCompact(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePendingCompact(&buf, compactFixture()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	// One header per case, not per answer.
	if strings.Count(got, "### explain-lsof [explain] :: lsof -nP -iTCP:8080") != 1 {
		t.Errorf("expected exactly one explain-lsof header, got:\n%s", got)
	}
	if strings.Count(got, "### find-big [suggest] :: find files over 100MB") != 1 {
		t.Errorf("expected exactly one find-big header, got:\n%s", got)
	}
	if !strings.Contains(got, "RUBRIC: must use find with -size +100M") {
		t.Errorf("missing RUBRIC line, got:\n%s", got)
	}
	if !strings.Contains(got, "REF: find ~ -type f -size +100M") {
		t.Errorf("missing REF line, got:\n%s", got)
	}
	// Global 1-based index, in emission order.
	if !strings.Contains(got, "[1] (zsh/macos x1 p1) Lists ⏎ processes") {
		t.Errorf("answer [1] not rendered as expected (newline -> ⏎), got:\n%s", got)
	}
	if !strings.Contains(got, "[2] (zsh/macos x2 p1, p2) find ~ -type f -size +50M\n") {
		t.Errorf("answer [2] not rendered as expected (no flags), got:\n%s", got)
	}
	if !strings.Contains(got, "[3] (zsh/macos x1 p3) battctl show {missing_commands=battctl}") {
		t.Errorf("answer [3] not rendered with its missing_commands flag, got:\n%s", got)
	}
}

func TestWritePendingCompact_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePendingCompact(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Nothing pending.") {
		t.Errorf("empty compact sheet = %q, want to mention nothing pending", buf.String())
	}
}

func TestCompactFlags(t *testing.T) {
	cases := []struct {
		name   string
		checks Checks
		want   string
	}{
		{"clean", Checks{FormatClean: true}, ""},
		{"parses false", Checks{Parses: boolPtr(false)}, "parses=false"},
		{"parses true (no flag)", Checks{Parses: boolPtr(true)}, ""},
		{"missing commands", Checks{MissingCommands: []string{"battctl", "jpegmini"}}, "missing_commands=battctl,jpegmini"},
		{"gnuisms", Checks{Gnuisms: []string{"sed-i-no-suffix"}}, "gnuisms=sed-i-no-suffix"},
		{"dangerous", Checks{Dangerous: []string{"rm-rf-root"}}, "dangerous=rm-rf-root"},
		{"everything at once", Checks{
			Parses: boolPtr(false), MissingCommands: []string{"x"}, Gnuisms: []string{"y"}, Dangerous: []string{"z"},
		}, "parses=false, missing_commands=x, gnuisms=y, dangerous=z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.checks)
			if err != nil {
				t.Fatal(err)
			}
			if got := compactFlags(b); got != c.want {
				t.Errorf("compactFlags(%s) = %q, want %q", b, got, c.want)
			}
		})
	}
	if got := compactFlags(nil); got != "" {
		t.Errorf("compactFlags(nil) = %q, want \"\"", got)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestWriteReadIndexRoundTrip(t *testing.T) {
	recs := compactFixture()
	var buf bytes.Buffer
	if err := WriteIndex(&buf, recs); err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "key-explain", 2: "key-find-1", 3: "key-find-2"}
	if len(got) != len(want) {
		t.Fatalf("ReadIndex() = %v, want %v", got, want)
	}
	for n, key := range want {
		if got[n] != key {
			t.Errorf("ReadIndex()[%d] = %q, want %q", n, got[n], key)
		}
	}
}

func TestReadIndex_MalformedLine(t *testing.T) {
	if _, err := ReadIndex(strings.NewReader("not-a-valid-line-no-tab\n")); err == nil {
		t.Error("ReadIndex with a malformed line: want error")
	}
	if _, err := ReadIndex(strings.NewReader("abc\tkey\n")); err == nil {
		t.Error("ReadIndex with a non-numeric n: want error")
	}
}

func TestParseGradeIndexLines(t *testing.T) {
	in := "# a comment\n" +
		"\n" +
		"1 2 clean explain, no markdown\n" +
		"2 1 works but not the canonical +100M spelling\n" +
		"3 0 battctl isn't installed\n"
	got, err := ParseGradeIndexLines(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []GradeIndexLine{
		{N: 1, Score: 2, Note: "clean explain, no markdown"},
		{N: 2, Score: 1, Note: "works but not the canonical +100M spelling"},
		{N: 3, Score: 0, Note: "battctl isn't installed"},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseGradeIndexLines() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseGradeIndexLines_NoNote(t *testing.T) {
	got, err := ParseGradeIndexLines(strings.NewReader("5 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (GradeIndexLine{N: 5, Score: 2, Note: ""}) {
		t.Errorf("ParseGradeIndexLines(no note) = %+v", got)
	}
}

func TestParseGradeIndexLines_BadInput(t *testing.T) {
	cases := []string{
		"not a valid line",
		"1 3 score out of range", // 3 isn't 0/1/2
		"1 -1 negative score",
	}
	for _, in := range cases {
		if _, err := ParseGradeIndexLines(strings.NewReader(in)); err == nil {
			t.Errorf("ParseGradeIndexLines(%q): want error", in)
		}
	}
}

func TestGradeInputsFromIndex(t *testing.T) {
	idx := map[int]string{1: "key-a", 2: "key-b"}
	lines := []GradeIndexLine{
		{N: 1, Score: 2, Note: "great"},
		{N: 2, Score: 0, Note: ""},
	}
	got, err := GradeInputsFromIndex(lines, idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("GradeInputsFromIndex() = %+v, want 2 entries", got)
	}
	if got[0].GradeKey != "key-a" || got[0].Score == nil || *got[0].Score != 2 || got[0].Note != "great" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].GradeKey != "key-b" || got[1].Score == nil || *got[1].Score != 0 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestGradeInputsFromIndex_MissingEntry(t *testing.T) {
	idx := map[int]string{1: "key-a"}
	lines := []GradeIndexLine{{N: 99, Score: 1}}
	if _, err := GradeInputsFromIndex(lines, idx); err == nil {
		t.Error("GradeInputsFromIndex with an n not in the index: want error")
	}
}

// TestCompactSheetRoundTripsThroughIndex is the end-to-end contract: build
// pending records, render them compact + write the index, parse a graded
// plain-text sheet against that index, and confirm every grade lands on
// the right grade_key with the right score/note -- exactly the path a
// reviewer (human or Claude) exercises grading a compact sheet by hand.
func TestCompactSheetRoundTripsThroughIndex(t *testing.T) {
	recs := compactFixture()

	var sheet bytes.Buffer
	if err := WritePendingCompact(&sheet, recs); err != nil {
		t.Fatal(err)
	}
	var idxBuf bytes.Buffer
	if err := WriteIndex(&idxBuf, recs); err != nil {
		t.Fatal(err)
	}
	idx, err := ReadIndex(&idxBuf)
	if err != nil {
		t.Fatal(err)
	}

	graded := "1 2 clean explain\n2 1 works, not canonical\n3 0 wrong tool entirely\n"
	lines, err := ParseGradeIndexLines(strings.NewReader(graded))
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := GradeInputsFromIndex(lines, idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 {
		t.Fatalf("got %d inputs, want 3", len(inputs))
	}
	wantKeys := []string{"key-explain", "key-find-1", "key-find-2"}
	wantScores := []int{2, 1, 0}
	for i, in := range inputs {
		if in.GradeKey != wantKeys[i] {
			t.Errorf("inputs[%d].GradeKey = %q, want %q", i, in.GradeKey, wantKeys[i])
		}
		if in.Score == nil || *in.Score != wantScores[i] {
			t.Errorf("inputs[%d].Score = %v, want %d", i, in.Score, wantScores[i])
		}
	}
}

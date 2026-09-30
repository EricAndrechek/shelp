package bench

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseGradeInputsValidation(t *testing.T) {
	ok := `{"grade_key":"abc","case":"c1","answer":"x","score":2,"note":"good"}`
	if ins, err := ParseGradeInputs(strings.NewReader(ok), false); err != nil || len(ins) != 1 {
		t.Fatalf("valid line: got %v, %v", ins, err)
	}

	noKey := `{"score":1}`
	if _, err := ParseGradeInputs(strings.NewReader(noKey), false); err == nil {
		t.Error("missing grade_key: want error")
	}

	noScore := `{"grade_key":"abc"}`
	if _, err := ParseGradeInputs(strings.NewReader(noScore), false); err == nil {
		t.Error("missing score: want error")
	}

	badScore := `{"grade_key":"abc","score":3}`
	if _, err := ParseGradeInputs(strings.NewReader(badScore), false); err == nil {
		t.Error("out-of-range score: want error")
	}

	unknown := `{"grade_key":"abc","score":1,"typo_field":"oops"}`
	if _, err := ParseGradeInputs(strings.NewReader(unknown), false); err == nil {
		t.Error("unknown field without --force: want error")
	}
	if ins, err := ParseGradeInputs(strings.NewReader(unknown), true); err != nil || len(ins) != 1 {
		t.Errorf("unknown field with --force: want 1 input, no error; got %v, %v", ins, err)
	}
}

func TestAppendAndLoadGradesOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grades.jsonl")

	s0, s1 := 0, 1
	in1 := GradeInput{GradeKey: "k1", Case: "c1", Answer: "a1", Score: &s0, Note: "first pass"}
	if err := AppendGrades(path, []GradeInput{in1}, "claude", time.Unix(1000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	grades, err := LoadGrades(path)
	if err != nil {
		t.Fatal(err)
	}
	if grades["k1"].Score != 0 || grades["k1"].Grader != "claude" {
		t.Fatalf("unexpected first grade: %+v", grades["k1"])
	}

	// Regrade the same key: a later write must override the earlier one.
	in2 := GradeInput{GradeKey: "k1", Case: "c1", Answer: "a1", Score: &s1, Note: "reconsidered"}
	if err := AppendGrades(path, []GradeInput{in2}, "eric", time.Unix(2000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	grades, err = LoadGrades(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(grades) != 1 {
		t.Fatalf("want 1 grade after override, got %d", len(grades))
	}
	if grades["k1"].Score != 1 || grades["k1"].Grader != "eric" || grades["k1"].Note != "reconsidered" {
		t.Fatalf("override didn't win: %+v", grades["k1"])
	}
}

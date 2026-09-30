package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// DefaultGradesPath is where `shelp-bench grade` appends and `report`/
// `pending` read from, relative to the repo root.
const DefaultGradesPath = "bench/grades.jsonl"

// GradeInput is one line a reviewer feeds to `shelp-bench grade`: normally
// exactly what `pending --format jsonl` emitted (PendingRecord), with
// "score" filled in and "note" optionally edited. Score is a pointer so a
// missing field is distinguishable from an explicit 0.
type GradeInput struct {
	GradeKey  string          `json:"grade_key"`
	Case      string          `json:"case,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Shell     string          `json:"shell,omitempty"`
	OS        string          `json:"os,omitempty"`
	Input     string          `json:"input,omitempty"`
	Rubric    string          `json:"rubric,omitempty"`
	Reference []string        `json:"reference,omitempty"`
	Answer    string          `json:"answer,omitempty"`
	Checks    json.RawMessage `json:"checks,omitempty"`
	Count     int             `json:"count,omitempty"`
	Providers []string        `json:"providers,omitempty"`
	Score     *int            `json:"score"`
	Note      string          `json:"note,omitempty"`
}

// GradeRecord is one line of bench/grades.jsonl: the durable, compact grade
// store. Later records for the same GradeKey override earlier ones.
type GradeRecord struct {
	GradeKey string    `json:"grade_key"`
	Case     string    `json:"case,omitempty"`
	Answer   string    `json:"answer,omitempty"`
	Score    int       `json:"score"`
	Note     string    `json:"note,omitempty"`
	Grader   string    `json:"grader"`
	GradedAt time.Time `json:"graded_at"`
}

// ParseGradeInputs decodes jsonl grade-input lines from r. By default,
// unknown JSON keys are rejected (a typo'd field name fails loudly instead
// of being silently dropped); force lets them through, ignored.
func ParseGradeInputs(r io.Reader, force bool) ([]GradeInput, error) {
	var out []GradeInput
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var in GradeInput
		dec := json.NewDecoder(bytes.NewReader(line))
		if !force {
			dec.DisallowUnknownFields()
		}
		if err := dec.Decode(&in); err != nil {
			return nil, fmt.Errorf("line %d: %w (use --force to ignore unknown fields)", lineNo, err)
		}
		if in.GradeKey == "" {
			return nil, fmt.Errorf("line %d: missing grade_key", lineNo)
		}
		if in.Score == nil {
			return nil, fmt.Errorf("line %d: missing score", lineNo)
		}
		if *in.Score < 0 || *in.Score > 2 {
			return nil, fmt.Errorf("line %d: score %d out of range (want 0, 1, or 2)", lineNo, *in.Score)
		}
		out = append(out, in)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AppendGrades appends one GradeRecord per input to the grades file at
// path, stamping grader and graded_at. It does not deduplicate: later
// readers (LoadGrades) apply last-write-wins by grade_key.
func AppendGrades(path string, inputs []GradeInput, grader string, now time.Time) error {
	if len(inputs) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, in := range inputs {
		rec := GradeRecord{
			GradeKey: in.GradeKey, Case: in.Case, Answer: in.Answer,
			Score: *in.Score, Note: in.Note, Grader: grader, GradedAt: now,
		}
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return f.Sync()
}

// LoadGrades reads a grades.jsonl file into a map keyed by grade_key, with
// later lines overriding earlier ones (regrading). A missing file yields an
// empty map.
func LoadGrades(path string) (map[string]GradeRecord, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]GradeRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]GradeRecord{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec GradeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[rec.GradeKey] = rec
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

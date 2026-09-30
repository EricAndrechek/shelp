package bench

// ResolvedGrade is a trial's final grade, whichever source it came from.
type ResolvedGrade struct {
	Score  int
	Grader string // "reference" or a human/model grader name from grades.jsonl
	Note   string
	Graded bool
}

// Resolve looks up r's grade: an exact (normalized) match against the
// case's reference answers auto-grades 2 as "reference" without touching
// grades.jsonl; otherwise it's whatever grades has recorded for the trial's
// grade_key, if anything.
func Resolve(r TrialResult, c Case, grades map[string]GradeRecord) ResolvedGrade {
	if !r.Unavailable && r.Error == "" && MatchesReference(r.Mode, r.Output, c.Reference) {
		return ResolvedGrade{Score: 2, Grader: "reference", Graded: true}
	}
	if g, ok := grades[r.GradeKey]; ok {
		return ResolvedGrade{Score: g.Score, Grader: g.Grader, Note: g.Note, Graded: true}
	}
	return ResolvedGrade{}
}

// CaseIndex indexes cases by ID for quick lookup while walking results.
func CaseIndex(cases []Case) map[string]Case {
	m := make(map[string]Case, len(cases))
	for _, c := range cases {
		m[c.ID] = c
	}
	return m
}

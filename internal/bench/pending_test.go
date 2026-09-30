package bench

import "testing"

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

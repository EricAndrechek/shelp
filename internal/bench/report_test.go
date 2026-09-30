package bench

import "testing"

// fixture builds a small, hand-computable set of results:
//
//	c1 (easy, suggest, reference "echo hi"), provider p1, template v1:
//	  sample 1: "echo hi"  -> matches reference -> auto score 2, cold
//	  sample 2: "echo yo"  -> no grade recorded -> ungraded
//	c2 (hard, explain), provider p1, template v1:
//	  sample 1: "it lists things" -> ungraded
func fixture() ([]TrialResult, []Case) {
	cases := []Case{
		{ID: "c1", Mode: "suggest", Difficulty: "easy", Reference: []string{"echo hi"}},
		{ID: "c2", Mode: "explain", Difficulty: "hard"},
	}
	results := []TrialResult{
		{
			Case: "c1", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p1", Template: "v1",
			Sample: 1, Output: "echo hi", LatencyMS: 100, Cold: true,
			Checks:   Checks{FormatClean: true},
			GradeKey: GradeKey("c1", "zsh", "macos", "suggest", "echo hi"),
		},
		{
			Case: "c1", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p1", Template: "v1",
			Sample: 2, Output: "echo yo", LatencyMS: 120,
			Checks:   Checks{FormatClean: true},
			GradeKey: GradeKey("c1", "zsh", "macos", "suggest", "echo yo"),
		},
		{
			Case: "c2", Mode: "explain", Shell: "zsh", OS: "macos", Provider: "p1", Template: "v1",
			Sample: 1, Output: "it lists things", LatencyMS: 50, FirstChunkMS: 20,
			Checks:   Checks{FormatClean: true},
			GradeKey: GradeKey("c2", "zsh", "macos", "explain", "it lists things"),
		},
	}
	return results, cases
}

func TestReportSummaryTable(t *testing.T) {
	results, cases := fixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	want := "| Provider | Template | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% | Unavailable% | Suggest p50/p90 | Explain first-chunk p50 | Explain total p50 | Cold p50 |\n" +
		"|---|---|---|---|---|---|---|---|---|---|---|---|\n" +
		"| p1 | v1 | 3 | 33% | 100% | 100% | 100% | 0% | 100ms/120ms | 20ms | 50ms | 100ms |\n"
	if got := rep.SummaryTable(); got != want {
		t.Errorf("SummaryTable() =\n%s\nwant\n%s", got, want)
	}
}

func TestReportDifficultyTable(t *testing.T) {
	results, cases := fixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	want := "| Difficulty | Trials | Graded% | Mean score% | Fully-correct% |\n|---|---|---|---|---|\n" +
		"| easy | 2 | 50% | 100% | 100% |\n" +
		"| hard | 1 | 0% | 0% | 0% |\n"
	if got := rep.DifficultyTable(); got != want {
		t.Errorf("DifficultyTable() =\n%s\nwant\n%s", got, want)
	}
}

func TestReportModeTable(t *testing.T) {
	results, cases := fixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	want := "| Mode | Trials | Graded% | Mean score% | Fully-correct% |\n|---|---|---|---|---|\n" +
		"| suggest | 2 | 50% | 100% | 100% |\n" +
		"| explain | 1 | 0% | 0% | 0% |\n"
	if got := rep.ModeTable(); got != want {
		t.Errorf("ModeTable() =\n%s\nwant\n%s", got, want)
	}
}

func TestReportCaseGrid(t *testing.T) {
	results, cases := fixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	want := "| Case | p1 |\n|---|---|\n| c1 | ✅ 2.0 |\n| c2 | · |\n"
	if got := rep.CaseGrid(); got != want {
		t.Errorf("CaseGrid() =\n%q\nwant\n%q", got, want)
	}
}

func TestReportTemplateTable(t *testing.T) {
	results, cases := fixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	got, common := rep.TemplateTable()
	if len(common) != 1 || common[0] != "p1" {
		t.Fatalf("common providers = %v, want [p1]", common)
	}
	want := "| Template | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% |\n|---|---|---|---|---|---|\n" +
		"| v1 | 3 | 33% | 100% | 100% | 100% |\n"
	if got != want {
		t.Errorf("TemplateTable() =\n%s\nwant\n%s", got, want)
	}
}

func TestReportUnavailableCountedNotAveraged(t *testing.T) {
	results := []TrialResult{
		{Case: "c1", Mode: "suggest", Provider: "p1", Template: "v1", Unavailable: true, Error: "p1: offline"},
	}
	cases := []Case{{ID: "c1", Mode: "suggest"}}
	rep := Compute(results, cases, map[string]GradeRecord{})
	s := rep.ByProviderTemplate[ptKey{"p1", "v1"}]
	if s.Trials != 1 || s.Unavailable != 1 || s.Answered != 0 {
		t.Fatalf("unexpected stats for unavailable trial: %+v", s)
	}
	if pct := s.UnavailablePct(); pct != 100 {
		t.Errorf("UnavailablePct() = %v, want 100", pct)
	}
}

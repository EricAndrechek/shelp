package bench

import (
	"strings"
	"testing"
)

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

// multiContextFixture builds two runs of the same provider/template pair
// against case c1, one made with --context os,shell and one with
// --context os,shell,tools -- the scenario report grouping must keep apart
// rather than silently merge into one row.
func multiContextFixture() ([]TrialResult, []Case) {
	cases := []Case{
		{ID: "c1", Mode: "suggest", Difficulty: "easy", Reference: []string{"echo hi"}},
	}
	results := []TrialResult{
		{
			Case: "c1", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p1", Template: "v1",
			Context: ContextFields{OS: "macOS 27.0", Userland: "BSD", Shell: "zsh"},
			Sample:  1, Output: "echo hi", LatencyMS: 100,
			Checks:   Checks{FormatClean: true},
			GradeKey: GradeKey("c1", "zsh", "macos", "suggest", "echo hi"),
		},
		{
			Case: "c1", Mode: "suggest", Shell: "zsh", OS: "macos", Provider: "p1", Template: "v1",
			Context: ContextFields{OS: "macOS 27.0", Userland: "BSD", Shell: "zsh", Tools: []string{"git"}},
			Sample:  1, Output: "echo nope", LatencyMS: 200,
			Checks:   Checks{FormatClean: true},
			GradeKey: GradeKey("c1", "zsh", "macos", "suggest", "echo nope"),
		},
	}
	return results, cases
}

func TestReportGroupsByContext(t *testing.T) {
	results, cases := multiContextFixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	if !rep.MultiContext() {
		t.Fatalf("MultiContext() = false, want true (two distinct context sets)")
	}
	if got := rep.ByProviderTemplate[ptKey{"p1", "v1", "os,shell"}]; got == nil || got.Trials != 1 {
		t.Fatalf("os,shell row = %+v, want a single-trial row", got)
	}
	if got := rep.ByProviderTemplate[ptKey{"p1", "v1", "os,shell,tools"}]; got == nil || got.Trials != 1 {
		t.Fatalf("os,shell,tools row = %+v, want a single-trial row", got)
	}
	// They must NOT have been merged into one row.
	if len(rep.ByProviderTemplate) != 2 {
		t.Fatalf("ByProviderTemplate has %d rows, want 2 (one per context set)", len(rep.ByProviderTemplate))
	}
}

func TestReportSummaryTableShowsContextColumnWhenMultiple(t *testing.T) {
	results, cases := multiContextFixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	got := rep.SummaryTable()
	if !strings.Contains(got, "| Context |") {
		t.Errorf("SummaryTable() with multiple context sets should have a Context column, got:\n%s", got)
	}
	if !strings.Contains(got, "os,shell,tools") || !strings.Contains(got, "os,shell |") {
		t.Errorf("SummaryTable() should list both context sets, got:\n%s", got)
	}
}

func TestReportSummaryTableOmitsContextColumnWhenSingle(t *testing.T) {
	results, cases := fixture()
	rep := Compute(results, cases, map[string]GradeRecord{})
	if rep.MultiContext() {
		t.Fatalf("fixture() should have a single context set")
	}
	got := rep.SummaryTable()
	if strings.Contains(got, "Context") {
		t.Errorf("SummaryTable() with a single context set should not have a Context column, got:\n%s", got)
	}
}

func TestReportTemplateTableSeparatesByContext(t *testing.T) {
	// Same template (v1), same provider (p1), but two different context
	// sets: the template comparison must keep them apart, not average them
	// into one row.
	cases := []Case{{ID: "c1", Mode: "suggest", Reference: []string{"echo hi"}}}
	results := []TrialResult{
		{Case: "c1", Mode: "suggest", Provider: "p1", Template: "v1",
			Context: ContextFields{OS: "macOS 27.0", Shell: "zsh"},
			Output:  "echo hi", GradeKey: GradeKey("c1", "", "", "suggest", "echo hi")},
		{Case: "c1", Mode: "suggest", Provider: "p1", Template: "v1",
			Context: ContextFields{OS: "macOS 27.0", Shell: "zsh", Tools: []string{"git"}},
			Output:  "echo bad", GradeKey: GradeKey("c1", "", "", "suggest", "echo bad")},
	}
	rep := Compute(results, cases, map[string]GradeRecord{})
	table, common := rep.TemplateTable()
	if !strings.Contains(table, "| Context |") {
		t.Errorf("TemplateTable() with multiple contexts should have a Context column, got:\n%s", table)
	}
	// Two rows for v1 (one per context set).
	count := strings.Count(table, "| v1 |")
	if count != 2 {
		t.Errorf("TemplateTable() should have 2 rows for v1 (one per context), got %d in:\n%s", count, table)
	}
	if len(common) != 1 || common[0] != "p1" {
		t.Errorf("commonProviders = %v, want [p1] (p1 is common within each context individually)", common)
	}
}

func TestReportUnavailableCountedNotAveraged(t *testing.T) {
	results := []TrialResult{
		{Case: "c1", Mode: "suggest", Provider: "p1", Template: "v1", Unavailable: true, Error: "p1: offline"},
	}
	cases := []Case{{ID: "c1", Mode: "suggest"}}
	rep := Compute(results, cases, map[string]GradeRecord{})
	s := rep.ByProviderTemplate[ptKey{"p1", "v1", ""}]
	if s.Trials != 1 || s.Unavailable != 1 || s.Answered != 0 {
		t.Fatalf("unexpected stats for unavailable trial: %+v", s)
	}
	if pct := s.UnavailablePct(); pct != 100 {
		t.Errorf("UnavailablePct() = %v, want 100", pct)
	}
}

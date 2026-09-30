package bench

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// LoadRuns reads results.jsonl (and run.json, best-effort) from each run
// directory and concatenates them.
func LoadRuns(dirs []string) ([]TrialResult, []Manifest, error) {
	var results []TrialResult
	var manifests []Manifest
	for _, d := range dirs {
		rs, err := LoadResults(d)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", d, err)
		}
		results = append(results, rs...)
		if m, err := LoadManifest(d); err == nil {
			manifests = append(manifests, m)
		}
	}
	return results, manifests, nil
}

// Stats aggregates one slice of trials.
type Stats struct {
	Trials       int
	Answered     int // no error, not unavailable
	Unavailable  int
	Graded       int
	ScoreSum     int
	FullyCorrect int
	FormatCleanN int
}

func (s *Stats) add(r TrialResult, g ResolvedGrade) {
	s.Trials++
	if r.Unavailable {
		s.Unavailable++
		return
	}
	if r.Error != "" {
		return
	}
	s.Answered++
	if r.Checks.FormatClean {
		s.FormatCleanN++
	}
	if g.Graded {
		s.Graded++
		s.ScoreSum += g.Score
		if g.Score == 2 {
			s.FullyCorrect++
		}
	}
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d) * 100
}

func (s Stats) GradedPct() float64       { return pct(s.Graded, s.Trials) }
func (s Stats) MeanScorePct() float64    { return pct(s.ScoreSum, s.Graded*2) }
func (s Stats) FullyCorrectPct() float64 { return pct(s.FullyCorrect, s.Graded) }
func (s Stats) FormatCleanPct() float64  { return pct(s.FormatCleanN, s.Answered) }
func (s Stats) UnavailablePct() float64  { return pct(s.Unavailable, s.Trials) }

// latencies accumulates timing samples for percentiles.
type latencies struct {
	suggest      []int64
	explainFirst []int64
	explainTotal []int64
	cold         []int64
}

func percentile(vals []int64, p float64) (int64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(math.Ceil(p/100*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx], true
}

func fmtMS(v int64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%dms", v)
}

func fmtPct(v float64) string { return fmt.Sprintf("%.0f%%", v) }

// ptKey groups by provider+template+context. context is the context-field
// set label from ContextSet (e.g. "os,shell" or "os,shell,tools"): two runs
// of the same provider/template made with different --context flags get
// distinct keys instead of being silently merged into one row.
type ptKey struct{ provider, template, context string }

// Report holds every computed table for the markdown report.
type Report struct {
	ByProviderTemplate map[ptKey]*Stats
	LatByPT            map[ptKey]*latencies
	ByDifficulty       map[string]*Stats
	ByMode             map[string]*Stats
	ByCaseProvider     map[[2]string]*Stats // [case, provider]
	Providers          []string
	Templates          []string
	Cases              []string
	// Contexts lists every distinct context-field set seen across the
	// input results, sorted. Usually has one element.
	Contexts  []string
	CaseCount int
	MaxSample int
}

// MultiContext reports whether more than one context-field set appears in
// the input results. Report tables show a Context column only when this is
// true, so a single-context report (the common case) renders identically
// to before this field existed.
func (rep Report) MultiContext() bool { return len(rep.Contexts) > 1 }

// Compute builds every table from raw trials.
func Compute(results []TrialResult, cases []Case, grades map[string]GradeRecord) Report {
	idx := CaseIndex(cases)
	rep := Report{
		ByProviderTemplate: map[ptKey]*Stats{},
		LatByPT:            map[ptKey]*latencies{},
		ByDifficulty:       map[string]*Stats{},
		ByMode:             map[string]*Stats{},
		ByCaseProvider:     map[[2]string]*Stats{},
	}
	provSeen := map[string]bool{}
	tmplSeen := map[string]bool{}
	caseSeen := map[string]bool{}
	ctxSeen := map[string]bool{}
	for _, r := range results {
		c, ok := idx[r.Case]
		if !ok {
			continue // result from a case not in the current case set; skip
		}
		g := Resolve(r, c, grades)

		provSeen[r.Provider] = true
		tmplSeen[r.Template] = true
		caseSeen[r.Case] = true
		if r.Sample > rep.MaxSample {
			rep.MaxSample = r.Sample
		}

		ctxSet := ContextSet(r.Context)
		ctxSeen[ctxSet] = true
		pt := ptKey{r.Provider, r.Template, ctxSet}
		if rep.ByProviderTemplate[pt] == nil {
			rep.ByProviderTemplate[pt] = &Stats{}
			rep.LatByPT[pt] = &latencies{}
		}
		rep.ByProviderTemplate[pt].add(r, g)
		if !r.Unavailable && r.Error == "" {
			lat := rep.LatByPT[pt]
			switch r.Mode {
			case "explain":
				lat.explainFirst = append(lat.explainFirst, r.FirstChunkMS)
				lat.explainTotal = append(lat.explainTotal, r.LatencyMS)
			default:
				lat.suggest = append(lat.suggest, r.LatencyMS)
			}
			if r.Cold {
				lat.cold = append(lat.cold, r.LatencyMS)
			}
		}

		diff := c.Difficulty
		if diff == "" {
			diff = "unrated"
		}
		if rep.ByDifficulty[diff] == nil {
			rep.ByDifficulty[diff] = &Stats{}
		}
		rep.ByDifficulty[diff].add(r, g)

		if rep.ByMode[r.Mode] == nil {
			rep.ByMode[r.Mode] = &Stats{}
		}
		rep.ByMode[r.Mode].add(r, g)

		cp := [2]string{r.Case, r.Provider}
		if rep.ByCaseProvider[cp] == nil {
			rep.ByCaseProvider[cp] = &Stats{}
		}
		rep.ByCaseProvider[cp].add(r, g)
	}
	for p := range provSeen {
		rep.Providers = append(rep.Providers, p)
	}
	sort.Strings(rep.Providers)
	for t := range tmplSeen {
		rep.Templates = append(rep.Templates, t)
	}
	sort.Strings(rep.Templates)
	for c := range caseSeen {
		rep.Cases = append(rep.Cases, c)
	}
	sort.Strings(rep.Cases)
	for ctxSet := range ctxSeen {
		rep.Contexts = append(rep.Contexts, ctxSet)
	}
	sort.Strings(rep.Contexts)
	rep.CaseCount = len(rep.Cases)
	return rep
}

// SummaryTable renders table (a): provider x template (x context, when more
// than one context-field set appears in the input — see MultiContext).
func (rep Report) SummaryTable() string {
	multi := rep.MultiContext()
	var b strings.Builder
	if multi {
		b.WriteString("| Provider | Template | Context | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% | Unavailable% | Suggest p50/p90 | Explain first-chunk p50 | Explain total p50 | Cold p50 |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	} else {
		b.WriteString("| Provider | Template | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% | Unavailable% | Suggest p50/p90 | Explain first-chunk p50 | Explain total p50 | Cold p50 |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	}
	keys := ptKeysSorted(rep.ByProviderTemplate)
	for _, k := range keys {
		s := rep.ByProviderTemplate[k]
		lat := rep.LatByPT[k]
		sp50, sp50ok := percentile(lat.suggest, 50)
		sp90, sp90ok := percentile(lat.suggest, 90)
		efp50, efok := percentile(lat.explainFirst, 50)
		etp50, etok := percentile(lat.explainTotal, 50)
		cp50, cpok := percentile(lat.cold, 50)
		if multi {
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s | %s | %s | %s | %s/%s | %s | %s | %s |\n",
				k.provider, k.template, k.context, s.Trials, fmtPct(s.GradedPct()), fmtPct(s.MeanScorePct()),
				fmtPct(s.FullyCorrectPct()), fmtPct(s.FormatCleanPct()), fmtPct(s.UnavailablePct()),
				fmtMS(sp50, sp50ok), fmtMS(sp90, sp90ok), fmtMS(efp50, efok), fmtMS(etp50, etok), fmtMS(cp50, cpok))
		} else {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s | %s | %s/%s | %s | %s | %s |\n",
				k.provider, k.template, s.Trials, fmtPct(s.GradedPct()), fmtPct(s.MeanScorePct()),
				fmtPct(s.FullyCorrectPct()), fmtPct(s.FormatCleanPct()), fmtPct(s.UnavailablePct()),
				fmtMS(sp50, sp50ok), fmtMS(sp90, sp90ok), fmtMS(efp50, efok), fmtMS(etp50, etok), fmtMS(cp50, cpok))
		}
	}
	return b.String()
}

func ptKeysSorted(m map[ptKey]*Stats) []ptKey {
	var keys []ptKey
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider != keys[j].provider {
			return keys[i].provider < keys[j].provider
		}
		if keys[i].template != keys[j].template {
			return keys[i].template < keys[j].template
		}
		return keys[i].context < keys[j].context
	})
	return keys
}

// DifficultyTable renders breakdown (b), by difficulty.
func (rep Report) DifficultyTable() string {
	order := []string{"easy", "medium", "hard", "unrated"}
	var b strings.Builder
	b.WriteString("| Difficulty | Trials | Graded% | Mean score% | Fully-correct% |\n|---|---|---|---|---|\n")
	for _, d := range order {
		s, ok := rep.ByDifficulty[d]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", d, s.Trials, fmtPct(s.GradedPct()), fmtPct(s.MeanScorePct()), fmtPct(s.FullyCorrectPct()))
	}
	return b.String()
}

// ModeTable renders breakdown (b), by mode.
func (rep Report) ModeTable() string {
	order := []string{"suggest", "explain"}
	var b strings.Builder
	b.WriteString("| Mode | Trials | Graded% | Mean score% | Fully-correct% |\n|---|---|---|---|---|\n")
	for _, m := range order {
		s, ok := rep.ByMode[m]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", m, s.Trials, fmtPct(s.GradedPct()), fmtPct(s.MeanScorePct()), fmtPct(s.FullyCorrectPct()))
	}
	return b.String()
}

func gradeCell(s *Stats) string {
	if s == nil || s.Graded == 0 {
		return "·"
	}
	mean := float64(s.ScoreSum) / float64(s.Graded)
	rounded := int(math.Round(mean))
	switch rounded {
	case 2:
		return fmt.Sprintf("✅ %.1f", mean)
	case 1:
		return fmt.Sprintf("\U0001F7E1 %.1f", mean)
	default:
		return fmt.Sprintf("❌ %.1f", mean)
	}
}

// CaseGrid renders (c): rows = cases, cols = providers, cell = mean score
// (averaged over every template/shell/os/sample for that case+provider).
func (rep Report) CaseGrid() string {
	var b strings.Builder
	b.WriteString("| Case |")
	for _, p := range rep.Providers {
		fmt.Fprintf(&b, " %s |", p)
	}
	b.WriteString("\n|---|")
	for range rep.Providers {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, c := range rep.Cases {
		fmt.Fprintf(&b, "| %s |", c)
		for _, p := range rep.Providers {
			s := rep.ByCaseProvider[[2]string{c, p}]
			fmt.Fprintf(&b, " %s |", gradeCell(s))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TemplateTable renders (d): templates compared on the same provider set
// (the providers present for every template in this report), within the
// same context-field set — a provider that ran template v1 under
// "os,shell" and v2 under "os,shell,tools" is never treated as common
// between them, since their prompts differed. When more than one
// context-field set appears in the input (MultiContext), a Context column
// is added and the provider-commonality computation, and each row's
// aggregate, are done separately per context-field set; commonProviders is
// then the union of every context's own common-provider set (each row's own
// commonality is what actually gates its numbers, this return value is only
// used for the summary line callers print). With a single context (the
// common case), this renders identically to before context-awareness was
// added.
func (rep Report) TemplateTable() (table string, commonProviders []string) {
	if len(rep.Templates) == 0 {
		return "(no data)\n", nil
	}
	multi := rep.MultiContext()
	contexts := rep.Contexts
	if len(contexts) == 0 {
		contexts = []string{""}
	}

	commonSet := map[string]bool{}
	var b strings.Builder
	if multi {
		b.WriteString("| Template | Context | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% |\n|---|---|---|---|---|---|---|\n")
	} else {
		b.WriteString("| Template | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% |\n|---|---|---|---|---|---|\n")
	}

	for _, ctxSet := range contexts {
		present := map[string]map[string]bool{} // template -> provider set, within this context
		for k := range rep.ByProviderTemplate {
			if k.context != ctxSet {
				continue
			}
			if present[k.template] == nil {
				present[k.template] = map[string]bool{}
			}
			present[k.template][k.provider] = true
		}
		var templatesHere []string
		for _, t := range rep.Templates {
			if present[t] != nil {
				templatesHere = append(templatesHere, t)
			}
		}
		if len(templatesHere) == 0 {
			continue
		}
		common := map[string]bool{}
		for p := range present[templatesHere[0]] {
			inAll := true
			for _, t := range templatesHere {
				if !present[t][p] {
					inAll = false
					break
				}
			}
			if inAll {
				common[p] = true
			}
		}
		var commonHere []string
		for p := range common {
			commonHere = append(commonHere, p)
			commonSet[p] = true
		}
		sort.Strings(commonHere)

		for _, t := range templatesHere {
			agg := &Stats{}
			for _, p := range commonHere {
				if s := rep.ByProviderTemplate[ptKey{p, t, ctxSet}]; s != nil {
					agg.Trials += s.Trials
					agg.Answered += s.Answered
					agg.Unavailable += s.Unavailable
					agg.Graded += s.Graded
					agg.ScoreSum += s.ScoreSum
					agg.FullyCorrect += s.FullyCorrect
					agg.FormatCleanN += s.FormatCleanN
				}
			}
			if multi {
				fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s |\n", t, ctxSet, agg.Trials, fmtPct(agg.GradedPct()), fmtPct(agg.MeanScorePct()), fmtPct(agg.FullyCorrectPct()), fmtPct(agg.FormatCleanPct()))
			} else {
				fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s |\n", t, agg.Trials, fmtPct(agg.GradedPct()), fmtPct(agg.MeanScorePct()), fmtPct(agg.FullyCorrectPct()), fmtPct(agg.FormatCleanPct()))
			}
		}
	}

	for p := range commonSet {
		commonProviders = append(commonProviders, p)
	}
	sort.Strings(commonProviders)
	return b.String(), commonProviders
}

// Render produces the full bench/RESULTS.md content.
func (rep Report) Render(now time.Time) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# shelp bench results")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "_generated %s · %d cases · up to %d sample(s) per trial_\n\n", now.UTC().Format("2006-01-02"), rep.CaseCount, rep.MaxSample)

	fmt.Fprintln(&b, "## Summary: provider x template")
	fmt.Fprintln(&b)
	b.WriteString(rep.SummaryTable())
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## By difficulty")
	fmt.Fprintln(&b)
	b.WriteString(rep.DifficultyTable())
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## By mode")
	fmt.Fprintln(&b)
	b.WriteString(rep.ModeTable())
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Per-case grid")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "✅ mean 2 · \U0001F7E1 mean 1 · ❌ mean 0 · · ungraded")
	fmt.Fprintln(&b)
	b.WriteString(rep.CaseGrid())
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Prompt templates (same provider set)")
	fmt.Fprintln(&b)
	tmplTable, common := rep.TemplateTable()
	if rep.MultiContext() {
		if len(common) > 0 {
			fmt.Fprintf(&b, "Providers common to every template within a given context: %s\n\n", strings.Join(common, ", "))
		} else {
			fmt.Fprintln(&b, "No provider ran every template within any single context; table below is empty per template.")
			fmt.Fprintln(&b)
		}
	} else if len(common) > 0 {
		fmt.Fprintf(&b, "Providers common to every template: %s\n\n", strings.Join(common, ", "))
	} else {
		fmt.Fprintln(&b, "No provider ran every template; table below is empty per template.")
		fmt.Fprintln(&b)
	}
	b.WriteString(tmplTable)

	return b.String()
}

const (
	readmeStart = "<!-- bench:start -->"
	readmeEnd   = "<!-- bench:end -->"
)

// UpdateReadme replaces the text between the bench:start/bench:end markers
// in readmePath with table (a) plus a one-line footnote. It errors if the
// markers are missing so a silent no-op never hides a broken --readme flag.
func UpdateReadme(readmePath string, rep Report, now time.Time) error {
	b, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}
	content := string(b)
	si := strings.Index(content, readmeStart)
	ei := strings.Index(content, readmeEnd)
	if si == -1 || ei == -1 || ei < si {
		return fmt.Errorf("%s: markers %s / %s not found; add them to enable --readme", readmePath, readmeStart, readmeEnd)
	}
	var mid strings.Builder
	fmt.Fprintln(&mid)
	mid.WriteString(rep.SummaryTable())
	fmt.Fprintf(&mid, "\n_%s · %d cases · up to %d sample(s) per trial_\n", now.UTC().Format("2006-01-02"), rep.CaseCount, rep.MaxSample)
	newContent := content[:si+len(readmeStart)] + mid.String() + content[ei:]
	return os.WriteFile(readmePath, []byte(newContent), 0o644)
}

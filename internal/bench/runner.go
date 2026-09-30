package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ericandrechek/shelp/internal/clean"
	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
)

// providerLimiter caps in-flight calls per provider ID, while letting
// different providers run fully in parallel. It also tracks which provider
// has already had its first call dispatched, for the "cold" flag.
type providerLimiter struct {
	mu   sync.Mutex
	sems map[string]chan struct{}
	cold map[string]bool
	n    int
}

func newProviderLimiter(n int) *providerLimiter {
	if n < 1 {
		n = 1
	}
	return &providerLimiter{sems: map[string]chan struct{}{}, cold: map[string]bool{}, n: n}
}

// acquire blocks until a slot for id is free and reports whether this call
// is the first ever dispatched for id in this run.
func (l *providerLimiter) acquire(id string) (release func(), isCold bool) {
	l.mu.Lock()
	ch, ok := l.sems[id]
	if !ok {
		ch = make(chan struct{}, l.n)
		l.sems[id] = ch
	}
	isCold = !l.cold[id]
	l.cold[id] = true
	l.mu.Unlock()
	ch <- struct{}{}
	return func() { <-ch }, isCold
}

// Progress is reported periodically as trials complete.
type Progress struct {
	Done, Total int
	LastCase    string
	LastErr     error
}

// RunConfig configures Run.
type RunConfig struct {
	Trials        []Trial
	ContextFields []string
	OutDir        string
	Concurrency   int // per-provider concurrency limit
	HostOSKey     string
	Resume        bool
	OnProgress    func(Progress)
}

// Run executes every trial in cfg.Trials, mirroring exactly what the shelp
// CLI does per call (see cmd/shelp/main.go: suggest -> no OnChunk, clean
// with clean.Command; explain -> a no-op OnChunk since the CLI streams
// explain, clean with clean.Text), streaming results to disk as they
// complete. Already-completed trials (by Trial.Key()) are skipped when
// cfg.Resume is set.
func Run(ctx context.Context, cfg RunConfig) error {
	done := map[string]bool{}
	if cfg.Resume {
		existing, err := LoadResults(cfg.OutDir)
		if err != nil {
			return err
		}
		for _, r := range existing {
			done[r.TrialKey()] = true
		}
	}
	w, err := OpenResultWriter(cfg.OutDir)
	if err != nil {
		return err
	}
	defer w.Close()

	var pending []Trial
	for _, t := range cfg.Trials {
		if done[t.Key()] {
			continue
		}
		pending = append(pending, t)
	}

	lim := newProviderLimiter(cfg.Concurrency)
	var writeMu sync.Mutex
	var wg sync.WaitGroup
	var doneCount int
	total := len(cfg.Trials)
	doneCount = total - len(pending)
	var progMu sync.Mutex
	report := func(caseID string, err error) {
		progMu.Lock()
		doneCount++
		d, tt := doneCount, total
		progMu.Unlock()
		if cfg.OnProgress != nil {
			cfg.OnProgress(Progress{Done: d, Total: tt, LastCase: caseID, LastErr: err})
		}
	}

	for _, t := range pending {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, cold := lim.acquire(t.Provider.ID())
			defer release()
			res, rerr := runOne(ctx, t, cfg.ContextFields, cfg.HostOSKey, cold)
			writeMu.Lock()
			werr := w.Write(res)
			writeMu.Unlock()
			if werr != nil && rerr == nil {
				rerr = werr
			}
			report(t.Case.ID, rerr)
		}()
	}
	wg.Wait()
	return nil
}

// runOne executes exactly one trial and produces its TrialResult. It never
// returns a non-nil error for a model failure (that's recorded in the
// result); the error return is reserved for a fatal setup problem.
func runOne(ctx context.Context, t Trial, contextFields []string, hostOSKey string, cold bool) (TrialResult, error) {
	c, err := SyntheticContext(t.OS, t.Shell, contextFields)
	if err != nil {
		return TrialResult{}, err
	}
	mode := provider.Suggest
	if t.Case.Mode == "explain" {
		mode = provider.Explain
	}
	req := prompt.Render(t.Template, mode, t.Case.Input, c)
	if mode == provider.Explain {
		// The CLI streams explain output straight to the terminal; give the
		// same non-nil OnChunk here (a no-op) so provider.Call measures
		// FirstChunk the same way, without printing anything.
		req.OnChunk = func(string) {}
	}
	started := time.Now().UTC()
	res, callErr := provider.Call(ctx, t.Provider, req)

	out := TrialResult{
		Case: t.Case.ID, Mode: t.Case.Mode, Shell: t.Shell, OS: t.OS,
		Provider: t.Provider.ID(), Template: t.Template.Name,
		Context: FromPromptContext(c), Sample: t.Sample,
		LatencyMS: res.Latency.Milliseconds(), FirstChunkMS: res.FirstChunk.Milliseconds(),
		Cold: cold, StartedAt: started,
	}
	if callErr != nil {
		out.Error = callErr.Error()
		var ue *provider.UnavailableError
		out.Unavailable = errors.As(callErr, &ue)
		out.GradeKey = GradeKey(t.Case.ID, t.Shell, t.OS, t.Case.Mode, "")
		return out, nil
	}
	out.Raw = res.Text
	if t.Case.Mode == "explain" {
		out.Output = clean.Text(res.Text)
	} else {
		out.Output = clean.Command(res.Text)
	}
	out.Checks = RunChecks(ctx, t.Case.Mode, t.Shell, t.OS, hostOSKey, out.Raw, out.Output)
	out.GradeKey = GradeKey(t.Case.ID, t.Shell, t.OS, t.Case.Mode, out.Output)
	return out, nil
}

// FormatProgress renders a single-line progress string for -v/live output.
func FormatProgress(p Progress) string {
	if p.LastErr != nil {
		return fmt.Sprintf("\r%d/%d  last: %s (%v)          ", p.Done, p.Total, p.LastCase, p.LastErr)
	}
	return fmt.Sprintf("\r%d/%d  last: %s          ", p.Done, p.Total, p.LastCase)
}

// WriteProgress writes a progress line to w, overwriting the previous one
// (assumes w is a terminal; callers should skip this for non-tty output).
func WriteProgress(w io.Writer, p Progress) {
	fmt.Fprint(w, FormatProgress(p))
}

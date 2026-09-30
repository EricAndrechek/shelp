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
// has already had its first call dispatched, for the "cold" flag, and
// (when minInterval > 0) paces consecutive calls to the SAME provider at
// least minInterval apart -- e.g. to stay clear of a backend's observed
// rate limit (see CLAUDE.md's Apple Intelligence Pro notes).
type providerLimiter struct {
	mu          sync.Mutex
	sems        map[string]chan struct{}
	cold        map[string]bool
	lastCall    map[string]time.Time
	n           int
	minInterval time.Duration
}

func newProviderLimiter(n int, minInterval time.Duration) *providerLimiter {
	if n < 1 {
		n = 1
	}
	return &providerLimiter{
		sems: map[string]chan struct{}{}, cold: map[string]bool{}, lastCall: map[string]time.Time{},
		n: n, minInterval: minInterval,
	}
}

// acquire blocks until a slot for id is free (and, if minInterval > 0,
// until at least minInterval has passed since the last call this limiter
// dispatched for id) and reports whether this call is the first ever
// dispatched for id in this run. ctx cancellation cuts the pacing wait
// short so a run can still stop promptly.
func (l *providerLimiter) acquire(ctx context.Context, id string) (release func(), isCold bool) {
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

	if l.minInterval > 0 {
		l.mu.Lock()
		last, seen := l.lastCall[id]
		l.mu.Unlock()
		if seen {
			if wait := l.minInterval - time.Since(last); wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
				}
			}
		}
		l.mu.Lock()
		l.lastCall[id] = time.Now()
		l.mu.Unlock()
	}
	return func() { <-ch }, isCold
}

// BreakerTrip records one provider tripping the rate-limit circuit
// breaker: from the moment its first *provider.RateLimitedError is seen,
// Run stops dispatching any of its remaining trials for the rest of the
// run (they're left for a later `--resume`, never written to
// results.jsonl).
type BreakerTrip struct {
	Provider string    `json:"provider"`
	Reason   string    `json:"reason"`
	AfterN   int       `json:"after_calls"`    // calls made to this provider, including the one that tripped it
	Skipped  int       `json:"skipped_trials"` // this provider's remaining trials that were never dispatched
	At       time.Time `json:"at"`
}

// breaker tracks, per provider, whether it has tripped -- rate-limited
// once already this run -- so every other in-flight or not-yet-started
// trial for that provider can be skipped without calling it again.
type breaker struct {
	mu      sync.Mutex
	tripped map[string]bool
}

func newBreaker() *breaker { return &breaker{tripped: map[string]bool{}} }

func (b *breaker) isTripped(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped[id]
}

// trip marks id tripped and reports whether THIS call is the one that did
// it (so the caller emits exactly one BreakerTrip per provider).
func (b *breaker) trip(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tripped[id] {
		return false
	}
	b.tripped[id] = true
	return true
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
	// MinInterval, when > 0, paces consecutive calls to the SAME provider
	// at least this far apart (across all trials for that provider,
	// regardless of Concurrency) -- e.g. to stay under a backend's
	// observed rate limit.
	MinInterval time.Duration
	// OnBreakerTrip, if non-nil, is called exactly once per provider the
	// first time it rate-limits during this run (see BreakerTrip).
	OnBreakerTrip func(BreakerTrip)
}

// Run executes every trial in cfg.Trials, mirroring exactly what the shelp
// CLI does per call (see cmd/shelp/main.go: suggest -> no OnChunk, clean
// with clean.Command; explain -> a no-op OnChunk since the CLI streams
// explain, clean with clean.Text), streaming results to disk as they
// complete. Already-completed trials (by Trial.Key()) are skipped when
// cfg.Resume is set.
//
// The first *provider.RateLimitedError seen from a given provider trips a
// circuit breaker for it (see breaker/BreakerTrip): every trial for that
// provider not yet dispatched is skipped for the rest of this run -- not
// run, and (unlike every other trial) never written to results.jsonl, so
// a later `Run` with cfg.Resume set treats them as still pending and fills
// them in.
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
	remaining := map[string]int{} // provider ID -> trials of pending not yet dispatched/completed
	for _, t := range cfg.Trials {
		if done[t.Key()] {
			continue
		}
		pending = append(pending, t)
		remaining[t.Provider.ID()]++
	}

	lim := newProviderLimiter(cfg.Concurrency, cfg.MinInterval)
	brk := newBreaker()
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

	var callCountMu sync.Mutex
	callCount := map[string]int{}
	var remMu sync.Mutex // guards remaining

	for _, t := range pending {
		t := t
		pid := t.Provider.ID()
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Fast path: don't even wait for a concurrency slot once this
			// provider's breaker has already tripped.
			if brk.isTripped(pid) {
				remMu.Lock()
				remaining[pid]--
				remMu.Unlock()
				return
			}
			release, cold := lim.acquire(ctx, pid)
			defer release()
			// Re-check: another goroutine for the same provider may have
			// tripped the breaker while this one waited for a slot.
			if brk.isTripped(pid) {
				remMu.Lock()
				remaining[pid]--
				remMu.Unlock()
				return
			}

			callCountMu.Lock()
			callCount[pid]++
			n := callCount[pid]
			callCountMu.Unlock()

			res, rerr := runOne(ctx, t, cfg.ContextFields, cfg.HostOSKey, cold)
			writeMu.Lock()
			werr := w.Write(res)
			writeMu.Unlock()
			if werr != nil && rerr == nil {
				rerr = werr
			}

			remMu.Lock()
			remaining[pid]--
			skipped := remaining[pid]
			remMu.Unlock()

			if res.RateLimited && brk.trip(pid) && cfg.OnBreakerTrip != nil {
				if skipped < 0 {
					skipped = 0
				}
				cfg.OnBreakerTrip(BreakerTrip{
					Provider: pid, Reason: res.RateLimitReason, AfterN: n, Skipped: skipped, At: time.Now().UTC(),
				})
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
		var rle *provider.RateLimitedError
		if errors.As(callErr, &rle) {
			out.RateLimited = true
			out.RateLimitReason = rle.Reason
		}
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

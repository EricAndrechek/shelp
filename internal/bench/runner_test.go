package bench

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
)

func testTrials(t *testing.T) []Trial {
	t.Helper()
	c := Case{ID: "c1", Mode: "suggest", Input: "list files", Shells: []string{"zsh"}, OS: []string{"macos"}}
	tmpl, err := prompt.Get(prompt.Default)
	if err != nil {
		t.Fatal(err)
	}
	ps := []provider.Provider{
		&fakeProvider{id: "ok", text: "ls -la"},
		&fakeProvider{id: "down", unavailable: true},
	}
	trials, err := Expand([]Case{c}, ps, []prompt.Template{tmpl}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return trials
}

func TestRunWritesResultsAndMarksColdAndUnavailable(t *testing.T) {
	dir := t.TempDir()
	trials := testTrials(t)
	err := Run(context.Background(), RunConfig{
		Trials: trials, ContextFields: []string{"os", "shell"}, OutDir: dir,
		Concurrency: 1, HostOSKey: "macos",
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	byProvider := map[string]TrialResult{}
	for _, r := range results {
		byProvider[r.Provider] = r
	}
	ok, down := byProvider["ok"], byProvider["down"]
	if !ok.Cold {
		t.Error("first (only) call to provider 'ok' should be cold")
	}
	if ok.Unavailable || ok.Error != "" {
		t.Errorf("provider 'ok' should succeed, got %+v", ok)
	}
	if ok.Output != "ls -la" {
		t.Errorf("ok.Output = %q, want %q", ok.Output, "ls -la")
	}
	if !down.Unavailable {
		t.Error("provider 'down' should be recorded unavailable")
	}
	if !down.Cold {
		t.Error("first (only) call to provider 'down' should be cold")
	}
}

func TestRunResumeSkipsCompletedTrials(t *testing.T) {
	dir := t.TempDir()
	trials := testTrials(t)
	ctx := context.Background()
	cfg := RunConfig{
		Trials: trials, ContextFields: []string{"os", "shell"}, OutDir: dir,
		Concurrency: 1, HostOSKey: "macos",
	}
	if err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	first, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("got %d results after first run, want 2", len(first))
	}

	// Re-run the SAME trial set with Resume=true: nothing new should be
	// appended, since every trial key is already present.
	cfg.Resume = true
	if err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	second, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 {
		t.Fatalf("resume appended extra trials: got %d results, want 2", len(second))
	}
}

func TestRunResumeRunsOnlyNewTrials(t *testing.T) {
	dir := t.TempDir()
	tmpl, _ := prompt.Get(prompt.Default)
	c := Case{ID: "c1", Mode: "suggest", Input: "x", Shells: []string{"zsh"}, OS: []string{"macos"}}
	p1 := &fakeProvider{id: "p1", text: "cmd"}
	trials1, _ := Expand([]Case{c}, []provider.Provider{p1}, []prompt.Template{tmpl}, 1)
	ctx := context.Background()
	cfg := RunConfig{Trials: trials1, ContextFields: []string{"os"}, OutDir: dir, Concurrency: 1}
	if err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	p2 := &fakeProvider{id: "p2", text: "cmd2"}
	trials2, _ := Expand([]Case{c}, []provider.Provider{p1, p2}, []prompt.Template{tmpl}, 1)
	cfg.Trials = trials2
	cfg.Resume = true
	if err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	results, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (p1 from before, p2 new)", len(results))
	}
}

// TestRunBreakerStopsDispatchAfterRateLimitAndResumeFillsRest is the
// incident scenario from CLAUDE.md's "Rate limits and cooldowns" section,
// reproduced with a fake provider family instead of the real Shortcuts
// backend: a provider that rate-limits after a fixed number of calls must
// trip the breaker exactly once, leave its remaining trials undispatched
// AND unwritten (so --resume later fills them), and report the trip via
// OnBreakerTrip with an accurate after/skipped count. Concurrency is 1
// (the default, and the safe setting for a real rate-limited backend), so
// dispatch is fully serialized and the counts are exact, not approximate.
func TestRunBreakerStopsDispatchAfterRateLimitAndResumeFillsRest(t *testing.T) {
	dir := t.TempDir()
	tmpl, err := prompt.Get(prompt.Default)
	if err != nil {
		t.Fatal(err)
	}
	c := Case{ID: "c1", Mode: "suggest", Input: "list files", Shells: []string{"zsh"}, OS: []string{"macos"}}

	const samples = 10
	const rateLimitAfter = 2 // 3rd call trips the breaker
	limited := &fakeProvider{id: "limited", text: "ls -la", rateLimitAfter: rateLimitAfter}
	trials, err := Expand([]Case{c}, []provider.Provider{limited}, []prompt.Template{tmpl}, samples)
	if err != nil {
		t.Fatal(err)
	}

	var tripMu sync.Mutex
	var trips []BreakerTrip
	err = Run(context.Background(), RunConfig{
		Trials: trials, ContextFields: []string{"os", "shell"}, OutDir: dir,
		Concurrency: 1, HostOSKey: "macos",
		OnBreakerTrip: func(bt BreakerTrip) {
			tripMu.Lock()
			trips = append(trips, bt)
			tripMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	results, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly rateLimitAfter+1 calls actually ran (2 successes then the
	// 3rd that trips the breaker); the other 7 trials must never have been
	// dispatched or written, so a later --resume can fill them in.
	wantRan := rateLimitAfter + 1
	if len(results) != wantRan {
		t.Fatalf("got %d results, want %d (the rest must be left for --resume, not written)", len(results), wantRan)
	}
	rlCount := 0
	for _, r := range results {
		if r.RateLimited {
			rlCount++
			// RateLimitReason itself is deliberately json:"-" (never
			// persisted); the general `error` string is what round-trips
			// through results.jsonl and must still be non-empty.
			if r.Error == "" {
				t.Error("rate-limited result should not have an empty error string")
			}
		}
	}
	if rlCount != 1 {
		t.Fatalf("results with rate_limited=true = %d, want 1", rlCount)
	}

	if len(trips) != 1 {
		t.Fatalf("OnBreakerTrip called %d times, want exactly 1: %+v", len(trips), trips)
	}
	trip := trips[0]
	if trip.Provider != "limited" {
		t.Errorf("trip.Provider = %q, want limited", trip.Provider)
	}
	if trip.AfterN != wantRan {
		t.Errorf("trip.AfterN = %d, want %d", trip.AfterN, wantRan)
	}
	wantSkipped := samples - wantRan
	if trip.Skipped != wantSkipped {
		t.Errorf("trip.Skipped = %d, want %d", trip.Skipped, wantSkipped)
	}
	if trip.Reason == "" {
		t.Error("trip.Reason should not be empty")
	}

	// --resume with a FRESH provider (same ID, no rate limiting this time,
	// as if the real backend's cooldown had passed) must fill in exactly
	// the trials that were skipped, bringing the total up to `samples`.
	fresh := &fakeProvider{id: "limited", text: "ls -la"}
	trials2, err := Expand([]Case{c}, []provider.Provider{fresh}, []prompt.Template{tmpl}, samples)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), RunConfig{
		Trials: trials2, ContextFields: []string{"os", "shell"}, OutDir: dir,
		Concurrency: 1, HostOSKey: "macos", Resume: true,
	})
	if err != nil {
		t.Fatalf("Run (resume): %v", err)
	}
	results2, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results2) != samples {
		t.Fatalf("after --resume, got %d results, want %d (every trial eventually filled in)", len(results2), samples)
	}
	seenSamples := map[int]bool{}
	for _, r := range results2 {
		seenSamples[r.Sample] = true
	}
	if len(seenSamples) != samples {
		t.Fatalf("after --resume, saw %d distinct samples, want %d: %v", len(seenSamples), samples, seenSamples)
	}
}

// TestRunMinIntervalPacesConsecutiveCallsToSameProvider verifies
// RunConfig.MinInterval spaces out consecutive calls to the SAME provider,
// which is the pacing knob CLAUDE.md recommends staying under Apple
// Intelligence Pro's observed rate with. Different providers are
// unaffected by another provider's pacing.
func TestRunMinIntervalPacesConsecutiveCallsToSameProvider(t *testing.T) {
	dir := t.TempDir()
	tmpl, err := prompt.Get(prompt.Default)
	if err != nil {
		t.Fatal(err)
	}
	c := Case{ID: "c1", Mode: "suggest", Input: "x", Shells: []string{"zsh"}, OS: []string{"macos"}}
	p := &fakeProvider{id: "paced", text: "ok"}
	const n = 3
	const minInterval = 60 * time.Millisecond
	trials, err := Expand([]Case{c}, []provider.Provider{p}, []prompt.Template{tmpl}, n)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = Run(context.Background(), RunConfig{
		Trials: trials, ContextFields: []string{"os", "shell"}, OutDir: dir,
		Concurrency: 1, HostOSKey: "macos", MinInterval: minInterval,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	// n calls to the same provider, each at least minInterval after the
	// previous, take at least (n-1)*minInterval in total.
	want := time.Duration(n-1) * minInterval
	if elapsed < want {
		t.Errorf("elapsed = %v, want at least %v ((n-1)*minInterval)", elapsed, want)
	}
	results, err := LoadResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
}

package bench

import (
	"context"
	"testing"

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

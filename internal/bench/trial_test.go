package bench

import (
	"testing"

	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
)

func TestExpand(t *testing.T) {
	caseA := Case{ID: "a", Mode: "suggest", Input: "x", Shells: []string{"zsh", "bash"}, OS: []string{"macos"}}
	caseB := Case{ID: "b", Mode: "suggest", Input: "y", Shells: []string{"zsh"}, OS: []string{"macos", "linux"}}
	p1 := &fakeProvider{id: "p1", text: "cmd1"}
	p2 := &fakeProvider{id: "p2", text: "cmd2"}
	tmpl, err := prompt.Get(prompt.Default)
	if err != nil {
		t.Fatal(err)
	}

	trials, err := Expand([]Case{caseA, caseB}, []provider.Provider{p1, p2}, []prompt.Template{tmpl}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// caseA: 2 shells x 1 os = 2 combos; caseB: 1 shell x 2 os = 2 combos.
	// total combos = 4, x 2 providers x 1 template x 2 samples = 16.
	if want := 16; len(trials) != want {
		t.Fatalf("Expand: got %d trials, want %d", len(trials), want)
	}
	keys := map[string]bool{}
	for _, tr := range trials {
		if keys[tr.Key()] {
			t.Fatalf("duplicate trial key %q", tr.Key())
		}
		keys[tr.Key()] = true
		if tr.Sample < 1 || tr.Sample > 2 {
			t.Fatalf("sample out of range: %d", tr.Sample)
		}
	}
	if len(keys) != 16 {
		t.Fatalf("got %d unique keys, want 16", len(keys))
	}
}

func TestExpandRequiresProvidersAndTemplates(t *testing.T) {
	c := Case{ID: "a", Mode: "suggest", Input: "x", Shells: []string{"zsh"}, OS: []string{"macos"}}
	tmpl, _ := prompt.Get(prompt.Default)
	p := &fakeProvider{id: "p1", text: "cmd"}

	if _, err := Expand([]Case{c}, nil, []prompt.Template{tmpl}, 1); err == nil {
		t.Fatal("Expand with no providers: want error, got nil")
	}
	if _, err := Expand([]Case{c}, []provider.Provider{p}, nil, 1); err == nil {
		t.Fatal("Expand with no templates: want error, got nil")
	}
}

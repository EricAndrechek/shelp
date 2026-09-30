package bench

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ericandrechek/shelp/internal/prompt"
	"github.com/ericandrechek/shelp/internal/provider"
)

// Trial is one point in the cases x shells x os x providers x templates x
// samples grid.
type Trial struct {
	Case     Case
	Shell    string
	OS       string // case.OS entry: "macos" | "linux"
	Provider provider.Provider
	Template prompt.Template
	Sample   int // 1-based
}

// Key uniquely identifies a trial for resume/idempotency purposes.
// Everything that changes the request or the environment is included;
// nothing that's purely derived (context fields) needs to be.
func (t Trial) Key() string {
	return strings.Join([]string{
		t.Case.ID, t.Shell, t.OS, t.Provider.ID(), t.Template.Name, strconv.Itoa(t.Sample),
	}, "|")
}

// Expand builds the full trial grid: cases x each case's shells x each
// case's os x providers x templates x samples.
func Expand(cases []Case, providers []provider.Provider, templates []prompt.Template, samples int) ([]Trial, error) {
	if samples < 1 {
		samples = 1
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("bench: no providers selected")
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("bench: no templates selected")
	}
	var out []Trial
	for _, c := range cases {
		for _, shell := range c.Shells {
			for _, osKey := range c.OS {
				for _, p := range providers {
					for _, tmpl := range templates {
						for s := 1; s <= samples; s++ {
							out = append(out, Trial{
								Case: c, Shell: shell, OS: osKey,
								Provider: p, Template: tmpl, Sample: s,
							})
						}
					}
				}
			}
		}
	}
	return out, nil
}

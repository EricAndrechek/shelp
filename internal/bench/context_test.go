package bench

import (
	"reflect"
	"testing"

	"github.com/ericandrechek/shelp/internal/prompt"
)

func TestSyntheticContext(t *testing.T) {
	cases := []struct {
		name   string
		os     string
		shell  string
		fields []string
		want   prompt.Context
	}{
		{
			name: "macos os+shell", os: "macos", shell: "zsh", fields: []string{"os", "shell"},
			want: prompt.Context{OS: "macOS 27.0", Userland: "BSD", Shell: "zsh"},
		},
		{
			name: "linux os+shell", os: "linux", shell: "bash", fields: []string{"os", "shell"},
			want: prompt.Context{OS: "Ubuntu 24.04", Userland: "GNU", Shell: "bash"},
		},
		{
			name: "no fields", os: "macos", shell: "zsh", fields: nil,
			want: prompt.Context{},
		},
		{
			name: "cwd and tools", os: "macos", shell: "zsh", fields: []string{"cwd", "tools"},
			want: prompt.Context{Cwd: "/Users/bench/project", Tools: osFixtures["macos"].Tools},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SyntheticContext(c.os, c.shell, c.fields)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SyntheticContext(%q,%q,%v) = %+v, want %+v", c.os, c.shell, c.fields, got, c.want)
			}
		})
	}
}

func TestSyntheticContextUnknownOS(t *testing.T) {
	if _, err := SyntheticContext("windows", "zsh", []string{"os"}); err == nil {
		t.Fatal("SyntheticContext with unknown os: want error, got nil")
	}
}

// SyntheticContext must never consult the host: two calls under the same
// case os must be identical regardless of what machine runs the test.
func TestSyntheticContextIsHostIndependent(t *testing.T) {
	a, _ := SyntheticContext("macos", "fish", prompt.AllFields)
	b, _ := SyntheticContext("macos", "fish", prompt.AllFields)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("SyntheticContext not deterministic: %+v != %+v", a, b)
	}
}

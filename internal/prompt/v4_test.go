package prompt

import (
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/provider"
)

func TestV4IsDefault(t *testing.T) {
	if Default != "v4" {
		t.Fatalf("Default = %q, want v4", Default)
	}
	if _, err := Get(""); err != nil {
		t.Fatal("default template not registered")
	}
}

func TestV4SuggestEqualsV2(t *testing.T) {
	v2, _ := Get("v2")
	v4, _ := Get("v4")
	for _, c := range []Context{
		{}, {OS: "macOS 27.0", Userland: "BSD", Shell: "zsh"}, {OS: "Ubuntu", Userland: "GNU", Shell: "fish", Cwd: "/x", Tools: []string{"jq"}},
	} {
		if a, b := v4.System(provider.Suggest, c), v2.System(provider.Suggest, c); a != b {
			t.Errorf("suggest system differs:\n%q\n%q", a, b)
		}
		if a, b := v4.User(provider.Suggest, "do it", c), v2.User(provider.Suggest, "do it", c); a != b {
			t.Errorf("suggest user differs")
		}
	}
}

func TestV4ExplainAsksForMarkdown(t *testing.T) {
	v4, _ := Get("v4")
	c := Context{OS: "macOS 27.0", Shell: "zsh", Cwd: "/tmp"}
	s := v4.System(provider.Explain, c)
	for _, want := range []string{"zsh", "macOS 27.0", "25 words", "12 words", "- `-h` — human-readable sizes", "**Warning:**", "No headings", "no code fences", "no URLs", "/tmp"} {
		if !strings.Contains(s, want) {
			t.Errorf("explain prompt missing %q:\n%s", want, s)
		}
	}
	if got := v4.User(provider.Explain, "du -sh", c); got != "Command: du -sh" {
		t.Errorf("user = %q", got)
	}
	v2, _ := Get("v2")
	if v2.System(provider.Explain, c) == s {
		t.Error("v4 explain should differ from v2")
	}
}

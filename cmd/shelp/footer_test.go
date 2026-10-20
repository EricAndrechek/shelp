package main

import (
	"strings"
	"testing"
)

func TestExplainNoTokensNoFooterTokens(t *testing.T) {
	fakeBackend(t, "ls", "")
	t.Setenv("FAKE_NO_USAGE", "1")
	setColorEnv(t, "1")
	out, _, _ := runOut(t, "explain", "ls")
	if strings.Contains(out, "tokens") {
		t.Errorf("footer %q", out)
	}
}

func TestExplainTerminalFooter(t *testing.T) {
	fakeBackend(t, "Lists files.", "")
	setColorEnv(t, "1")
	out, _, err := runOut(t, "explain", "ls -la")
	if err != nil {
		t.Fatal(err)
	}
	plain := strip(out)
	if !strings.Contains(plain, "  Lists files.\n\n  man: ls\n  fake:m · ") {
		t.Fatalf("footer layout:\n%q", plain)
	}
	if !strings.HasSuffix(plain, " · 15 tokens\n") || strings.HasSuffix(plain, "\n\n") {
		t.Errorf("tail %q", plain)
	}
	// no man links: just the stats line after the blank
	out, _, _ = runOut(t, "explain", "FOO=1")
	plain = strip(out)
	if !strings.Contains(plain, "\n\n  fake:m · ") || strings.Contains(plain, "man:") || strings.HasSuffix(plain, "\n\n") {
		t.Errorf("no-links footer %q", plain)
	}
	// off a terminal: the model's text and nothing else
	setColorEnv(t, "")
	out, _, _ = runOut(t, "explain", "ls -la")
	if out != "Lists files.\n" {
		t.Errorf("non-terminal explain %q", out)
	}
}

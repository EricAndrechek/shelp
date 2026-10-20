package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONAndVerboseTokens(t *testing.T) {
	fakeBackend(t, "ls", "")
	out, _, err := runOut(t, "--json", "list", "files")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Text   string
		Tokens struct{ Input, Output int }
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Tokens.Input != 11 || got.Tokens.Output != 4 || got.Text != "ls" {
		t.Errorf("json %q: %+v %v", out, got, err)
	}
	_, errOut, _ := runOut(t, "-v", "list", "files")
	if !strings.Contains(errOut, "fake:m in") || !strings.Contains(errOut, "15 tokens (11 in / 4 out)") {
		t.Errorf("-v stderr %q", errOut)
	}
}

func TestUnknownTokensOmitted(t *testing.T) {
	fakeBackend(t, "ls", "")
	t.Setenv("FAKE_NO_USAGE", "1")
	out, _, err := runOut(t, "--json", "list", "files")
	if err != nil || strings.Contains(out, "tokens") {
		t.Errorf("json %q %v", out, err)
	}
	if r := readHistory(t); r[0].Tokens != nil {
		t.Errorf("tokens recorded: %+v", r[0].Tokens)
	}
	_, errOut, _ := runOut(t, "-v", "list", "files")
	if strings.Contains(errOut, "tokens") {
		t.Errorf("-v %q", errOut)
	}
}

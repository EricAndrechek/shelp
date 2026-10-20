package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSuggestSameAsInputHint(t *testing.T) {
	fakeBackend(t, "find . -type f", "")
	out, errOut, err := runOut(t, "find", ".", "-type", "f")
	if err != nil || out != "find . -type f\n" {
		t.Fatalf("stdout %q, %v", out, err)
	}
	if errOut != "shelp: that's already a command; to explain it: shelp explain 'find . -type f'\n" {
		t.Errorf("stderr %q", errOut)
	}
	// whitespace differences still count as the same
	out, errOut, _ = runOut(t, "find  .", "-type   f")
	if out != "find . -type f\n" || !strings.Contains(errOut, "already a command") {
		t.Errorf("collapsed whitespace: %q / %q", out, errOut)
	}
	// quotes in the hint are escaped for pasting
	fakeBackend(t, "echo 'hi'", "")
	_, errOut, _ = runOut(t, "echo", "'hi'")
	if !strings.Contains(errOut, `shelp explain 'echo '\''hi'\'''`) {
		t.Errorf("stderr %q", errOut)
	}
	// a different command: no hint
	fakeBackend(t, "ls -la", "")
	if _, errOut, _ = runOut(t, "list", "files"); errOut != "" {
		t.Errorf("unexpected stderr %q", errOut)
	}
	// --json: no hint, stdout stays one JSON object
	fakeBackend(t, "ls", "")
	out, errOut, _ = runOut(t, "--json", "ls")
	if errOut != "" || !json.Valid([]byte(out)) {
		t.Errorf("json: %q / %q", out, errOut)
	}
}

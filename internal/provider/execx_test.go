package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackendGate(t *testing.T) {
	t.Setenv(NoBackendsEnv, "1")

	// Real system paths of the guarded backends are refused, by name or path.
	for _, name := range []string{"/usr/bin/shortcuts", "/usr/bin/fm", "/usr/local/bin/claude", "/usr/bin/osascript", "/usr/bin/open"} {
		if !backendRefused(name) {
			t.Errorf("backendRefused(%q) = false", name)
		}
		_, err := RunCmd(context.Background(), nil, nil, name, "x")
		var ue *UnavailableError
		if !errors.As(err, &ue) {
			t.Errorf("RunCmd(%q) err = %v, want Unavailable", name, err)
		}
	}
	// Other tools are not guarded.
	if backendRefused("/usr/bin/true") || backendRefused("plutil") {
		t.Error("gate refused a non-backend")
	}

	// A fake in a temp dir (or testdata) is allowed, by path and via PATH.
	dir := t.TempDir()
	fake := filepath.Join(dir, "shortcuts")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if backendRefused(fake) {
		t.Error("temp-dir fake refused")
	}
	t.Setenv("PATH", dir)
	if p, ok := LookPath("shortcuts"); !ok || p != fake {
		t.Errorf("LookPath(shortcuts) = %q, %v; want the fake", p, ok)
	}
	res, err := RunCmd(context.Background(), nil, nil, "shortcuts")
	if err != nil || res.Stdout != "fake\n" {
		t.Errorf("RunCmd fake = %q, %v", res.Stdout, err)
	}
	if backendRefused(filepath.Join("testdata", "agentcli", "claude")) {
		t.Error("testdata fake refused")
	}

	// Gate off: nothing is refused.
	t.Setenv(NoBackendsEnv, "")
	if backendRefused("/usr/bin/shortcuts") {
		t.Error("gate refused with the env unset")
	}
}

func TestBackendGateLookPathRefusesRealOnes(t *testing.T) {
	t.Setenv(NoBackendsEnv, "1")
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, n := range []string{"shortcuts", "fm", "osascript"} {
		if p, ok := LookPath(n); ok {
			t.Errorf("LookPath(%s) = %q, want refused", n, p)
		}
	}
}

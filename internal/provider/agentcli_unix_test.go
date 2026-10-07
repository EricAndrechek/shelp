//go:build unix

package provider

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// ---------------- iTerm2 isolation ----------------

func traceLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestClaude_OwnProcessGroupAndScrubbedEnv(t *testing.T) {
	fakePATH(t)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("SHELP_FAKE_TRACE", trace)
	t.Setenv("SHELP_FAKE_VERSION", "2.1.0 (Claude Code)")
	t.Setenv("SHELP_FAKE_STDOUT", "ok")
	t.Setenv("ITERM_SESSION_ID", "w0t0p0:ABC")
	t.Setenv("TERM_SESSION_ID", "w0t0p0:ABC")
	t.Setenv("LC_TERMINAL", "iTerm2")

	p := newProvider(claudeSpec, "haiku")
	if st := p.Detect(context.Background()); !st.Available {
		t.Fatalf("Detect: %q", st.Reason)
	}
	if _, err := p.Generate(context.Background(), agentTestReq()); err != nil {
		t.Fatal(err)
	}

	lines := traceLines(t, trace)
	if len(lines) != 3 { // --version, auth status, generate
		t.Fatalf("want 3 invocations, got %d: %q", len(lines), lines)
	}
	self := strconv.Itoa(syscall.Getpgrp())
	for _, l := range lines {
		if strings.Contains(l, "PGID:"+self+" ") {
			t.Errorf("child shares the test's process group %s: %s", self, l)
		}
		for _, bad := range []string{"w0t0p0", "LCT:iTerm2"} {
			if strings.Contains(l, bad) {
				t.Errorf("iTerm2 identity leaked (%s): %s", bad, l)
			}
		}
		if !strings.Contains(l, "ITERM:<unset>") || !strings.Contains(l, "TERMSESS:<unset>") {
			t.Errorf("session vars not unset: %s", l)
		}
	}
}

func TestClaude_IdentifyOncePerProcess(t *testing.T) {
	fakePATH(t)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("SHELP_FAKE_TRACE", trace)
	t.Setenv("SHELP_FAKE_VERSION", "2.1.0 (Claude Code)")

	ps := agentCLIFamily{}.Providers(&config.Config{})
	if len(ps) < 2 {
		t.Fatalf("want >=2 claude providers, got %d", len(ps))
	}
	for _, p := range ps {
		if st := p.Detect(context.Background()); !st.Available {
			t.Fatalf("%s: %q", p.ID(), st.Reason)
		}
	}
	if n := len(traceLines(t, trace)); n != 2 { // --version + auth status, once
		t.Errorf("identify ran %d claude invocations for %d providers, want 2 total", n, len(ps))
	}
}

func TestClaude_CancelKillsWholeGroup(t *testing.T) {
	fakePATH(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("SHELP_FAKE_GRANDCHILD", pidFile)
	t.Setenv("SHELP_FAKE_SLEEP", "60")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newProvider(claudeSpec, "haiku").Generate(ctx, agentTestReq())
		done <- err
	}()

	var gpid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				gpid = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gpid == 0 {
		cancel()
		t.Fatal("fake never wrote its grandchild pid")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("Generate did not return promptly after cancel")
	}
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(gpid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("grandchild %d survived cancellation", gpid)
}

package style

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestSpinnerAnimatesAndStops(t *testing.T) {
	t.Setenv("TERM", "xterm")
	oldT, oldP := spinnerIsTerminal, spinnerPalette
	spinnerIsTerminal = func(io.Writer) bool { return true }
	spinnerPalette = func(io.Writer) Palette { return Forced(TrueColor) }
	t.Cleanup(func() { spinnerIsTerminal, spinnerPalette = oldT, oldP })

	buf := &lockedBuf{}
	s := StartSpinner(buf, "thinking…")
	if s == nil {
		t.Fatal("nil spinner on fake terminal")
	}
	time.Sleep(spinnerDelay + 4*spinnerInterval)
	s.Stop()
	out := buf.String()
	if !strings.HasPrefix(out, "\x1b[?25l") {
		t.Errorf("cursor not hidden first: %q", out)
	}
	if !strings.HasSuffix(out, "\r\x1b[K\x1b[?25h") {
		t.Errorf("Stop did not clear and restore cursor: %q", out)
	}
	plain := Strip(out)
	frames := 0
	for _, f := range spinnerFrames {
		if strings.Contains(plain, f+" thinking…") {
			frames++
		}
	}
	if frames < 3 {
		t.Errorf("only %d distinct frames drawn: %q", frames, plain)
	}
	s.Stop() // idempotent
	n := len(buf.String())
	time.Sleep(3 * spinnerInterval)
	if len(buf.String()) != n {
		t.Error("wrote after Stop")
	}
}

func TestSpinnerNoColorStillAnimates(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "1")
	oldT := spinnerIsTerminal
	spinnerIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { spinnerIsTerminal = oldT })
	buf := &lockedBuf{}
	s := StartSpinner(buf, "x")
	time.Sleep(spinnerDelay + 2*spinnerInterval)
	s.Stop()
	out := buf.String()
	if !strings.Contains(out, "⠋ x") || strings.Contains(out, "\x1b[38") || strings.Contains(out, "\x1b[2m") {
		t.Errorf("unexpected output %q", out)
	}
}

func TestSpinnerQuickStopWritesNothing(t *testing.T) {
	t.Setenv("TERM", "xterm")
	oldT := spinnerIsTerminal
	spinnerIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { spinnerIsTerminal = oldT })
	buf := &lockedBuf{}
	StartSpinner(buf, "x").Stop()
	if buf.String() != "" {
		t.Errorf("wrote %q before the delay", buf.String())
	}
}

func TestSpinnerNilAndNonTerminal(t *testing.T) {
	var s *Spinner
	s.Stop()
	s.Stop()
	var buf bytes.Buffer
	if got := StartSpinner(&buf, "x"); got != nil {
		t.Error("non-terminal writer should give nil")
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q", buf.String())
	}
	t.Setenv("TERM", "dumb")
	oldT := spinnerIsTerminal
	spinnerIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { spinnerIsTerminal = oldT })
	if StartSpinner(&buf, "x") != nil {
		t.Error("TERM=dumb should give nil")
	}
}

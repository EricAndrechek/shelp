package style

import (
	"io"
	"os"
	"sync"
	"time"
)

// Spinner animation timing. The first frame waits spinnerDelay so a
// provider that answers instantly never flashes a spinner; a Spinner
// stopped before then writes nothing at all (no cursor hide/show either).
const (
	spinnerInterval = 80 * time.Millisecond
	spinnerDelay    = 120 * time.Millisecond
	spinnerCycle    = 1600 * time.Millisecond // one BrandStart->BrandEnd->BrandStart sweep
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Test seams.
var (
	spinnerIsTerminal = IsTerminal
	spinnerPalette    = For
	spinnerNow        = time.Now
)

// IsTerminal reports whether w is a character device (a terminal).
func IsTerminal(w io.Writer) bool { return isTerminal(w) }

// Spinner is a one-line "thinking" animation on a terminal writer. A nil
// *Spinner is valid and does nothing.
type Spinner struct {
	w    io.Writer
	text string
	pal  Palette
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// StartSpinner animates `<frame> <text>` on w until Stop. It returns nil
// (and writes nothing) when w is not a terminal or TERM is "dumb". The
// frame follows the brand gradient and the text is dim; under NO_COLOR it
// still animates, uncolored. Always call Stop (it is idempotent) before
// writing anything else to w.
func StartSpinner(w io.Writer, text string) *Spinner {
	if w == nil || os.Getenv("TERM") == "dumb" || !spinnerIsTerminal(w) {
		return nil
	}
	s := &Spinner{w: w, text: text, pal: spinnerPalette(w), stop: make(chan struct{}), done: make(chan struct{})}
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.done)
	select {
	case <-s.stop:
		return
	case <-time.After(spinnerDelay):
	}
	// Show the cursor again no matter how we leave the loop.
	io.WriteString(s.w, "\x1b[?25l")
	defer io.WriteString(s.w, "\r\x1b[K\x1b[?25h")
	start := spinnerNow()
	t := time.NewTicker(spinnerInterval)
	defer t.Stop()
	for i := 0; ; i++ {
		// Ping-pong position 0..1..0 over spinnerCycle.
		ph := float64(spinnerNow().Sub(start)%spinnerCycle) / float64(spinnerCycle)
		pos := 2 * ph
		if pos > 1 {
			pos = 2 - pos
		}
		frame := s.pal.RGB(Lerp(BrandStart, BrandEnd, pos), spinnerFrames[i%len(spinnerFrames)])
		io.WriteString(s.w, "\r"+frame+" "+s.pal.Dim(s.text)+"\x1b[K")
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

// Stop halts the animation, waits for it, clears the line and restores the
// cursor. Safe to call more than once and on a nil Spinner; nothing is
// written after it returns.
func (s *Spinner) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

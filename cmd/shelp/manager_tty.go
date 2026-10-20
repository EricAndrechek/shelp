package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/ericandrechek/shelp/internal/term"
)

// ttyScreen is the real terminal: keys from in (stdin), frames to out.
type ttyScreen struct {
	in, out *os.File
	state   *term.State
	keys    chan term.Key
	stop    chan struct{}
	done    chan struct{}
	resize  chan os.Signal
	started bool
}

func newTTYScreen(in, out *os.File) *ttyScreen {
	s := &ttyScreen{in: in, out: out, keys: make(chan term.Key, 64), resize: make(chan os.Signal, 1)}
	term.NotifyResize(s.resize)
	return s
}

func (s *ttyScreen) Start() error {
	if s.started {
		return nil
	}
	st, err := term.MakeRaw(int(s.in.Fd()))
	if err != nil {
		return err
	}
	s.state, s.started = st, true
	// Alternate screen, cursor hidden, cleared.
	io.WriteString(s.out, "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J")
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func(stop, done chan struct{}) {
		defer close(done)
		term.ReadKeys(int(s.in.Fd()), stop, s.keys)
	}(s.stop, s.done)
	return nil
}

// Stop ends the key reader before restoring the terminal, so no read is
// left waiting on stdin to steal what the next prompt reads, and drops
// keys typed but not handled.
func (s *ttyScreen) Stop() {
	if !s.started {
		return
	}
	s.started = false
	close(s.stop)
	<-s.done
	// A line break first: a terminal without an alternate screen then goes
	// on below the last frame instead of after its last character.
	io.WriteString(s.out, "\r\n\x1b[?25h\x1b[?1049l")
	term.Restore(int(s.in.Fd()), s.state)
	for {
		select {
		case <-s.keys:
		default:
			return
		}
	}
}

func (s *ttyScreen) Close() { signal.Stop(s.resize) }

func (s *ttyScreen) Size() (int, int) {
	c, r := term.Size(int(s.out.Fd()))
	if c <= 0 {
		c = 80
	}
	if r <= 0 {
		r = 24
	}
	return c, r
}

// Draw repaints from the top-left corner: each line, then clear to its
// end; then clear below. OPOST is off in raw mode, hence "\r\n".
func (s *ttyScreen) Draw(frame string) {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range strings.Split(frame, "\n") {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
		b.WriteString("\x1b[K")
	}
	b.WriteString("\x1b[J")
	io.WriteString(s.out, b.String())
}

func (s *ttyScreen) Keys() <-chan term.Key     { return s.keys }
func (s *ttyScreen) Resized() <-chan os.Signal { return s.resize }

func (s *ttyScreen) WaitKey(ctx context.Context) (term.Key, error) {
	fd := int(s.in.Fd())
	st, err := term.MakeRaw(fd)
	if err != nil {
		return term.Key{}, err
	}
	defer term.Restore(fd, st)
	k, ok, err := term.ReadKey(fd, ctx.Done())
	if err == nil && !ok {
		err = ctx.Err()
	}
	return k, err
}

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ericandrechek/shelp/internal/term"
)

// fakeScreen stands in for the terminal. It feeds its script one key per
// frame, and only once no row is still "checking…", so a test's keys land
// on a settled list whatever order the probes finish in.
type fakeScreen struct {
	script   []term.Key
	waitKeys []term.Key // answers to "press any key"
	keys     chan term.Key
	resized  chan os.Signal
	frames   []string
	starts   int
	stops    int
	started  bool
	w, h     int
	onDraw   func(n int) // called before each Draw is recorded
}

func newFakeScreen(script ...term.Key) *fakeScreen {
	return &fakeScreen{script: script, keys: make(chan term.Key, len(script)+1), resized: make(chan os.Signal, 1), w: 120, h: 60}
}

func (f *fakeScreen) Start() error {
	if !f.started {
		f.starts++
		f.started = true
	}
	return nil
}

func (f *fakeScreen) Stop() {
	if f.started {
		f.stops++
		f.started = false
	}
}

func (f *fakeScreen) Size() (int, int) { return f.w, f.h }

func (f *fakeScreen) Draw(frame string) {
	if f.onDraw != nil {
		f.onDraw(len(f.frames))
	}
	f.frames = append(f.frames, frame)
	if strings.Contains(frame, "checking…") || len(f.script) == 0 {
		return
	}
	f.keys <- f.script[0]
	f.script = f.script[1:]
}

func (f *fakeScreen) Keys() <-chan term.Key     { return f.keys }
func (f *fakeScreen) Resized() <-chan os.Signal { return f.resized }
func (f *fakeScreen) Close()                    {}

func (f *fakeScreen) WaitKey(ctx context.Context) (term.Key, error) {
	if len(f.waitKeys) == 0 {
		return term.Key{Code: term.KeyEnter}, nil
	}
	k := f.waitKeys[0]
	f.waitKeys = f.waitKeys[1:]
	return k, nil
}

func (f *fakeScreen) last() string {
	if len(f.frames) == 0 {
		return ""
	}
	return f.frames[len(f.frames)-1]
}

// keys builds a key script: each string's runes as typed keys, plus
// term.Key values as they are.
func keys(parts ...any) []term.Key {
	var out []term.Key
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			for _, r := range v {
				out = append(out, term.Key{Code: term.KeyRune, Rune: r})
			}
		case term.Key:
			out = append(out, v)
		case []term.Key:
			out = append(out, v...)
		}
	}
	return out
}

var (
	kEnter     = term.Key{Code: term.KeyEnter}
	kEsc       = term.Key{Code: term.KeyEsc}
	kUp        = term.Key{Code: term.KeyUp}
	kDown      = term.Key{Code: term.KeyDown}
	kLeft      = term.Key{Code: term.KeyLeft}
	kRight     = term.Key{Code: term.KeyRight}
	kShiftUp   = term.Key{Code: term.KeyUp, Shift: true}
	kShiftDown = term.Key{Code: term.KeyDown, Shift: true}
	kBackTab   = term.Key{Code: term.KeyBackTab}
	kBackspace = term.Key{Code: term.KeyBackspace}
	kDelete    = term.Key{Code: term.KeyDelete}
)

// withScreen makes runManager use scr and treat the session as a terminal.
func withScreen(t *testing.T, scr *fakeScreen) {
	t.Helper()
	old, oldInt := openScreen, isInteractive
	openScreen = func(io.Writer) (screen, error) { return scr, nil }
	isInteractive = func(io.Writer) bool { return true }
	t.Cleanup(func() { openScreen, isInteractive = old, oldInt })
}

// runWithTimeout runs args with a deadline, so a script that never quits
// fails instead of hanging.
func runWithTimeout(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errb strings.Builder
	err = run(ctx, args, &out, &errb)
	if ctx.Err() != nil {
		t.Fatalf("%v: timed out (the key script never quit)", args)
	}
	return out.String(), errb.String(), err
}

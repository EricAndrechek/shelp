package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPrompterInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, w, _ := os.Pipe() // a read that blocks until the context ends
	defer w.Close()
	var out strings.Builder
	pr := newPrompter(ctx, r, &out)
	cancel()
	if pr.yesNo("Go?", true) {
		t.Error("an interrupted prompt said yes")
	}
	if !errors.Is(pr.err(), errInterrupted) {
		t.Errorf("err %v", pr.err())
	}
	// Esc in an answer is no, even when the default is yes.
	pr = newPrompter(context.Background(), strings.NewReader("\x1b\n"), &out)
	if pr.yesNo("Go?", true) || pr.err() != nil {
		t.Error("Esc answered yes")
	}
	// EOF keeps the default.
	pr = newPrompter(context.Background(), strings.NewReader(""), &out)
	if !pr.yesNo("Go?", true) {
		t.Error("EOF did not keep the default")
	}
	if code, show := exitCode(context.Background(), errInterrupted); code != 130 || show {
		t.Errorf("exit %d show %v", code, show)
	}
}

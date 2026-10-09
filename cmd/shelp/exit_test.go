package main

import (
	"context"
	"errors"
	"testing"
)

func TestExitCode(t *testing.T) {
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		name string
		ctx  context.Context
		err  error
		code int
		show bool
	}{
		{"interrupted", interrupted, errors.New("fakesrv: could not reach http://127.0.0.1:1/v1: interrupt signal received"), 130, false},
		{"interrupted usage error", interrupted, usageError{errors.New("bad flag")}, 130, false},
		{"usage error", context.Background(), usageError{errors.New("bad flag")}, 2, true},
		{"failure", context.Background(), errors.New("no provider answered"), 1, true},
	} {
		if code, show := exitCode(c.ctx, c.err); code != c.code || show != c.show {
			t.Errorf("%s: got %d, %v; want %d, %v", c.name, code, show, c.code, c.show)
		}
	}
}

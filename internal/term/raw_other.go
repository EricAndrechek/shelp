//go:build !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !linux

package term

import (
	"errors"
	"os"
)

// State is a terminal's mode before MakeRaw.
type State struct{}

// Supported reports whether this platform has raw mode.
const Supported = false

var errUnsupported = errors.New("interactive mode is not supported on this platform")

func IsTerminal(fd int) bool               { return false }
func MakeRaw(fd int) (*State, error)       { return nil, errUnsupported }
func Restore(fd int, s *State) error       { return nil }
func Size(fd int) (cols, rows int)         { return 0, 0 }
func read(fd int, buf []byte) (int, error) { return 0, errUnsupported }
func NotifyResize(c chan<- os.Signal)      {}
func QuitSignals() []os.Signal             { return nil }
func SignalExitCode(s os.Signal) int       { return 1 }

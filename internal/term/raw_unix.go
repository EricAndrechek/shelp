//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package term

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// State is a terminal's mode before MakeRaw, for Restore.
type State struct{ t syscall.Termios }

// Supported reports whether this platform has raw mode.
const Supported = true

func getTermios(fd int) (syscall.Termios, error) {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlGetTermios, uintptr(unsafe.Pointer(&t)))
	if e != 0 {
		return t, e
	}
	return t, nil
}

func setTermios(fd int, t *syscall.Termios) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlSetTermios, uintptr(unsafe.Pointer(t)))
	if e != 0 {
		return e
	}
	return nil
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool {
	_, err := getTermios(fd)
	return err == nil
}

// MakeRaw puts fd into raw mode (cfmakeraw: no echo, no line editing, no
// signals from Ctrl-C/Ctrl-Z, no output processing) with reads that return
// after 100ms when nothing arrives (VMIN=0, VTIME=1). It returns the old
// state for Restore.
func MakeRaw(fd int) (*State, error) {
	old, err := getTermios(fd)
	if err != nil {
		return nil, err
	}
	t := old
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1
	if err := setTermios(fd, &t); err != nil {
		return nil, err
	}
	return &State{t: old}, nil
}

// Restore puts fd back the way MakeRaw found it. A nil state is a no-op.
func Restore(fd int, s *State) error {
	if s == nil {
		return nil
	}
	return setTermios(fd, &s.t)
}

type winsize struct{ Row, Col, X, Y uint16 }

// Size returns fd's terminal size in columns and rows, 0, 0 if unknown.
func Size(fd int) (cols, rows int) {
	var ws winsize
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if e != 0 {
		return 0, 0
	}
	return int(ws.Col), int(ws.Row)
}

// read reads what is there, waiting at most VTIME (100ms in raw mode); 0
// bytes means the input was quiet. EINTR (a SIGWINCH, say) is a quiet read.
func read(fd int, buf []byte) (int, error) {
	n, err := syscall.Read(fd, buf)
	if err == syscall.EINTR || err == syscall.EAGAIN {
		return 0, nil
	}
	if n < 0 {
		n = 0
	}
	return n, err
}

// NotifyResize relays window-size changes (SIGWINCH) to c.
func NotifyResize(c chan<- os.Signal) { signal.Notify(c, syscall.SIGWINCH) }

// QuitSignals are the signals an interactive screen catches so it can put
// the terminal back before exiting (SIGINT is main's: it cancels the
// context).
func QuitSignals() []os.Signal { return []os.Signal{syscall.SIGTERM, syscall.SIGHUP} }

// SignalExitCode is 128 + the signal's number, as shells report a death by
// signal.
func SignalExitCode(s os.Signal) int {
	if n, ok := s.(syscall.Signal); ok {
		return 128 + int(n)
	}
	return 1
}

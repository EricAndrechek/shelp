//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package style

import (
	"syscall"
	"unsafe"
)

const tiocgwinsz = syscall.TIOCGWINSZ

func ioctlWinsz(fd uintptr, ws *winsize) bool {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, tiocgwinsz, uintptr(unsafe.Pointer(ws)))
	return e == 0
}

//go:build linux

package style

import (
	"syscall"
	"unsafe"
)

func ioctlWinsz(fd uintptr, ws *winsize) bool {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(ws)))
	return e == 0
}

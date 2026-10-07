//go:build !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !linux

package style

func ioctlWinsz(fd uintptr, ws *winsize) bool { return false }

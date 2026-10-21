//go:build !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !linux

package style

const haveWinszIoctl = false

func ioctlWinsz(fd uintptr, ws *winsize) bool { return false }

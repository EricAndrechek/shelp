//go:build unix

package provider

import (
	"os/exec"
	"syscall"
	"time"
)

// isolate puts cmd in its own process group and makes context cancellation
// kill that whole group. Once the child is out of the terminal's foreground
// group, Go's default cancel (kill just the child) would leave anything the
// child spawned running, so the signal goes to -pgid instead.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Pgid == pid because of Setpgid. ESRCH means it already exited.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

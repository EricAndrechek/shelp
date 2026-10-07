//go:build !unix

package provider

import "os/exec"

// isolate is a no-op where process groups are not available.
func isolate(cmd *exec.Cmd) {}

package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

// CmdResult is the outcome of running a subprocess.
type CmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// CmdOpts is the full set of knobs RunCmdOpts accepts. The zero value
// matches RunCmd's old, fixed behavior: no stdin, no streaming, and -
// critically - Dir "" and Env nil both mean "inherit the calling process's",
// exactly like exec.Cmd's own zero value.
type CmdOpts struct {
	// Dir sets the subprocess's working directory without touching this
	// process's own (unlike os.Chdir, safe to use concurrently with other
	// goroutines that rely on relative paths). "" means inherit.
	Dir string
	// Env sets the subprocess's environment. nil means inherit (exec.Cmd
	// semantics); pass os.Environ() plus overrides to inherit-and-extend.
	Env []string
	// Stdin feeds the subprocess's stdin. nil means /dev/null.
	Stdin io.Reader
	// OnChunk, when non-nil, receives stdout as it streams.
	OnChunk func(string)
	// NewProcessGroup runs the subprocess in its own process group (so it
	// is never the terminal's foreground job) and makes ctx cancellation
	// kill the whole group. Stdin must stay nil: a background group that
	// reads the tty would get SIGTTIN. No-op on non-unix platforms.
	NewProcessGroup bool
}

// RunCmd runs name with args, feeding stdin (nil means /dev/null), and
// streams stdout to onChunk when it is non-nil, inheriting this process's
// working directory and environment. A non-zero exit is not an error here;
// callers inspect ExitCode/Stderr. err is non-nil only when the process
// could not run or ctx expired. It is a thin wrapper over RunCmdOpts kept
// for callers (fm, shortcuts, openai) that never need a per-call Dir/Env.
func RunCmd(ctx context.Context, stdin io.Reader, onChunk func(string), name string, args ...string) (CmdResult, error) {
	return RunCmdOpts(ctx, CmdOpts{Stdin: stdin, OnChunk: onChunk}, name, args...)
}

// RunCmdOpts is RunCmd with a per-call working directory and/or
// environment. Setting Dir here - instead of os.Chdir'ing the whole
// process - is what makes it safe to run several subprocesses that care
// about relative paths concurrently in one process (e.g. shelp-bench
// running providers in parallel while writing relative bench/runs/...
// paths itself).
func RunCmdOpts(ctx context.Context, opts CmdOpts, name string, args ...string) (CmdResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.Stdin = opts.Stdin
	if opts.NewProcessGroup {
		isolate(cmd)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var out strings.Builder
	onChunk := opts.OnChunk
	if onChunk == nil {
		cmd.Stdout = &out
		err := cmd.Run()
		return finish(ctx, out.String(), stderr.String(), err)
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return CmdResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return CmdResult{}, err
	}
	r := bufio.NewReader(pipe)
	buf := make([]byte, 4096)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			s := string(buf[:n])
			out.WriteString(s)
			onChunk(s)
		}
		if rerr != nil {
			break
		}
	}
	err = cmd.Wait()
	return finish(ctx, out.String(), stderr.String(), err)
}

func finish(ctx context.Context, stdout, stderr string, err error) (CmdResult, error) {
	res := CmdResult{Stdout: stdout, Stderr: stderr}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// LookPath reports whether name is on PATH, returning its path.
func LookPath(name string) (string, bool) {
	p, err := exec.LookPath(name)
	return p, err == nil
}

// FirstLine returns the first non-empty trimmed line of s.
func FirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

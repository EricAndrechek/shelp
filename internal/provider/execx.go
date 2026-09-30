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

// RunCmd runs name with args, feeding stdin (nil means /dev/null), and
// streams stdout to onChunk when it is non-nil. A non-zero exit is not an
// error here; callers inspect ExitCode/Stderr. err is non-nil only when the
// process could not run or ctx expired.
func RunCmd(ctx context.Context, stdin io.Reader, onChunk func(string), name string, args ...string) (CmdResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var out strings.Builder
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

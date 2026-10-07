package provider

import (
	"context"
	"regexp"
	"strings"

	"github.com/ericandrechek/shelp/internal/config"
)

// fm: Apple's on-device Foundation Model via the `fm` CLI that ships with
// macOS 27+. Free, offline, private; ~0.6s per short answer. The machine
// must have accepted Apple's terms once (`sudo fm license`, exit 69 until
// then) and have Apple Intelligence enabled.
//
// Notes from tuning (2026-09-25):
//   - --guardrails permissive-content-transformations: the default level
//     refused "find all files over 100MB in my home dir".
//   - -g (greedy) for determinism; --no-stream unless the caller streams.
//   - `--` before the prompt so a prompt starting with "-" isn't a flag.
//   - stdin must not be inherited: fm reads a piped stdin as the prompt.

type fmFamily struct{}

func init() { Register(fmFamily{}) }

func (fmFamily) Name() string { return "fm" }

func (fmFamily) Providers(cfg *config.Config) []Provider {
	if _, ok := LookPath("fm"); !ok {
		return nil
	}
	return []Provider{&fmProvider{}}
}

type fmProvider struct{}

func (*fmProvider) ID() string          { return "fm" }
func (*fmProvider) Kind() Kind          { return OnDevice }
func (*fmProvider) Description() string { return "Apple on-device model (fm CLI, macOS 27+)" }

func (*fmProvider) Label() string { return "Apple on-device model" }

func (p *fmProvider) Detect(ctx context.Context) Status {
	lic, err := RunCmd(ctx, nil, nil, "fm", "license", "--status")
	if err != nil {
		return Status{Reason: "fm failed to run", Detail: err.Error()}
	}
	if lic.ExitCode != 0 {
		return Status{Reason: "license not accepted", Fix: "run `sudo fm license`"}
	}
	av, err := RunCmd(ctx, nil, nil, "fm", "available", "--model", "system")
	if err != nil {
		return Status{Reason: "fm failed to run", Detail: err.Error()}
	}
	line := FirstLine(stripANSI(av.Stdout + av.Stderr))
	if av.ExitCode != 0 || !strings.Contains(strings.ToLower(line), "available") || strings.Contains(strings.ToLower(line), "not available") || strings.Contains(strings.ToLower(line), "unavailable") {
		return Status{Reason: "model not available", Fix: "enable Apple Intelligence in System Settings", Detail: line}
	}
	return Status{Available: true, Version: FirstLine(lic.Stdout)}
}

func (p *fmProvider) Generate(ctx context.Context, req Request) (string, error) {
	args := []string{"respond", "-g", "--guardrails", "permissive-content-transformations"}
	if req.OnChunk == nil {
		args = append(args, "--no-stream")
	} else {
		args = append(args, "--stream")
	}
	args = append(args, "-i", req.System, "--", req.Prompt)
	res, err := RunCmd(ctx, nil, req.OnChunk, "fm", args...)
	if err != nil {
		return "", err
	}
	switch {
	case res.ExitCode == 69:
		return "", Unavailable(p.ID(), "Apple's fm terms not accepted: run `sudo fm license`")
	case res.ExitCode != 0:
		msg := FirstLine(stripANSI(res.Stderr))
		if msg == "" {
			msg = FirstLine(stripANSI(res.Stdout))
		}
		return "", Unavailable(p.ID(), "fm exited %d: %s", res.ExitCode, msg)
	}
	return res.Stdout, nil
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

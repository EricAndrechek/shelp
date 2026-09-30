package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultLatencyPath is where `shelp-bench latency` appends its results.
const DefaultLatencyPath = "bench/latency.jsonl"

// DefaultLatencyPrompt is used when --prompt is not given.
const DefaultLatencyPrompt = "list files sorted by size"

// BuildShelp builds ./cmd/shelp to a temp file and returns its path. The
// caller must run this from the module root (cmd/shelp resolves relative to
// the working directory, same as any other `go build` invocation).
func BuildShelp() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "shelp-bench-latency-*")
	if err != nil {
		return "", nil, err
	}
	bin := dir + "/shelp"
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/shelp")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("go build ./cmd/shelp: %w", err)
	}
	return bin, func() { os.RemoveAll(dir) }, nil
}

// LatencyResult is one provider's end-to-end latency sample, as recorded to
// bench/latency.jsonl.
type LatencyResult struct {
	Provider  string    `json:"provider"`
	N         int       `json:"n"`
	ColdMS    int64     `json:"cold_ms"`
	WarmP50MS int64     `json:"warm_p50_ms"`
	WarmP90MS int64     `json:"warm_p90_ms"`
	Errors    int       `json:"errors"`
	Timestamp time.Time `json:"timestamp"`
}

// MeasureLatency runs `<binary> -p <providerID> --json <prompt>` n times
// sequentially (end-to-end process spawn -> output, what a user feels) and
// summarizes cold (first call) vs warm (the rest) latency.
func MeasureLatency(binary, providerID, prompt string, n int) LatencyResult {
	res := LatencyResult{Provider: providerID, N: n, Timestamp: time.Now().UTC()}
	var warm []int64
	for i := 0; i < n; i++ {
		start := time.Now()
		cmd := exec.Command(binary, "-p", providerID, "--json", prompt)
		out, err := cmd.Output()
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			res.Errors++
			continue
		}
		var parsed struct {
			LatencyMS int64 `json:"latency_ms"`
		}
		_ = json.Unmarshal(out, &parsed)
		// elapsed is process spawn -> output, which is what a user feels;
		// it also includes provider latency_ms plus process/exec overhead.
		if i == 0 {
			res.ColdMS = elapsed
		} else {
			warm = append(warm, elapsed)
		}
	}
	if p50, ok := percentile(warm, 50); ok {
		res.WarmP50MS = p50
	}
	if p90, ok := percentile(warm, 90); ok {
		res.WarmP90MS = p90
	}
	return res
}

// AppendLatency appends one LatencyResult line to path.
func AppendLatency(path string, r LatencyResult) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

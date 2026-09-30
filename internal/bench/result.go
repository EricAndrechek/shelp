package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// TrialResult is one line of results.jsonl: the outcome of a single trial.
type TrialResult struct {
	Case         string        `json:"case"`
	Mode         string        `json:"mode"`
	Shell        string        `json:"shell"`
	OS           string        `json:"os"`
	Provider     string        `json:"provider"`
	Template     string        `json:"template"`
	Context      ContextFields `json:"context"`
	Sample       int           `json:"sample"`
	Raw          string        `json:"raw"`
	Output       string        `json:"output"`
	Error        string        `json:"error,omitempty"`
	Unavailable  bool          `json:"unavailable"`
	RateLimited  bool          `json:"rate_limited,omitempty"`
	LatencyMS    int64         `json:"latency_ms"`
	FirstChunkMS int64         `json:"first_chunk_ms"`
	Cold         bool          `json:"cold"`
	Checks       Checks        `json:"checks"`
	GradeKey     string        `json:"grade_key"`
	StartedAt    time.Time     `json:"started_at"`

	// RateLimitReason is the *provider.RateLimitedError's Reason when
	// RateLimited is true, for runner.go's circuit-breaker message. It is
	// never persisted (results.jsonl only ever stores the general `error`
	// string above).
	RateLimitReason string `json:"-"`
}

// TrialKey reconstructs the Trial.Key() this result was produced from,
// matching Trial.Key()'s field order and separator exactly.
func (r TrialResult) TrialKey() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%d", r.Case, r.Shell, r.OS, r.Provider, r.Template, r.Sample)
}

// Manifest is run.json: the arguments and environment a run was invoked
// with.
type Manifest struct {
	Args        []string           `json:"args"`
	HostOS      string             `json:"host_os"`
	GitCommit   string             `json:"git_commit"`
	Providers   []ProviderManifest `json:"providers"`
	Templates   []string           `json:"templates"`
	Cases       []string           `json:"cases"`
	Samples     int                `json:"samples"`
	Context     []string           `json:"context"`
	Concurrency int                `json:"concurrency"`
	StartedAt   time.Time          `json:"started_at"`
	EndedAt     time.Time          `json:"ended_at,omitempty"`
	Trials      int                `json:"trials"`
	Resumed     bool               `json:"resumed,omitempty"`
	// BreakerTrips records every provider that tripped the rate-limit
	// circuit breaker during this run (see runner.go); empty when none
	// did.
	BreakerTrips []BreakerTrip `json:"breaker_trips,omitempty"`
}

// ProviderManifest records one provider's identity and Detect result as of
// run start.
type ProviderManifest struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	Version     string `json:"version,omitempty"`
}

// ResultsPath and ManifestPath are the fixed file names inside a run
// directory.
func ResultsPath(dir string) string  { return dir + "/results.jsonl" }
func ManifestPath(dir string) string { return dir + "/run.json" }

// LoadResults reads every line of a run directory's results.jsonl. A
// missing file yields an empty slice, not an error (a fresh --out dir).
func LoadResults(dir string) ([]TrialResult, error) {
	f, err := os.Open(ResultsPath(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeResults(f)
}

func decodeResults(r io.Reader) ([]TrialResult, error) {
	var out []TrialResult
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var tr TrialResult
		if err := json.Unmarshal(line, &tr); err != nil {
			return nil, fmt.Errorf("results.jsonl: %w", err)
		}
		out = append(out, tr)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadManifest reads a run directory's run.json.
func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(ManifestPath(dir))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("run.json: %w", err)
	}
	return m, nil
}

// SaveManifest writes run.json, formatted for readability, creating dir if
// needed.
func SaveManifest(dir string, m Manifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ManifestPath(dir), append(b, '\n'), 0o644)
}

// ResultWriter appends TrialResult lines to results.jsonl as they complete,
// flushing and syncing after each write so a crash mid-run keeps finished
// trials.
type ResultWriter struct {
	f *os.File
}

// OpenResultWriter opens (creating dir and file as needed) results.jsonl for
// appending.
func OpenResultWriter(dir string) (*ResultWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(ResultsPath(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &ResultWriter{f: f}, nil
}

// Write appends one result line and durably flushes it.
func (w *ResultWriter) Write(r TrialResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := w.f.Write(b); err != nil {
		return err
	}
	return w.f.Sync()
}

// Close closes the underlying file.
func (w *ResultWriter) Close() error { return w.f.Close() }

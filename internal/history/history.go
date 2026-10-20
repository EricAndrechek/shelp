// Package history keeps shelp's request history: an append-only JSONL file
// with one record per suggest/explain call, at
// $XDG_STATE_HOME/shelp/history.jsonl (~/.local/state/shelp/ by default).
//
// The file holds the user's prompts, so it is created 0600 in a 0700
// directory. Writing is best-effort: Append swallows every error and does
// one O_APPEND write of a single line, so it can neither fail nor slow the
// real command noticeably.
package history

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
)

// DefaultMaxEntries is the retention when [history] max_entries is unset.
const DefaultMaxEntries = 1000

// Tokens is a backend-reported token count.
type Tokens struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// Record is one history line.
type Record struct {
	Time         string   `json:"time"` // RFC3339
	Mode         string   `json:"mode"` // "suggest" or "explain"
	Input        string   `json:"input"`
	Output       string   `json:"output"` // what was printed; empty on error
	Provider     string   `json:"provider,omitempty"`
	LatencyMS    int64    `json:"latency_ms"`
	FirstChunkMS int64    `json:"first_chunk_ms,omitempty"` // streamed calls only
	Tokens       *Tokens  `json:"tokens,omitempty"`
	Tried        []string `json:"tried,omitempty"` // failed or skipped before Provider
	Template     string   `json:"template,omitempty"`
	Shell        string   `json:"shell,omitempty"`
	Error        string   `json:"error,omitempty"`
	// Key identifies the request (see Key) so a later answer cache can
	// find entries; nothing reads it yet.
	Key string `json:"key"`
}

// Key is a stable hash of what determines an answer: the mode, the template
// and the rendered system and user prompts.
func Key(mode, template, system, prompt string) string {
	h := sha256.New()
	for _, s := range []string{mode, template, system, prompt} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Path is the history file: $XDG_STATE_HOME/shelp/history.jsonl, else
// ~/.local/state/shelp/history.jsonl.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "shelp", "history.jsonl")
}

// Settings is the [history] config table; every field is optional.
type Settings struct {
	Enabled    *bool `toml:"enabled"`
	MaxEntries int   `toml:"max_entries"`
}

// Load reads the [history] table. A malformed table yields the defaults.
func Load(cfg *config.Config) Settings {
	var s Settings
	_ = cfg.Section("history", &s)
	return s
}

// IsEnabled reports whether calls are recorded: not turned off by
// `[history] enabled = false` or by SHELP_HISTORY=0 (or off/false/no).
func (s Settings) IsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SHELP_HISTORY"))) {
	case "0", "off", "false", "no":
		return false
	}
	return s.Enabled == nil || *s.Enabled
}

// Max is the retention in entries.
func (s Settings) Max() int {
	if s.MaxEntries > 0 {
		return s.MaxEntries
	}
	return DefaultMaxEntries
}

// minLineBytes is a safe lower bound on a record's size; a file smaller
// than 1.5*max*minLineBytes cannot hold more than 1.5*max lines, so the
// retention check then skips reading the file.
const minLineBytes = 120

// Append writes r to the history at path, then trims the file to the newest
// max entries once it holds more than 1.5x that many. Best-effort: errors
// are swallowed.
func Append(path string, r Record, max int) {
	if max < 1 {
		max = DefaultMaxEntries
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	line = append(line, '\n')
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, werr := f.Write(line) // one write: atomic for short lines
	st, serr := f.Stat()
	f.Close()
	if werr != nil || serr != nil || st.Size() < int64(max)*3/2*minLineBytes {
		return
	}
	trim(path, max)
}

// trim rewrites the file (temp + rename) to its newest max lines when it
// has more than 1.5*max.
func trim(path string, max int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	n := bytes.Count(data, []byte{'\n'})
	if n <= max*3/2 {
		return
	}
	keep := data
	for i := 0; i < n-max; i++ {
		keep = keep[bytes.IndexByte(keep, '\n')+1:]
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".history-*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(keep)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}

// Read returns every record in the file at path, oldest first. A missing
// file is no records; a line that is not a record is skipped.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Mode == "" {
			continue
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Clear deletes the history file; a missing file is fine.
func Clear(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Now is the timestamp format used in Record.Time.
func Now() string { return time.Now().Format(time.RFC3339) }

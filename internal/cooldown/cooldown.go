// Package cooldown persists provider rate-limit cooldowns across shelp
// invocations: once a provider reports it is rate-limited (see
// provider.RateLimitedError), shelp remembers not to call it again for a
// while, so a fallback chain -- or a benchmark run -- doesn't keep
// hammering (and, for Apple Shortcuts, keep triggering a macOS
// notification from) a backend that just said no.
//
// The store is one small JSON file, $XDG_CACHE_HOME/shelp/cooldown.json
// (~/.cache/shelp/cooldown.json by default): provider ID -> {until,
// reason}. Reads and writes are failure-tolerant (a missing or corrupt
// file just means "no cooldowns", never an error) and writes are atomic
// (a uniquely-named temp file + rename) so a crash, or two shelp
// processes writing at once, never leaves a torn or half-written file.
package cooldown

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// DefaultDuration is how long a provider is cooled down for when a rate
// limit doesn't say how long to wait (no Retry-After, no CLI-reported
// duration) and no `cooldown` config key overrides it.
const DefaultDuration = 30 * time.Minute

// Entry is one provider's recorded cooldown.
type Entry struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason,omitempty"`
}

// Path returns the cooldown store location: $XDG_CACHE_HOME/shelp/
// cooldown.json, or ~/.cache/shelp/cooldown.json when XDG_CACHE_HOME is
// unset.
func Path() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "shelp", "cooldown.json")
}

// Load reads every recorded cooldown. A missing or corrupt file yields an
// empty map, never an error: the cooldown store is an optimization, not a
// dependency shelp can fail on.
func Load() map[string]Entry {
	b, err := os.ReadFile(Path())
	if err != nil {
		return map[string]Entry{}
	}
	var entries map[string]Entry
	if err := json.Unmarshal(b, &entries); err != nil || entries == nil {
		return map[string]Entry{}
	}
	return entries
}

// Save writes entries atomically: a uniquely-named temp file in the same
// directory, written and closed in full, then renamed over the real path.
// The unique name (rather than a single fixed "*.tmp") means two writers
// racing never truncate each other's in-flight write; the last rename to
// complete simply wins, and the file at Path() is always one writer's
// complete, valid JSON.
func Save(entries map[string]Entry) error {
	path := Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "cooldown-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// Active reports id's cooldown entry, if it has one that is still in
// effect (Until is still in the future).
func Active(entries map[string]Entry, id string) (Entry, bool) {
	e, ok := entries[id]
	if !ok || !time.Now().Before(e.Until) {
		return Entry{}, false
	}
	return e, true
}

// Set records id as rate-limited until `until`, read-modify-writing the
// store fresh so a concurrent shelp process's own recent write isn't
// blindly clobbered (best effort: on a true race the last writer still
// wins, but Save's atomic rename means the file itself is never left
// corrupt either way).
func Set(id string, until time.Time, reason string) error {
	entries := Load()
	entries[id] = Entry{Until: until.UTC(), Reason: reason}
	return Save(entries)
}

// Reset clears every recorded cooldown.
func Reset() error { return Save(map[string]Entry{}) }

// ResetID clears one provider's cooldown, if it has one.
func ResetID(id string) error {
	entries := Load()
	if _, ok := entries[id]; !ok {
		return nil
	}
	delete(entries, id)
	return Save(entries)
}

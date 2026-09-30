package cooldown

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setCacheHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

func TestRoundTrip(t *testing.T) {
	setCacheHome(t)
	until := time.Now().Add(20 * time.Minute)
	if err := Set("shortcuts:apple-pro", until, "usage limit"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries := Load()
	e, ok := entries["shortcuts:apple-pro"]
	if !ok {
		t.Fatal("entry not found after Set")
	}
	if e.Reason != "usage limit" {
		t.Errorf("Reason = %q, want %q", e.Reason, "usage limit")
	}
	if diff := e.Until.Sub(until); diff > time.Second || diff < -time.Second {
		t.Errorf("Until = %v, want ~%v", e.Until, until)
	}
}

func TestPath_DefaultsUnderXDGCacheHome(t *testing.T) {
	dir := setCacheHome(t)
	want := filepath.Join(dir, "shelp", "cooldown.json")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestActive_ExpiredEntryIsNotActive(t *testing.T) {
	setCacheHome(t)
	past := time.Now().Add(-time.Minute)
	if err := Set("fm", past, "old"); err != nil {
		t.Fatal(err)
	}
	entries := Load()
	if _, ok := Active(entries, "fm"); ok {
		t.Error("expired cooldown reported active")
	}
}

func TestActive_FutureEntryIsActive(t *testing.T) {
	setCacheHome(t)
	future := time.Now().Add(time.Hour)
	if err := Set("fm", future, "still limited"); err != nil {
		t.Fatal(err)
	}
	entries := Load()
	e, ok := Active(entries, "fm")
	if !ok {
		t.Fatal("future cooldown not reported active")
	}
	if e.Reason != "still limited" {
		t.Errorf("Reason = %q", e.Reason)
	}
}

func TestActive_UnknownID(t *testing.T) {
	if _, ok := Active(map[string]Entry{}, "nope"); ok {
		t.Error("unknown id reported active")
	}
}

func TestLoad_MissingFileYieldsEmptyMap(t *testing.T) {
	setCacheHome(t)
	entries := Load()
	if len(entries) != 0 {
		t.Errorf("Load() on missing file = %v, want empty", entries)
	}
}

func TestLoad_CorruptFileYieldsEmptyMapNotError(t *testing.T) {
	setCacheHome(t)
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := Load()
	if len(entries) != 0 {
		t.Errorf("Load() on corrupt file = %v, want empty", entries)
	}
}

func TestReset(t *testing.T) {
	setCacheHome(t)
	if err := Set("fm", time.Now().Add(time.Hour), "x"); err != nil {
		t.Fatal(err)
	}
	if err := Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if entries := Load(); len(entries) != 0 {
		t.Errorf("Load() after Reset = %v, want empty", entries)
	}
}

func TestResetID(t *testing.T) {
	setCacheHome(t)
	if err := Set("fm", time.Now().Add(time.Hour), "x"); err != nil {
		t.Fatal(err)
	}
	if err := Set("shortcuts:apple-pro", time.Now().Add(time.Hour), "y"); err != nil {
		t.Fatal(err)
	}
	if err := ResetID("fm"); err != nil {
		t.Fatalf("ResetID: %v", err)
	}
	entries := Load()
	if _, ok := entries["fm"]; ok {
		t.Error("fm still present after ResetID")
	}
	if _, ok := entries["shortcuts:apple-pro"]; !ok {
		t.Error("shortcuts:apple-pro should be untouched by ResetID(\"fm\")")
	}
	// Resetting an id that was never set is a no-op, not an error.
	if err := ResetID("never-set"); err != nil {
		t.Fatalf("ResetID on unknown id: %v", err)
	}
}

// TestConcurrentSetIsRaceFreeAndLeavesValidJSON exercises Save's atomic,
// uniquely-named-temp-file write under real concurrency (run with
// `go test -race`): many goroutines calling Set at once must never corrupt
// the store (each write is a complete file, torn writes are impossible)
// and must never leave a stray .tmp file behind.
func TestConcurrentSetIsRaceFreeAndLeavesValidJSON(t *testing.T) {
	dir := setCacheHome(t)
	var wg sync.WaitGroup
	ids := []string{"a", "b", "c", "d", "e"}
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			_ = Set(id, time.Now().Add(time.Duration(i)*time.Minute), "concurrent")
		}(i)
	}
	wg.Wait()

	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	var entries map[string]Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatalf("final cooldown.json is not valid JSON: %v\n%s", err, b)
	}

	files, err := os.ReadDir(filepath.Join(dir, "shelp"))
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", f.Name())
		}
	}
}

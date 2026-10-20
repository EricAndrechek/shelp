package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
)

func rec(i int) Record {
	return Record{Time: Now(), Mode: "suggest", Input: fmt.Sprintf("task %d", i), Output: "ls", Provider: "fm", Key: "k"}
}

func TestAppendAndRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "history.jsonl")
	r := rec(1)
	r.Tokens = &Tokens{Input: 5, Output: 2}
	r.Tried = []string{"a", "b"}
	Append(p, r, 10)
	Append(p, rec(2), 10)
	got, err := Read(p)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got[0].Input != "task 1" || got[0].Tokens == nil || got[0].Tokens.Input != 5 || len(got[0].Tried) != 2 {
		t.Fatalf("record 0 = %+v", got[0])
	}
}

func TestPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "shelp", "history.jsonl")
	Append(p, rec(1), 10)
	for path, want := range map[string]os.FileMode{p: 0o600, filepath.Dir(p): 0o700} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, st.Mode().Perm(), want)
		}
	}
}

func TestRetentionRewrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	const max = 10
	for i := 0; i < 30; i++ {
		Append(p, rec(i), max)
		got, _ := Read(p)
		if len(got) > max*3/2+1 {
			t.Fatalf("after %d appends: %d lines, over the 1.5x limit", i+1, len(got))
		}
	}
	got, _ := Read(p)
	if len(got) < max || got[len(got)-1].Input != "task 29" {
		t.Fatalf("got %d entries, last %q", len(got), got[len(got)-1].Input)
	}
	// the oldest were dropped, the order kept
	if got[0].Input == "task 0" {
		t.Fatal("oldest entry survived")
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("rewritten file mode = %v", st.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".history-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestTrimKeepsNewest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	var b strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, `{"mode":"suggest","input":"t%d"}`+"\n", i)
	}
	os.WriteFile(p, []byte(b.String()), 0o600)
	trim(p, 5)
	got, _ := Read(p)
	if len(got) != 5 || got[0].Input != "t15" || got[4].Input != "t19" {
		t.Fatalf("got %+v", got)
	}
}

func TestCorruptLineSkipped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	os.WriteFile(p, []byte(`{"mode":"suggest","input":"a"}`+"\nnot json\n\n"+`{"mode":"explain","input":"b"}`+"\n"), 0o600)
	got, err := Read(p)
	if err != nil || len(got) != 2 || got[1].Input != "b" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestReadMissing(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSettings(t *testing.T) {
	t.Setenv("SHELP_HISTORY", "")
	cfg, _ := config.Parse("[history]\nenabled = false\nmax_entries = 5\n")
	s := Load(cfg)
	if s.IsEnabled() || s.Max() != 5 {
		t.Fatalf("%+v enabled=%v", s, s.IsEnabled())
	}
	cfg, _ = config.Parse("")
	s = Load(cfg)
	if !s.IsEnabled() || s.Max() != DefaultMaxEntries {
		t.Fatalf("defaults: %+v", s)
	}
	t.Setenv("SHELP_HISTORY", "0")
	if s.IsEnabled() {
		t.Fatal("SHELP_HISTORY=0 should disable")
	}
}

func TestPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x")
	if got := Path(); got != "/x/shelp/history.jsonl" {
		t.Fatal(got)
	}
}

func TestKeyStable(t *testing.T) {
	a := Key("suggest", "v1", "sys", "p")
	if a != Key("suggest", "v1", "sys", "p") || a == Key("explain", "v1", "sys", "p") || a == Key("suggest", "v1", "sys", "q") {
		t.Fatal("key not stable/distinct")
	}
	// field boundaries matter
	if Key("a", "bc", "", "") == Key("ab", "c", "", "") {
		t.Fatal("ambiguous key")
	}
}

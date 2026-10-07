package provider

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericandrechek/shelp/internal/config"
)

func TestChainHasShortcuts(t *testing.T) {
	cases := []struct {
		chain []string
		want  bool
	}{
		{[]string{"shortcuts:apple-pro", "fm"}, true},
		{[]string{"fm", "claude:haiku"}, false},
		{[]string{"SHORTCUTS:APPLE-PRO"}, true}, // case-insensitive
		{nil, false},
		{[]string{}, false},
	}
	for _, tc := range cases {
		if got := chainHasShortcuts(tc.chain); got != tc.want {
			t.Errorf("chainHasShortcuts(%v) = %v, want %v", tc.chain, got, tc.want)
		}
	}
}

func TestShouldRunShortcutVersionCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	if !shouldRunShortcutVersionCheck("1.2.3") {
		t.Error("shouldRunShortcutVersionCheck: want true when no cache file exists yet")
	}
	if err := recordShortcutVersionCheck("1.2.3"); err != nil {
		t.Fatalf("recordShortcutVersionCheck: %v", err)
	}
	if shouldRunShortcutVersionCheck("1.2.3") {
		t.Error("shouldRunShortcutVersionCheck: want false right after recording the same version")
	}
	if !shouldRunShortcutVersionCheck("1.2.4") {
		t.Error("shouldRunShortcutVersionCheck: want true for a different version than the one recorded")
	}
}

func TestShortcutCheckPathDefaultsUnderCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	got := shortcutCheckPath()
	want := filepath.Join(dir, "shelp", "shortcut-check.json")
	if got != want {
		t.Errorf("shortcutCheckPath() = %q, want %q", got, want)
	}
}

// fakeFreshness fakes `shortcuts list` as names and the marker as
// versions (missing: the run fails), action count 0; it returns the names
// asked their version.
func fakeFreshness(t *testing.T, names []string, versions map[string]int) *[]string {
	t.Helper()
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	shortcutsListCmd = func(ctx context.Context) (string, error) { return strings.Join(names, "\n"), nil }
	var asked []string
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		asked = append(asked, name)
		return 0, nil
	})
	installFakeMarkerVersion(t, func(ctx context.Context, name string) (int, error) {
		if v, ok := versions[name]; ok {
			return v, nil
		}
		return 0, errors.New("shortcuts run exited 1")
	})
	return &asked
}

func TestShortcutsFreshnessHint(t *testing.T) {
	legacy := []string{"shelp · Apple Intelligence Pro", "shelp · ChatGPT 1"}
	cases := []struct {
		name     string
		cfg      string
		names    []string
		versions map[string]int
		want     string
		asked    []string
	}{
		{"current", "", append([]string{"shelp"}, legacy...), map[string]int{"shelp": ShortcutFormatVersion}, "", []string{"shelp"}},
		{"outdated", "", []string{"shelp"}, map[string]int{"shelp": 4}, shortcutOutdatedHint, []string{"shelp"}},
		{"unreadable is never outdated", "", []string{"shelp"}, nil, "", []string{"shelp"}},
		{"only legacy ones: the upgrade to one shortcut", "", legacy, map[string]int{legacy[0]: 4}, shortcutMigrateHint, nil},
		{"nothing installed", "", []string{"Ask AI"}, nil, "", nil},
		{"configured name", "[shortcuts]\nname = \"Ask AI\"\n", []string{"shelp", "Ask AI"}, map[string]int{"shelp": 5, "Ask AI": 4}, shortcutOutdatedHint, []string{"Ask AI"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asked := fakeFreshness(t, c.names, c.versions)
			cfg, err := config.Parse(c.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := shortcutsFreshnessHint(context.Background(), cfg); got != c.want {
				t.Errorf("hint = %q, want %q", got, c.want)
			}
			if strings.Join(*asked, ",") != strings.Join(c.asked, ",") {
				t.Errorf("asked %q for a version, want %q (legacy shortcuts are never run)", *asked, c.asked)
			}
		})
	}
}

func TestShortcutsFreshnessHintListError(t *testing.T) {
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	shortcutsListCmd = func(ctx context.Context) (string, error) { return "", errors.New("shortcuts list exited 1") }
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		t.Fatal("asked a version after the list failed")
		return 0, nil
	})
	if got := shortcutsFreshnessHint(context.Background(), &config.Config{}); got != "" {
		t.Errorf("hint = %q, want none when the list fails", got)
	}
}

func TestCheckShortcutsFreshnessEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	fakeFreshness(t, []string{"shelp"}, map[string]int{"shelp": 4}) // outdated

	var stderr strings.Builder
	cfg := &config.Config{}
	chain := []string{"shortcuts:apple-pro", "fm"}

	CheckShortcutsFreshness(context.Background(), cfg, "9.9.9", chain, &stderr)
	if !strings.Contains(stderr.String(), "shelp setup shortcuts") {
		t.Errorf("stderr = %q, want the update hint on a first, outdated check", stderr.String())
	}

	// Second call, same version: shouldRunShortcutVersionCheck now says no,
	// so nothing more is printed even though the fake still says outdated.
	stderr.Reset()
	CheckShortcutsFreshness(context.Background(), cfg, "9.9.9", chain, &stderr)
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a second call for the same already-checked version", stderr.String())
	}
}

func TestCheckShortcutsFreshnessNeverPrintsForNonShortcutsChain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	origList := shortcutsListCmd
	t.Cleanup(func() { shortcutsListCmd = origList })
	shortcutsListCmd = func(ctx context.Context) (string, error) {
		t.Fatal("shortcuts list should not run when chain has no shortcuts provider")
		return "", nil
	}

	var stderr strings.Builder
	CheckShortcutsFreshness(context.Background(), &config.Config{}, "9.9.9", []string{"fm"}, &stderr)
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty for a chain with no shortcuts provider", stderr.String())
	}
}

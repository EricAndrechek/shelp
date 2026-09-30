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

func TestFindOutdatedInstalledShortcuts(t *testing.T) {
	cfg := &config.Config{}

	t.Run("current", func(t *testing.T) {
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
			return currentShortcutActionCount(), nil
		})
		if findOutdatedInstalledShortcuts(context.Background(), cfg, []string{"shortcuts:apple-pro"}) {
			t.Error("want false when the installed shortcut's action count matches current")
		}
	})

	t.Run("outdated", func(t *testing.T) {
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
			return shortcutV1ActionCount, nil
		})
		if !findOutdatedInstalledShortcuts(context.Background(), cfg, []string{"shortcuts:apple-pro"}) {
			t.Error("want true when the installed shortcut is still v1")
		}
	})

	t.Run("not installed or check failed -- never outdated", func(t *testing.T) {
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
			return 0, errors.New(`Can't get shortcut "shelp · Apple Intelligence Pro" (-1728)`)
		})
		if findOutdatedInstalledShortcuts(context.Background(), cfg, []string{"shortcuts:apple-pro"}) {
			t.Error("want false when the action-count check itself fails (e.g. not installed)")
		}
	})

	t.Run("no shortcuts provider in chain -- never calls the fake", func(t *testing.T) {
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
			t.Fatal("shortcutActionCountCmd should not be called for a chain with no shortcuts:* entry")
			return 0, nil
		})
		if findOutdatedInstalledShortcuts(context.Background(), cfg, []string{"fm", "claude:haiku"}) {
			t.Error("want false for a chain with no shortcuts provider")
		}
	})

	t.Run("dedupes by resolved name", func(t *testing.T) {
		calls := 0
		installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
			calls++
			return currentShortcutActionCount(), nil
		})
		// apple and apple-web share no name by default, but naming both to
		// the same override collapses them to one check.
		cfg, err := config.Parse(`[shortcuts]
names = { "apple" = "Ask AI", "apple-web" = "Ask AI" }
`)
		if err != nil {
			t.Fatalf("config.Parse: %v", err)
		}
		findOutdatedInstalledShortcuts(context.Background(), cfg, []string{"shortcuts:apple", "shortcuts:apple-web"})
		if calls != 1 {
			t.Errorf("shortcutActionCountCmd called %d times, want 1 (same resolved name)", calls)
		}
	})
}

func TestCheckShortcutsFreshnessEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		return shortcutV1ActionCount, nil // outdated
	})

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
	installFakeActionCount(t, func(ctx context.Context, name string) (int, error) {
		t.Fatal("shortcutActionCountCmd should not be called when chain has no shortcuts provider")
		return 0, nil
	})

	var stderr strings.Builder
	CheckShortcutsFreshness(context.Background(), &config.Config{}, "9.9.9", []string{"fm"}, &stderr)
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty for a chain with no shortcuts provider", stderr.String())
	}
}

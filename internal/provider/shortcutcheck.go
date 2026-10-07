package provider

// After a Homebrew upgrade, a user has no way to know shelp shipped a new
// shortcut format (ShortcutFormatVersion) until they happen to run `shelp
// providers` or hit an outdated shortcut some other way. This file adds a
// best-effort, once-per-binary-version runtime hint: cmd/shelp calls
// CheckShortcutsFreshness after printing a normal suggest/explain answer,
// and it prints one line to stderr if an installed shelp shortcut turns
// out to be outdated -- see CheckShortcutsFreshness's doc comment for the
// exact guarantees (never blocks/fails the real command, checked at most
// once per shelp version).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/style"
)

// shortcutCheckTimeout bounds CheckShortcutsFreshness's own work (a
// handful of osascript calls at most) so it can never hang the process it
// was only ever supposed to add a fraction of a second to. A package var,
// like shortcutsPollInterval/-Timeout, so tests can shrink it.
var shortcutCheckTimeout = 5 * time.Second

// shortcutCheckState is shortcut-check.json's shape.
type shortcutCheckState struct {
	CheckedVersion string    `json:"checked_version"`
	CheckedAt      time.Time `json:"checked_at"`
}

// shortcutCheckPath returns the runtime-hint cache file location:
// $XDG_CACHE_HOME/shelp/shortcut-check.json (~/.cache/shelp/... by
// default) -- the same directory convention internal/cooldown's store
// uses for cooldown.json.
func shortcutCheckPath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "shelp", "shortcut-check.json")
}

// shouldRunShortcutVersionCheck reports whether version hasn't already
// been recorded as checked. A missing or corrupt cache file means "not
// checked yet" (true), never an error: like cooldown.json, this cache is
// an optimization, not a dependency shelp can fail on.
func shouldRunShortcutVersionCheck(version string) bool {
	b, err := os.ReadFile(shortcutCheckPath())
	if err != nil {
		return true
	}
	var st shortcutCheckState
	if err := json.Unmarshal(b, &st); err != nil {
		return true
	}
	return st.CheckedVersion != version
}

// recordShortcutVersionCheck records version as checked, atomically (a
// uniquely-named temp file + rename, same pattern as internal/cooldown's
// Save) so a crash or a concurrent shelp process never leaves a torn file.
func recordShortcutVersionCheck(version string) error {
	path := shortcutCheckPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(shortcutCheckState{CheckedVersion: version, CheckedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "shortcut-check-*.tmp")
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

// chainHasShortcuts reports whether chain names at least one shortcuts:*
// provider -- no point ever touching Shortcuts Events when this
// invocation isn't going to use it.
func chainHasShortcuts(chain []string) bool {
	for _, id := range chain {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "shortcuts" || strings.HasPrefix(id, "shortcuts:") {
			return true
		}
	}
	return false
}

// findOutdatedInstalledShortcuts checks only the shortcuts:* provider IDs
// actually named in chain (deduplicated by resolved name) -- not every
// shortcutVariants entry -- since that's what this invocation actually
// relies on. For each, it calls shortcutActionCountCmd directly rather
// than first listing installed shortcuts: shortcutActionCountCmd already
// fails cleanly for a shortcut that doesn't exist (verified live: exit 1,
// "Can't get shortcut ... (-1728)"), and that failure is swallowed exactly
// like any other check failure -- never installed and check-failed are
// both "skip, not outdated" here, since neither is this function's job to
// distinguish (Detect already reports "not installed" separately).
func findOutdatedInstalledShortcuts(ctx context.Context, cfg *config.Config, chain []string) bool {
	var sc shortcutsConfig
	_ = cfg.Section("shortcuts", &sc)

	checked := map[string]bool{}
	for _, id := range chain {
		key, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(id)), "shortcuts:")
		if !ok {
			continue
		}
		v, ok := variantByKey(key)
		if !ok {
			continue
		}
		name := resolveName(v, sc)
		if checked[name] {
			continue
		}
		checked[name] = true

		if v, err := installedShortcutVersion(ctx, name); err == nil && v < ShortcutFormatVersion {
			return true
		}
	}
	return false
}

// CheckShortcutsFreshness is shelp's best-effort, once-per-binary-version
// hint that an installed Apple Shortcuts wrapper is outdated -- the
// scenario it exists for is a `brew upgrade shelp` that shipped a new
// ShortcutFormatVersion, which otherwise gives the user no signal to ever
// run `shelp setup shortcuts` again.
//
// Callers MUST invoke this only after the command's real output has
// already been printed (e.g. via `defer` right after a successful
// provider.Chain call, or from a goroutine only ever awaited after
// output) -- it must never delay or fail the actual suggest/explain
// result:
//   - it returns immediately unless chain names a shortcuts:* provider,
//     and unless this shelp binary version hasn't already been checked
//     (per shortcut-check.json -- see shouldRunShortcutVersionCheck);
//   - all work is bounded by shortcutCheckTimeout;
//   - every failure along the way (an unwritable cache dir, an osascript
//     error, ...) is swallowed silently -- only a confirmed-outdated
//     shortcut ever produces output, and it's always exactly one line.
func CheckShortcutsFreshness(ctx context.Context, cfg *config.Config, version string, chain []string, stderr io.Writer) {
	if !chainHasShortcuts(chain) {
		return
	}
	if !shouldRunShortcutVersionCheck(version) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, shortcutCheckTimeout)
	defer cancel()

	outdated := findOutdatedInstalledShortcuts(cctx, cfg, chain)
	_ = recordShortcutVersionCheck(version) // best-effort; a write failure just means we check again next call

	if outdated {
		fmt.Fprintln(stderr, style.For(stderr).Warn("shelp: your Apple Shortcuts are from an older shelp; run `shelp setup shortcuts` to update them"))
	}
}

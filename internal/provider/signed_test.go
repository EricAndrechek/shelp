package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The pre-signed copy must be this build's format and generator, and not
// near expiry; otherwise every user's setup quietly falls back to signing.
func TestEmbeddedShortcutCurrent(t *testing.T) {
	var m SignedShortcutMeta
	if err := json.Unmarshal(embeddedShortcutMetaJSON, &m); err != nil {
		t.Fatalf("signed/shelp.json: %v", err)
	}
	if !bytes.HasPrefix(embeddedShortcutData, []byte("AEA1")) {
		t.Fatal("signed/shelp.shortcut isn't a signed (AEA1) shortcut; run `make sign-shortcut`")
	}
	if m.Format != ShortcutFormatVersion || m.Fingerprint != WorkflowFingerprint() {
		t.Fatalf("signed/shelp.shortcut is format v%d with a stale fingerprint (or the generator changed); run `make sign-shortcut`", m.Format)
	}
	if time.Now().After(m.NotAfter.Add(-2 * embeddedShortcutMargin)) {
		t.Errorf("signed/shelp.shortcut's signature expires %s; run `make sign-shortcut` before releasing", m.NotAfter.Format("2006-01-02"))
	}
	if data, why := embeddedShortcut(time.Now()); data == nil {
		t.Errorf("embeddedShortcut refused the copy: %s", why)
	}
}

func TestWorkflowFingerprintIgnoresUUIDs(t *testing.T) {
	if a, b := WorkflowFingerprint(), WorkflowFingerprint(); a != b || len(a) != 64 {
		t.Errorf("fingerprints %q and %q; want one stable sha256 across builds with fresh UUIDs", a, b)
	}
}

func TestEmbeddedShortcutRefusesStaleCopies(t *testing.T) {
	origMeta := embeddedShortcutMetaJSON
	t.Cleanup(func() { embeddedShortcutMetaJSON = origMeta })
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	meta := func(format int, fp string, notAfter time.Time) {
		embeddedShortcutMetaJSON, _ = json.Marshal(SignedShortcutMeta{Format: format, Fingerprint: fp, NotAfter: notAfter})
	}
	cases := []struct {
		name     string
		format   int
		fp       string
		notAfter time.Time
		want     string
	}{
		{"usable", ShortcutFormatVersion, WorkflowFingerprint(), now.AddDate(1, 0, 0), ""},
		{"old format", ShortcutFormatVersion - 1, WorkflowFingerprint(), now.AddDate(1, 0, 0), "format v"},
		{"generator changed", ShortcutFormatVersion, "deadbeef", now.AddDate(1, 0, 0), "doesn't match"},
		{"near expiry", ShortcutFormatVersion, WorkflowFingerprint(), now.Add(embeddedShortcutMargin / 2), "expires"},
	}
	for _, c := range cases {
		meta(c.format, c.fp, c.notAfter)
		data, why := embeddedShortcut(now)
		if c.want == "" {
			if data == nil {
				t.Errorf("%s: refused (%s)", c.name, why)
			}
			continue
		}
		if data != nil || !strings.Contains(why, c.want) {
			t.Errorf("%s: got %d bytes, %q; want refused with %q", c.name, len(data), why, c.want)
		}
	}
}

func TestPrepareShortcut(t *testing.T) {
	origGen, origEmbedded := generateShortcut, embeddedShortcut
	t.Cleanup(func() { generateShortcut, embeddedShortcut = origGen, origEmbedded })
	generated := 0
	generateShortcut = func(ctx context.Context, dir, name string) (string, error) {
		generated++
		path := filepath.Join(dir, sanitizeFilename(name)+".shortcut")
		return path, os.WriteFile(path, []byte("freshly signed"), 0o600)
	}

	// usable copy: written as "<name>.shortcut", no signing
	embeddedShortcut = func(time.Time) ([]byte, string) { return []byte("AEA1 pre-signed"), "" }
	dir := t.TempDir()
	path, err := prepareShortcut(context.Background(), dir, "My shelp", func(string) { t.Error("notice for a usable copy") })
	if err != nil || filepath.Base(path) != "My shelp.shortcut" || generated != 0 {
		t.Fatalf("got %q, %v, %d signings; want My shelp.shortcut from the copy", path, err, generated)
	}
	if b, _ := os.ReadFile(path); string(b) != "AEA1 pre-signed" {
		t.Errorf("wrote %q", b)
	}

	// unusable copy: say why, then sign one
	embeddedShortcut = func(time.Time) ([]byte, string) { return nil, "the built-in copy is format v5, not v6" }
	var heard string
	if _, err := prepareShortcut(context.Background(), t.TempDir(), "shelp", func(why string) { heard = why }); err != nil || generated != 1 || !strings.Contains(heard, "format v5") {
		t.Errorf("got %v, %d signings, notice %q; want one signing after the reason", err, generated, heard)
	}
}

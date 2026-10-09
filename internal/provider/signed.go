package provider

// The release's pre-signed shortcut. Shortcuts only imports a signed
// .shortcut file, and `shortcuts sign` is a round trip to Apple's server
// that now and then stalls (see signShortcut), so each release ships the
// current format already signed and setup only falls back to signing
// when that copy can't be used.
//
// `make sign-shortcut` (internal/tools/signshortcut) regenerates both
// files. Run it whenever the generator changes -- TestEmbeddedShortcutCurrent
// fails until the copy's format and fingerprint match -- and before a
// release whose copy is within a couple of months of expiring. The
// signature is Apple's "anyone" kind: an Apple-issued certificate with no
// personal identity in it (CN is a random hex id, O=Apple Inc.), chaining
// to Apple Root CA - G3; the leaf is valid about 13 months.

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

//go:embed signed/shelp.shortcut
var embeddedShortcutData []byte

//go:embed signed/shelp.json
var embeddedShortcutMetaJSON []byte

// SignedShortcutMeta describes signed/shelp.shortcut.
type SignedShortcutMeta struct {
	Format      int       `json:"format"`
	Fingerprint string    `json:"fingerprint"` // WorkflowFingerprint() when it was signed
	SignedAt    time.Time `json:"signed_at"`
	NotAfter    time.Time `json:"not_after"` // the earliest expiry in its certificate chain
}

// embeddedShortcutMargin is how long before its certificate expires the
// embedded copy stops being used: an import shortly before expiry would
// still work, but a user who keeps an old shelp around shouldn't be the
// one to find out.
const embeddedShortcutMargin = 30 * 24 * time.Hour

var uuidRE = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)

// WorkflowFingerprint identifies what buildWorkflow generates, ignoring
// the fresh UUIDs every build gets: each distinct UUID is replaced by its
// order of first appearance before hashing, so two builds of the same
// shape match and any change to the shape doesn't.
func WorkflowFingerprint() string {
	data, err := json.Marshal(buildWorkflow())
	if err != nil {
		panic(err) // buildWorkflow is plain structs; this can't fail
	}
	seen := map[string]string{}
	norm := uuidRE.ReplaceAllStringFunc(string(data), func(u string) string {
		if n, ok := seen[u]; ok {
			return n
		}
		n := fmt.Sprintf("<UUID-%d>", len(seen)+1)
		seen[u] = n
		return n
	})
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// embeddedShortcut returns the pre-signed shortcut when it's this format,
// matches the generator and isn't close to expiring; otherwise nil and
// why not. A package var so setup tests can switch it off.
var embeddedShortcut = func(now time.Time) ([]byte, string) {
	var m SignedShortcutMeta
	if err := json.Unmarshal(embeddedShortcutMetaJSON, &m); err != nil || len(embeddedShortcutData) == 0 {
		return nil, "this build has no pre-signed copy"
	}
	switch {
	case m.Format != ShortcutFormatVersion:
		return nil, fmt.Sprintf("the built-in copy is format v%d, not v%d", m.Format, ShortcutFormatVersion)
	case m.Fingerprint != WorkflowFingerprint():
		return nil, "the built-in copy doesn't match this build's generator"
	case now.After(m.NotAfter.Add(-embeddedShortcutMargin)):
		return nil, fmt.Sprintf("the built-in copy's signature expires %s", m.NotAfter.Format("2006-01-02"))
	}
	return embeddedShortcutData, ""
}

// prepareShortcut puts the shortcut to install in dir, named "<name>.shortcut"
// (Shortcuts takes the shortcut's name from the file's): the pre-signed
// copy when it's usable, else one generated and signed now, in which case
// notice (if non-nil) hears why first.
func prepareShortcut(ctx context.Context, dir, name string, notice func(why string)) (string, error) {
	data, why := embeddedShortcut(time.Now())
	if data == nil {
		if notice != nil {
			notice(why)
		}
		return generateShortcut(ctx, dir, name)
	}
	path := filepath.Join(dir, sanitizeFilename(name)+".shortcut")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

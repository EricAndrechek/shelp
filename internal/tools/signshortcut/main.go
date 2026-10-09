// Command signshortcut regenerates internal/provider/signed/: the current
// shortcut format signed by Apple ("anyone" mode, so any Mac can import
// it) plus its metadata, which shelp embeds so `shelp setup shortcuts`
// needs no round trip to Apple's signing server. Run it from the repo
// root on macOS, via `make sign-shortcut`, whenever the generator changes
// (TestEmbeddedShortcutCurrent fails until you do) and before releasing a
// copy that's within a couple of months of expiring.
package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/provider"
)

func main() {
	out := flag.String("out", "internal/provider/signed", "directory to write shelp.shortcut and shelp.json into")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "signshortcut:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tmp, err := os.MkdirTemp("", "shelp-sign-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// The name inside doesn't matter: Shortcuts names an imported shortcut
	// after its file, and setup writes the file as "<name>.shortcut".
	path, err := provider.GenerateShortcut(ctx, tmp, "shelp")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	notAfter, err := chainExpiry(ctx, data, tmp)
	if err != nil {
		return fmt.Errorf("read the signature's certificates: %w", err)
	}
	meta, err := json.MarshalIndent(provider.SignedShortcutMeta{
		Format:      provider.ShortcutFormatVersion,
		Fingerprint: provider.WorkflowFingerprint(),
		SignedAt:    time.Now().UTC().Truncate(time.Second),
		NotAfter:    notAfter.UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "shelp.shortcut"), data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "shelp.json"), append(meta, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed format v%d, valid until %s -> %s\n", provider.ShortcutFormatVersion, notAfter.Format("2006-01-02"), out)
	return nil
}

// chainExpiry returns the earliest NotAfter in a signed shortcut's
// certificate chain. A signed .shortcut is an Apple Encrypted Archive:
// "AEA1", a 4-byte profile, a little-endian uint32 auth-data length, then
// the auth data -- a binary plist whose SigningCertificateChain is the
// DER certificates, leaf first. Go has no plist decoder, and plutil's JSON
// output can't hold binary data, so plutil turns it into an XML plist,
// where each certificate is a base64 <data> element.
func chainExpiry(ctx context.Context, data []byte, tmp string) (time.Time, error) {
	if len(data) < 12 || string(data[:4]) != "AEA1" {
		return time.Time{}, errors.New("not an AEA1 file")
	}
	n := binary.LittleEndian.Uint32(data[8:12])
	if uint64(12)+uint64(n) > uint64(len(data)) {
		return time.Time{}, errors.New("auth data runs past the end of the file")
	}
	authPath := filepath.Join(tmp, "auth.plist")
	if err := os.WriteFile(authPath, data[12:12+n], 0o600); err != nil {
		return time.Time{}, err
	}
	xmlPlist, err := exec.CommandContext(ctx, "plutil", "-convert", "xml1", "-o", "-", authPath).Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("plutil: %w", err)
	}
	// The auth plist is just {SigningCertificateChain: [<data>...]}, so
	// every <data> element is a certificate.
	var certs [][]byte
	dec := xml.NewDecoder(bytes.NewReader(xmlPlist))
	dec.Strict = false // the plist DOCTYPE names an external DTD
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return time.Time{}, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "data" {
			var b64 string
			if err := dec.DecodeElement(&b64, &se); err != nil {
				return time.Time{}, err
			}
			der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
			if err != nil {
				return time.Time{}, err
			}
			certs = append(certs, der)
		}
	}
	if len(certs) == 0 {
		return time.Time{}, errors.New("no certificates in the signature")
	}
	var earliest time.Time
	for _, der := range certs {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return time.Time{}, err
		}
		if earliest.IsZero() || c.NotAfter.Before(earliest) {
			earliest = c.NotAfter
		}
	}
	return earliest, nil
}

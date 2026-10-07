package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultMissingExplicitPathIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.toml")
	t.Setenv("SHELP_CONFIG", missing)
	_, err := LoadDefault()
	if err == nil || !strings.Contains(err.Error(), "SHELP_CONFIG") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("LoadDefault() error = %v, want one naming SHELP_CONFIG and %s", err, missing)
	}
}

func TestLoadDefaultMissingDefaultFileIsEmpty(t *testing.T) {
	t.Setenv("SHELP_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault() error = %v, want an empty config", err)
	}
	if len(c.Providers) != 0 || c.Template != "" {
		t.Errorf("LoadDefault() = %+v, want an empty config", c)
	}
}

func TestLoadDefaultReadsExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shelp.toml")
	if err := os.WriteFile(path, []byte(`providers = ["fm"]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELP_CONFIG", path)
	c, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 1 || c.Providers[0] != "fm" {
		t.Errorf("Providers = %v, want [fm]", c.Providers)
	}
}

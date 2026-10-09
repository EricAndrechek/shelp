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

func TestSetProviders(t *testing.T) {
	ids := []string{"fm", "claude:haiku"}
	want := `providers = ["fm", "claude:haiku"]`
	for name, c := range map[string]struct{ in, out string }{
		"empty":    {"", want + "\n"},
		"no-nl":    {"template = \"v4\"", "template = \"v4\"\n" + want + "\n"},
		"replace":  {"# hi\nproviders = [\"a\"] # old\ntemplate = \"v4\"\n", "# hi\n" + want + "\ntemplate = \"v4\"\n"},
		"multi":    {"providers = [\n  \"a\", # x ]\n  \"b\",\n]\ncooldown = \"1m\"\n[s]\nname = \"x\"\n", want + "\ncooldown = \"1m\"\n[s]\nname = \"x\"\n"},
		"insert":   {"template = \"v4\"\n\n# the shortcut\n[shortcuts]\nname = \"x\"\n", "template = \"v4\"\n\n" + want + "\n\n# the shortcut\n[shortcuts]\nname = \"x\"\n"},
		"in-table": {"[shell]\nproviders = [\"x\"]\n", want + "\n\n[shell]\nproviders = [\"x\"]\n"},
		"multictx": {"context = [\n  \"os\",\n]\n[a]\n", "context = [\n  \"os\",\n]\n" + want + "\n\n[a]\n"},
	} {
		if got := SetProviders(c.in, ids); got != c.out {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.out)
		}
		if _, err := Parse(SetProviders(c.in, ids)); err != nil {
			t.Errorf("%s: result does not parse: %v", name, err)
		}
	}
}

func TestSaveProvidersCreates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "config.toml")
	if err := SaveProviders(p, []string{"fm"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || len(c.Providers) != 1 || c.Providers[0] != "fm" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

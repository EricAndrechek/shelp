// Package config loads shelp's TOML config file.
//
// Top-level keys are shelp-wide settings. Each provider family owns one
// table (e.g. [shortcuts], [openai], [agentcli]) and decodes it itself via
// Section, so families can be added without touching this package.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the parsed config file. The zero value is a valid default.
type Config struct {
	// Providers is the preference/fallback chain of provider IDs. Empty
	// means "use the built-in auto order".
	Providers []string `toml:"providers"`
	// Template names the prompt template (see internal/prompt). Empty means
	// the default template.
	Template string `toml:"template"`
	// Context lists what to tell the model about the environment:
	// "os", "shell", "cwd", "tools". Empty means prompt.DefaultContext.
	Context []string `toml:"context"`

	meta     toml.MetaData
	sections map[string]toml.Primitive
}

// Path returns the config file location: $SHELP_CONFIG, else
// $XDG_CONFIG_HOME/shelp/config.toml, else ~/.config/shelp/config.toml.
func Path() string {
	if p := os.Getenv("SHELP_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "shelp", "config.toml")
}

// Load reads the config at path. A missing file yields an empty Config.
func Load(path string) (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(string(data))
}

// Parse parses config TOML from a string.
func Parse(data string) (*Config, error) {
	c := &Config{}
	var raw map[string]toml.Primitive
	meta, err := toml.Decode(data, &raw)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.meta, c.sections = meta, map[string]toml.Primitive{}
	for k, v := range raw {
		var err error
		switch k {
		case "providers":
			err = meta.PrimitiveDecode(v, &c.Providers)
		case "template":
			err = meta.PrimitiveDecode(v, &c.Template)
		case "context":
			err = meta.PrimitiveDecode(v, &c.Context)
		default:
			c.sections[k] = v
		}
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", k, err)
		}
	}
	return c, nil
}

// Section decodes the [name] table into v. It is a no-op (v untouched) when
// the table is absent.
func (c *Config) Section(name string, v any) error {
	if c == nil || c.sections == nil {
		return nil
	}
	p, ok := c.sections[name]
	if !ok {
		return nil
	}
	if err := c.meta.PrimitiveDecode(p, v); err != nil {
		return fmt.Errorf("config [%s]: %w", name, err)
	}
	return nil
}

package shellinit

// Config is the decoded [shell] table, read by `shelp init` (see
// cmd/shelp/main.go's runInit):
//
//	[shell]
//	line_mode = true   # raw-line Enter interception; default true
type Config struct {
	// LineMode is a pointer so an absent key can be told apart from an
	// explicit `line_mode = false`: nil means "unset" (default on).
	LineMode *bool `toml:"line_mode"`
}

// LineModeEnabled resolves the effective line-mode setting: the
// --no-line-mode flag wins over config, config wins over the built-in
// default (on).
func (c Config) LineModeEnabled(noLineModeFlag bool) bool {
	if noLineModeFlag {
		return false
	}
	if c.LineMode != nil {
		return *c.LineMode
	}
	return true
}

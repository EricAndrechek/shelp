package shellinit

import "testing"

func boolPtr(b bool) *bool { return &b }

func TestConfig_LineModeEnabled(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		noLineFlag bool
		want       bool
	}{
		{"default (no config, no flag)", Config{}, false, true},
		{"flag wins even with config true", Config{LineMode: boolPtr(true)}, true, false},
		{"config false, no flag", Config{LineMode: boolPtr(false)}, false, false},
		{"config true, no flag", Config{LineMode: boolPtr(true)}, false, true},
		{"flag true, config unset", Config{}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.cfg.LineModeEnabled(c.noLineFlag)
			if got != c.want {
				t.Errorf("LineModeEnabled(%v) with cfg=%+v = %v, want %v", c.noLineFlag, c.cfg, got, c.want)
			}
		})
	}
}

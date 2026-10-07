package main

import (
	"testing"

	"github.com/ericandrechek/shelp/internal/shellinit"
	"github.com/ericandrechek/shelp/internal/style"
)

func TestSetupShellMessageColors(t *testing.T) {
	on := style.Forced(style.Color16)
	cases := []struct {
		action string
		dry    bool
		want   func(string) string
	}{
		{"added", false, on.OK}, {"unchanged", false, on.OK}, {"active", false, on.OK},
		{"manual", false, on.Warn}, {"already-manual", false, on.Warn},
		{"added", true, on.Dim}, {"updated", true, on.Dim},
	}
	for _, c := range cases {
		res := shellinit.SetupResult{Action: c.action, Message: "msg\n  line"}
		if got := setupShellMessage(on, res, c.dry); got != c.want("msg\n  line") {
			t.Errorf("%s dry=%v: got %q", c.action, c.dry, got)
		}
		if got := setupShellMessage(style.Plain(), res, c.dry); got != res.Message {
			t.Errorf("plain %s: got %q", c.action, got)
		}
	}
}

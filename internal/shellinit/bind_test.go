package shellinit

import "testing"

func TestConvertBind(t *testing.T) {
	cases := []struct {
		shell, in, want string
		wantErr         bool
	}{
		// Empty spec: no binding.
		{"zsh", "", "", false},
		{"bash", "", "", false},
		{"fish", "", "", false},

		// zsh: bindkey parses caret/\e notation itself; pass through.
		{"zsh", "^G", "^G", false},
		{"zsh", "\\eg", "\\eg", false},
		{"zsh", "^[[A", "^[[A", false},

		// bash: caret notation converts to \C-x; other forms pass through.
		{"bash", "^G", `\C-g`, false},
		{"bash", "^g", `\C-g`, false},
		{"bash", "^A", `\C-a`, false},
		{"bash", "\\eg", "\\eg", false},
		{"bash", "\\C-g", "\\C-g", false},

		// fish: caret notation converts to \cx; other forms pass through.
		{"fish", "^G", `\cg`, false},
		{"fish", "^g", `\cg`, false},
		{"fish", "\\eg", "\\eg", false},
		{"fish", "\\cg", "\\cg", false},

		// unknown shell
		{"powershell", "^G", "", true},
	}
	for _, c := range cases {
		got, err := ConvertBind(c.shell, c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ConvertBind(%q, %q): want error, got nil", c.shell, c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ConvertBind(%q, %q): unexpected error: %v", c.shell, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ConvertBind(%q, %q) = %q, want %q", c.shell, c.in, got, c.want)
		}
	}
}

func TestConvertBind_Whitespace(t *testing.T) {
	got, err := ConvertBind("bash", "  ^G  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `\C-g` {
		t.Errorf("got %q, want %q", got, `\C-g`)
	}
}

package clean

import "testing"

func TestCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"find ~ -type f -size +100M\n", "find ~ -type f -size +100M"},
		{"```zsh\nfind . -name x\n```", "find . -name x"},
		{"\n\n```\nls -la\n```\n", "ls -la"},
		{"`ls -la`", "ls -la"},
		{"echo `date`", "echo `date`"},
		{"$ git status", "git status"},
		{"% git status", "git status"},
		{"for f in *.jpeg; do\n  mv \"$f\" x\ndone", "for f in *.jpeg; do\n  mv \"$f\" x\ndone"},
		{"  lsof -i :8080  ", "lsof -i :8080"},
		{"Command: ls -t | head -n 3", "ls -t | head -n 3"},
		{"command: `git status`", "git status"},
	}
	for _, c := range cases {
		if got := Command(c.in); got != c.want {
			t.Errorf("Command(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

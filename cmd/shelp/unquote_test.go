package main

import (
	"strings"
	"testing"
)

func TestUnquoteWord(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`du -sh *`, `du -sh *`},
		{`'du -sh * | sort -h'`, `du -sh * | sort -h`},
		{`  'du -sh'  `, `du -sh`},
		{`"du -sh * | sort -h"`, `du -sh * | sort -h`},
		{`"echo \"hi\" \\ \$HOME \` + "`" + `x\` + "`" + `"`, `echo "hi" \ $HOME ` + "`" + `x` + "`"},
		{`"keep \n and \a"`, `keep \n and \a`},
		{`echo 'a' 'b'`, `echo 'a' 'b'`},
		{`'a' | b`, `'a' | b`},
		{`'a' | 'b'`, `'a' | 'b'`},
		{`"a" "b"`, `"a" "b"`},
		{`"a" | "b"`, `"a" | "b"`},
		{`"say "hi""`, `"say "hi""`},
		{`'it's'`, `'it's'`},
		{`'`, `'`},
		{`''`, `''`},
		{`""`, `""`},
		{`'a"`, `'a"`},
		{`'$HOME'`, `$HOME`},
		{`"trailing \"`, `"trailing \"`},
	} {
		if got := unquoteWord(c.in); got != c.want {
			t.Errorf("unquoteWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExplainStripsQuotedInput(t *testing.T) {
	fakeBackend(t, "Shows disk usage.", "")
	if _, _, err := runOut(t, "explain", `'du -sh * | sort -h'`); err != nil {
		t.Fatal(err)
	}
	recs := readHistory(t)
	if len(recs) != 1 || recs[0].Input != "du -sh * | sort -h" {
		t.Fatalf("records = %+v", recs)
	}
	// on a terminal the header and the man links use the stripped command
	setColorEnv(t, "1")
	out, _, err := runOut(t, "explain", `'du -sh * | sort -h'`)
	if err != nil {
		t.Fatal(err)
	}
	plain := strip(out)
	if !strings.HasPrefix(plain, "$ du -sh * | sort -h\n") || !strings.Contains(plain, "man: du · sort") {
		t.Errorf("explain output:\n%s", plain)
	}
	// suggest is not touched: a task may legitimately start with a quote
	if _, _, err := runOut(t, `'quoted task'`); err != nil {
		t.Fatal(err)
	}
	if r := readHistory(t); r[len(r)-1].Input != `'quoted task'` {
		t.Errorf("suggest input = %q", r[len(r)-1].Input)
	}
}

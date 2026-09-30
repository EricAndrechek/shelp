package bench

import "testing"

func TestNormalizeSuggest(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  find  ~   -type f  ", "find ~ -type f"},
		{"find ~ -type f;", "find ~ -type f"},
		{"echo 'a   b'", "echo 'a   b'"},         // whitespace preserved inside single quotes
		{`echo "a   b"`, `echo "a   b"`},         // and inside double quotes
		{"echo   'a   b'   c", "echo 'a   b' c"}, // collapse outside, preserve inside
		{"", ""},
		{"echo hi;", "echo hi"},
	}
	for _, c := range cases {
		if got := NormalizeSuggest(c.in); got != c.want {
			t.Errorf("NormalizeSuggest(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeExplain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  lists   files  ", "lists files"},
		{"a\n\nb\tc", "a b c"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeExplain(c.in); got != c.want {
			t.Errorf("NormalizeExplain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGradeKeyStableAcrossWhitespace(t *testing.T) {
	k1 := GradeKey("c1", "zsh", "macos", "suggest", "find ~ -type f")
	k2 := GradeKey("c1", "zsh", "macos", "suggest", "  find   ~  -type f  ;")
	if k1 != k2 {
		t.Errorf("GradeKey should match after normalization: %q != %q", k1, k2)
	}
}

func TestGradeKeyDiffersByContext(t *testing.T) {
	base := GradeKey("c1", "zsh", "macos", "suggest", "find ~ -type f")
	byShell := GradeKey("c1", "bash", "macos", "suggest", "find ~ -type f")
	byOS := GradeKey("c1", "zsh", "linux", "suggest", "find ~ -type f")
	byCase := GradeKey("c2", "zsh", "macos", "suggest", "find ~ -type f")
	for _, k := range []string{byShell, byOS, byCase} {
		if k == base {
			t.Errorf("GradeKey collided when it should differ: %q", k)
		}
	}
}

func TestGradeKeyPreservesQuotedWhitespace(t *testing.T) {
	// Two answers differing only in whitespace INSIDE quotes must produce
	// different grade keys: that whitespace is part of the command.
	k1 := GradeKey("c1", "zsh", "macos", "suggest", "echo 'a b'")
	k2 := GradeKey("c1", "zsh", "macos", "suggest", "echo 'a  b'")
	if k1 == k2 {
		t.Error("GradeKey must not collapse whitespace inside quotes")
	}
}

func TestMatchesReference(t *testing.T) {
	refs := []string{"find ~ -type f -size +100M"}
	if !MatchesReference("suggest", "  find   ~ -type f -size +100M ; ", refs) {
		t.Error("expected normalized match against reference")
	}
	if MatchesReference("suggest", "find ~ -type f -size +100m", refs) {
		t.Error("case-sensitive suffix should not match")
	}
}

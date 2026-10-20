package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestQuoteRoundTrips(t *testing.T) {
	for _, s := range []string{"", "plain", `a "quoted" \ back`, "tab\there", "new\nline", "bell\x07", "Haiku via OpenRouter · ünïcode →", "del\x7f"} {
		var v struct{ K string }
		if _, err := toml.Decode("K = "+Quote(s), &v); err != nil {
			t.Errorf("Quote(%q) = %s: %v", s, Quote(s), err)
			continue
		}
		if v.K != s {
			t.Errorf("Quote(%q) decoded to %q", s, v.K)
		}
	}
}

const tablesSrc = `providers = ["fm"]

[shell]
bind = "^G"

# my router
[[openai.endpoint]]
name = "openrouter"
preset = "openrouter"
models = [
  "a/b",   # [not a header]
]

[openai.endpoint.labels]
"a/b" = "AB"

# the work one
[[openai.endpoint]]
name = "work"
base_url = "https://llm.example.com/v1"
models = ["m"]
notes = """
[[openai.endpoint]]
"""

[[openai.local]]
name = "fm-serve"
base_url = "http://127.0.0.1:11535/v1"
`

func TestArrayTableBlocks(t *testing.T) {
	bs := ArrayTableBlocks(tablesSrc, "openai.endpoint")
	if len(bs) != 2 {
		t.Fatalf("got %d blocks: %+v", len(bs), bs)
	}
	if !strings.HasPrefix(bs[0].Text, "# my router\n[[openai.endpoint]]") || !strings.HasSuffix(bs[0].Text, "\"a/b\" = \"AB\"\n") {
		t.Errorf("first block:\n%s", bs[0].Text)
	}
	if strings.Contains(bs[0].Text, "the work one") {
		t.Error("the next header's comment went with the first block")
	}
	if !strings.HasPrefix(bs[1].Text, "# the work one\n") || !strings.HasSuffix(bs[1].Text, "\"\"\"\n") {
		t.Errorf("second block:\n%s", bs[1].Text)
	}
	// Each block parses on its own.
	for _, b := range bs {
		var v map[string]any
		if _, err := toml.Decode(b.Text, &v); err != nil {
			t.Errorf("block does not parse alone: %v\n%s", err, b.Text)
		}
	}
	if ls := ArrayTableBlocks(tablesSrc, "openai.local"); len(ls) != 1 || !strings.Contains(ls[0].Text, "fm-serve") {
		t.Errorf("local blocks: %+v", ls)
	}
	if ArrayTableBlocks("[[ openai.endpoint ]]\nname = \"x\"\n", "openai.endpoint") == nil {
		t.Error("spaced header not found")
	}
}

func TestRemoveBlock(t *testing.T) {
	bs := ArrayTableBlocks(tablesSrc, "openai.endpoint")
	out := RemoveBlock(tablesSrc, bs[0])
	if strings.Contains(out, "openrouter") || strings.Contains(out, "my router") || strings.Contains(out, "AB") {
		t.Errorf("not removed:\n%s", out)
	}
	if !strings.Contains(out, "bind = \"^G\"\n\n# the work one\n[[openai.endpoint]]") {
		t.Errorf("spacing:\n%s", out)
	}
	// The last block, then the only one.
	ls := ArrayTableBlocks(out, "openai.local")
	out2 := RemoveBlock(out, ls[0])
	if strings.Contains(out2, "fm-serve") || !strings.HasSuffix(out2, "\"\"\"\n") {
		t.Errorf("last block removal:\n%q", out2)
	}
	one := "[[openai.local]]\nname = \"x\"\n"
	if got := RemoveBlock(one, ArrayTableBlocks(one, "openai.local")[0]); got != "" {
		t.Errorf("only block: %q", got)
	}
	for _, s := range []string{out, out2} {
		var v map[string]any
		if _, err := toml.Decode(s, &v); err != nil {
			t.Errorf("result does not parse: %v\n%s", err, s)
		}
	}
}

func TestAppendBlock(t *testing.T) {
	b := "[[openai.local]]\nname = \"x\"\n"
	for src, want := range map[string]string{
		"":                        b,
		"\n":                      b,
		"a = 1":                   "a = 1\n\n" + b,
		"a = 1\n":                 "a = 1\n\n" + b,
		"a = 1\n\n":               "a = 1\n\n" + b,
		"[shell]\nbind = \"x\"\n": "[shell]\nbind = \"x\"\n\n" + b,
	} {
		if got := AppendBlock(src, b); got != want {
			t.Errorf("AppendBlock(%q) = %q, want %q", src, got, want)
		}
	}
	if got := AppendBlock("", "[[x]]"); got != "[[x]]\n" {
		t.Errorf("no trailing newline: %q", got)
	}
}

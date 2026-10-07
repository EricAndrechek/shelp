package markdown

import (
	"reflect"
	"testing"
)

func TestParseBlock(t *testing.T) {
	cases := []struct {
		in   string
		want Block
	}{
		{"- `-h` — sizes", Block{Kind: Bullet, Marker: "-", Text: "`-h` — sizes"}},
		{"* star", Block{Kind: Bullet, Marker: "*", Text: "star"}},
		{"  - nested", Block{Kind: Bullet, Indent: 2, Marker: "-", Text: "nested"}},
		{"12. twelve", Block{Kind: Ordered, Marker: "12.", Text: "twelve"}},
		{"## Title ##", Block{Kind: Heading, Level: 2, Text: "Title"}},
		{"#nospace", Block{Kind: Paragraph, Text: "#nospace"}},
		{"-flag is not a bullet", Block{Kind: Paragraph, Text: "-flag is not a bullet"}},
		{"3.5 GB", Block{Kind: Paragraph, Text: "3.5 GB"}},
		{"```sh", Block{Kind: Fence, Text: "```sh"}},
		{"", Block{Kind: Paragraph}},
	}
	for _, c := range cases {
		if got := ParseBlock(c.in); got != c.want {
			t.Errorf("ParseBlock(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseInline(t *testing.T) {
	cases := []struct {
		in   string
		want []Span
	}{
		{"plain text", []Span{{Text: "plain text"}}},
		{"use `du -sh` now", []Span{{Text: "use "}, {Text: "du -sh", Code: true}, {Text: " now"}}},
		{"a **bold** b", []Span{{Text: "a "}, {Text: "bold", Bold: true}, {Text: " b"}}},
		{"**Warning:** x", []Span{{Text: "Warning:", Bold: true}, {Text: " x"}}},
		{"an *em* and _em2_", []Span{{Text: "an "}, {Text: "em", Em: true}, {Text: " and "}, {Text: "em2", Em: true}}},
		{"**bold `code`**", []Span{{Text: "bold ", Bold: true}, {Text: "code", Code: true, Bold: true}}},
		{"see [docs](https://x.io/a) ok", []Span{{Text: "see "}, {Text: "docs", URL: "https://x.io/a"}, {Text: " ok"}}},
		{"[bad](javascript:alert) ok", []Span{{Text: "bad ok"}}},
		{"snake_case_name", []Span{{Text: "snake_case_name"}}},
		{"glob * and * again", []Span{{Text: "glob * and * again"}}},
		{"unclosed **bold and `tick", []Span{{Text: "unclosed **bold and `tick"}}},
		{"naïve **café** ok", []Span{{Text: "naïve "}, {Text: "café", Bold: true}, {Text: " ok"}}},
	}
	for _, c := range cases {
		if got := ParseInline(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseInline(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

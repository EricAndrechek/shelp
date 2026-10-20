package term

import (
	"reflect"
	"testing"
)

func feed(chunks ...string) []Key {
	var d Decoder
	var out []Key
	for _, c := range chunks {
		if c == "" { // an empty chunk is a quiet read (the timeout)
			out = append(out, d.Timeout()...)
			continue
		}
		out = append(out, d.Feed([]byte(c))...)
	}
	return out
}

func TestDecoder(t *testing.T) {
	up, down := Key{Code: KeyUp}, Key{Code: KeyDown}
	sUp, sDown := Key{Code: KeyUp, Shift: true}, Key{Code: KeyDown, Shift: true}
	esc := Key{Code: KeyEsc}
	r := func(c rune) Key { return Key{Code: KeyRune, Rune: c} }
	for _, c := range []struct {
		name   string
		chunks []string
		want   []Key
	}{
		{"letters", []string{"jkq"}, []Key{r('j'), r('k'), r('q')}},
		{"arrows", []string{"\x1b[A\x1b[B\x1b[C\x1b[D"}, []Key{up, down, {Code: KeyRight}, {Code: KeyLeft}}},
		{"application mode arrows", []string{"\x1bOA\x1bOB"}, []Key{up, down}},
		{"shift arrows (xterm)", []string{"\x1b[1;2A\x1b[1;2B"}, []Key{sUp, sDown}},
		{"option arrows count as shift", []string{"\x1b[1;3A\x1b[1;4B"}, []Key{sUp, sDown}},
		{"ctrl arrows are plain", []string{"\x1b[1;5A"}, []Key{up}},
		{"rxvt shift arrows", []string{"\x1b[a\x1b[b"}, []Key{sUp, sDown}},
		{"esc esc arrow (alt)", []string{"\x1b\x1b[A"}, []Key{{Code: KeyUp, Shift: true, Alt: true}}},
		{"lone esc needs the timeout", []string{"\x1b"}, nil},
		{"lone esc then quiet", []string{"\x1b", ""}, []Key{esc}},
		{"esc then a split sequence is not esc", []string{"\x1b", "[A"}, []Key{up}},
		{"split csi", []string{"\x1b[1;", "2A"}, []Key{sUp}},
		{"cut-off sequence is dropped as esc", []string{"\x1b[1;", ""}, []Key{esc}},
		{"double esc", []string{"\x1b\x1b", ""}, []Key{esc, esc}},
		{"alt letter", []string{"\x1bq"}, []Key{{Code: KeyRune, Rune: 'q', Alt: true}}},
		{"enter cr and lf", []string{"\r\n"}, []Key{{Code: KeyEnter}, {Code: KeyEnter}}},
		{"backspace both ways", []string{"\x7f\x08"}, []Key{{Code: KeyBackspace}, {Code: KeyBackspace}}},
		{"ctrl-c", []string{"\x03"}, []Key{CtrlC}},
		{"ctrl-u", []string{"\x15"}, []Key{{Code: KeyCtrl, Rune: 'u'}}},
		{"tab and backtab", []string{"\t\x1b[Z"}, []Key{{Code: KeyTab}, {Code: KeyBackTab}}},
		{"delete home end pages", []string{"\x1b[3~\x1b[H\x1b[F\x1b[1~\x1b[4~\x1b[5~\x1b[6~"},
			[]Key{{Code: KeyDelete}, {Code: KeyHome}, {Code: KeyEnd}, {Code: KeyHome}, {Code: KeyEnd}, {Code: KeyPgUp}, {Code: KeyPgDn}}},
		{"unknown csi is skipped whole", []string{"\x1b[200~x"}, []Key{{Code: KeyUnknown}, r('x')}},
		{"utf-8", []string{"é→"}, []Key{r('é'), r('→')}},
		{"utf-8 split across reads", []string{"\xc3", "\xa9"}, []Key{r('é')}},
		{"space and plus", []string{" +-"}, []Key{r(' '), r('+'), r('-')}},
	} {
		if got := feed(c.chunks...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q -> %+v, want %+v", c.name, c.chunks, got, c.want)
		}
	}
}

func TestKeyHelpers(t *testing.T) {
	if !(Key{Code: KeyRune, Rune: 'q'}).Is('q') || (Key{Code: KeyRune, Rune: 'q', Alt: true}).Is('q') {
		t.Error("Is")
	}
	if !CtrlC.IsCtrl('c') {
		t.Error("IsCtrl")
	}
}

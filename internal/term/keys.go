// Package term is the little terminal layer shelp's interactive screens
// need: raw mode (stdlib syscall ioctls, no x/term), the window size, and a
// decoder that turns the bytes a terminal sends into keys.
//
// The decoder is pure (Feed bytes, get keys) so it is tested without a
// terminal. Raw mode reads with a 100ms timeout (VMIN=0, VTIME=1): that is
// how a lone Esc is told apart from the start of an escape sequence (the
// input goes quiet after it; Timeout then reports it), and how a reader
// loop notices it should stop without leaving a read blocked on stdin.
package term

import "unicode/utf8"

// Code is a key's kind.
type Code int

const (
	KeyUnknown Code = iota
	KeyRune         // a printable character, in Key.Rune
	KeyEnter
	KeyEsc
	KeyBackspace
	KeyDelete
	KeyTab
	KeyBackTab // Shift+Tab
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyCtrl // Ctrl+letter, the letter (lowercase) in Key.Rune; Ctrl-C is {KeyCtrl, 'c'}
)

// Key is one decoded keypress. Shift is set for Shift+arrow (and for
// Alt/Option+arrow, which some macOS terminals send instead); Alt for a
// key that came prefixed with ESC (Option as Meta).
type Key struct {
	Code  Code
	Rune  rune
	Shift bool
	Alt   bool
}

// Is reports whether k is the plain printable character r.
func (k Key) Is(r rune) bool { return k.Code == KeyRune && k.Rune == r && !k.Alt }

// IsCtrl reports whether k is Ctrl+letter (letter lowercase).
func (k Key) IsCtrl(letter rune) bool { return k.Code == KeyCtrl && k.Rune == letter }

// CtrlC is the key raw mode delivers for Ctrl-C (ISIG is off, so no SIGINT).
var CtrlC = Key{Code: KeyCtrl, Rune: 'c'}

// Decoder turns terminal input bytes into keys. An escape sequence or a
// UTF-8 character split across reads stays buffered until the rest
// arrives, or until Timeout says the input went quiet.
type Decoder struct{ buf []byte }

// Feed adds input and returns every key it completes.
func (d *Decoder) Feed(b []byte) []Key {
	d.buf = append(d.buf, b...)
	var out []Key
	for len(d.buf) > 0 {
		k, n := decode(d.buf)
		if n == 0 { // incomplete: wait for more, or for Timeout
			break
		}
		d.buf = d.buf[n:]
		out = append(out, k)
	}
	return out
}

// Pending reports whether bytes are buffered (a possible lone Esc).
func (d *Decoder) Pending() bool { return len(d.buf) > 0 }

// Timeout is called when no input arrived for a moment: a buffered lone
// ESC was the Esc key. Anything else still buffered (a cut-off sequence)
// is dropped after an Esc, so a stray fragment never types characters.
func (d *Decoder) Timeout() []Key {
	if len(d.buf) == 0 {
		return nil
	}
	var out []Key
	if d.buf[0] == 0x1b {
		out = append(out, Key{Code: KeyEsc})
		if len(d.buf) == 2 && d.buf[1] == 0x1b { // Esc pressed twice
			out = append(out, Key{Code: KeyEsc})
		}
	}
	d.buf = d.buf[:0]
	return out
}

// decode reads one key from the front of b; n == 0 means b holds only the
// start of one.
func decode(b []byte) (Key, int) {
	c := b[0]
	switch {
	case c == 0x1b:
		return decodeEsc(b)
	case c == '\r' || c == '\n':
		return Key{Code: KeyEnter}, 1
	case c == '\t':
		return Key{Code: KeyTab}, 1
	case c == 0x7f || c == 0x08:
		return Key{Code: KeyBackspace}, 1
	case c == 0:
		return Key{Code: KeyUnknown}, 1
	case c < 0x20:
		return Key{Code: KeyCtrl, Rune: rune(c) + 'a' - 1}, 1
	case c < 0x80:
		return Key{Code: KeyRune, Rune: rune(c)}, 1
	}
	if !utf8.FullRune(b) {
		return Key{}, 0
	}
	r, n := utf8.DecodeRune(b)
	if r == utf8.RuneError {
		return Key{Code: KeyUnknown}, n
	}
	return Key{Code: KeyRune, Rune: r}, n
}

func decodeEsc(b []byte) (Key, int) {
	if len(b) == 1 {
		return Key{}, 0 // a lone ESC so far: Timeout decides
	}
	switch b[1] {
	case '[':
		return decodeCSI(b)
	case 'O':
		if len(b) < 3 {
			return Key{}, 0
		}
		return ss3(b[2]), 3
	case 0x1b:
		// ESC ESC [ A: Alt+arrow in some terminals; a plain double Esc is
		// two Escs.
		if len(b) == 2 {
			return Key{}, 0
		}
		if b[2] == '[' || b[2] == 'O' {
			k, n := decodeEsc(b[1:])
			if n == 0 {
				return Key{}, 0
			}
			k.Alt = true
			if k.Code == KeyUp || k.Code == KeyDown {
				k.Shift = true
			}
			return k, n + 1
		}
		return Key{Code: KeyEsc}, 1
	}
	// ESC + key: Alt (Meta) + that key.
	k, n := decode(b[1:])
	if n == 0 {
		return Key{}, 0
	}
	k.Alt = true
	return k, n + 1
}

// decodeCSI reads ESC [ params final.
func decodeCSI(b []byte) (Key, int) {
	i := 2
	for i < len(b) && b[i] >= 0x30 && b[i] <= 0x3f { // parameter bytes
		i++
	}
	for i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f { // intermediate bytes
		i++
	}
	if i >= len(b) {
		return Key{}, 0
	}
	final := b[i]
	if final < 0x40 || final > 0x7e { // not a CSI after all
		return Key{Code: KeyUnknown}, i + 1
	}
	params := string(b[2:i])
	n := i + 1
	first, mod := params, ""
	for j := 0; j < len(params); j++ {
		if params[j] == ';' {
			first, mod = params[:j], params[j+1:]
			break
		}
	}
	// Modifier 2 = Shift, 3 = Alt, 4 = Shift+Alt: all move a chain entry.
	shifted := mod == "2" || mod == "3" || mod == "4"
	switch final {
	case 'A', 'B', 'C', 'D':
		k := Key{Code: map[byte]Code{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft}[final]}
		k.Shift = shifted
		return k, n
	case 'a': // rxvt Shift+Up
		return Key{Code: KeyUp, Shift: true}, n
	case 'b': // rxvt Shift+Down
		return Key{Code: KeyDown, Shift: true}, n
	case 'H':
		return Key{Code: KeyHome}, n
	case 'F':
		return Key{Code: KeyEnd}, n
	case 'Z':
		return Key{Code: KeyBackTab}, n
	case '~':
		switch first {
		case "1", "7":
			return Key{Code: KeyHome}, n
		case "4", "8":
			return Key{Code: KeyEnd}, n
		case "3":
			return Key{Code: KeyDelete}, n
		case "5":
			return Key{Code: KeyPgUp}, n
		case "6":
			return Key{Code: KeyPgDn}, n
		}
	}
	return Key{Code: KeyUnknown}, n
}

// ss3 maps ESC O <c> (application cursor/keypad mode).
func ss3(c byte) Key {
	switch c {
	case 'A':
		return Key{Code: KeyUp}
	case 'B':
		return Key{Code: KeyDown}
	case 'C':
		return Key{Code: KeyRight}
	case 'D':
		return Key{Code: KeyLeft}
	case 'H':
		return Key{Code: KeyHome}
	case 'F':
		return Key{Code: KeyEnd}
	case 'M':
		return Key{Code: KeyEnter}
	}
	return Key{Code: KeyUnknown}
}

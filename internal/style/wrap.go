package style

import "strings"

// Wrap breaks s (which may carry color codes) at spaces into lines of at
// most width visible columns; lines after the first start with indent. A
// color open at a break is closed at the end of its line and opened again
// after the indent. A word longer than a line stays whole (Truncate can cut
// it). width < 1, or an s that fits, is returned as is.
func Wrap(s string, width int, indent string) []string {
	if width < 1 || Width(s) <= width {
		return []string{s}
	}
	var out, open []string
	line, lineW, hasWord := "", 0, false
	for i, t := range strings.Split(s, " ") {
		tw := Width(t)
		switch {
		case hasWord && tw > 0 && lineW+1+tw > width:
			if len(open) > 0 {
				line += "\x1b[0m"
			}
			out = append(out, line)
			line, lineW = indent+strings.Join(open, "")+t, Width(indent)+tw
		case i > 0:
			line, lineW = line+" "+t, lineW+1+tw
		default:
			line, lineW = t, tw
		}
		hasWord = hasWord || tw > 0
		for _, code := range sgrRE.FindAllString(t, -1) {
			if code == "\x1b[0m" || code == "\x1b[m" {
				open = open[:0]
			} else {
				open = append(open, code)
			}
		}
	}
	return append(out, line)
}

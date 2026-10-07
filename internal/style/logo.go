package style

import "strings"

// The logo is the word "shelp" in a small hand-drawn pixel font, drawn
// with Braille characters (U+2800-U+28FF: each one a 2-wide, 4-tall grid
// of dots) the way Apple's fm CLI draws its logo, and colored with the
// brand gradient one terminal column at a time.
//
// Glyphs are 16 dots tall, so the logo is 4 lines: rows 0-3 hold the
// ascenders (h, l), rows 4-13 the x-height (baseline under row 13), and
// rows 14-15 p's descender. Strokes are 2 dots thick. Two rules keep the
// letters solid where a terminal's line spacing or letter spacing would
// otherwise show through:
//   - every horizontal stroke sits inside one line (rows 4-5, 8-9 and
//     12-13), never across the boundary between two lines;
//   - every vertical stem starts on an even dot column, so it fills whole
//     Braille cells ("⣿") rather than straddling two.

const (
	logoText   = "shelp"
	logoHeight = 16 // dots; a multiple of 4
	logoGap    = 2  // blank dot columns between letters
)

// logoGlyph is r's bitmap: logoHeight rows of equal width, '#' a dot and
// '.' blank. It knows only the letters of logoText.
func logoGlyph(r rune) [logoHeight]string {
	switch r {
	case 's':
		return [logoHeight]string{
			"........",
			"........",
			"........",
			"........",
			"..######",
			".#######",
			"##......",
			"###.....",
			".#####..",
			"..#####.",
			".....###",
			"......##",
			"#######.",
			"######..",
			"........",
			"........",
		}
	case 'h':
		return [logoHeight]string{
			"##......",
			"##......",
			"##......",
			"##......",
			"##.####.",
			"########",
			"###...##",
			"##....##",
			"##....##",
			"##....##",
			"##....##",
			"##....##",
			"##....##",
			"##....##",
			"........",
			"........",
		}
	case 'e':
		return [logoHeight]string{
			"........",
			"........",
			"........",
			"........",
			"..####..",
			".######.",
			"##....##",
			"##....##",
			"########",
			"########",
			"##......",
			"##......",
			".#######",
			"..######",
			"........",
			"........",
		}
	case 'l':
		// The blank last column gives the two stems of "lp" more air
		// than a stem beside a bowl gets, and keeps p's stem on an even
		// column.
		return [logoHeight]string{
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"##..",
			"###.",
			".##.",
			"....",
			"....",
		}
	case 'p':
		return [logoHeight]string{
			"........",
			"........",
			"........",
			"........",
			"##.####.",
			"########",
			"###...##",
			"##....##",
			"##....##",
			"##....##",
			"##....##",
			"###...##",
			"########",
			"##.####.",
			"##......",
			"##......",
		}
	}
	panic("style: no logo glyph for " + string(r))
}

// logoBitmap lays out logoText, padded on the right to whole Braille
// cells.
func logoBitmap() [logoHeight]string {
	var rows [logoHeight]string
	for i, r := range logoText {
		g := logoGlyph(r)
		for y := range rows {
			if i > 0 {
				rows[y] += strings.Repeat(".", logoGap)
			}
			rows[y] += g[y]
		}
	}
	if len(rows[0])%2 == 1 {
		for y := range rows {
			rows[y] += "."
		}
	}
	return rows
}

// brailleDot is the bit for the dot at column x (0-1), row y (0-3) of a
// Braille cell; the cell's character is U+2800 plus its dots' bits.
var brailleDot = [4][2]rune{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// Logo is shelp's logo: the word "shelp" in Braille, logoHeight/4 lines
// joined by "\n" (no trailing newline), every line the same width. p
// colors each column along the brand gradient, from BrandStart at the
// left to BrandEnd at the right; blank cells are U+2800 and left
// uncolored. With a plain Palette it is the same Braille, uncolored.
func Logo(p Palette) string {
	bm := logoBitmap()
	cols := len(bm[0]) / 2
	lines := make([]string, logoHeight/4)
	for cy := range lines {
		var b strings.Builder
		for cx := 0; cx < cols; cx++ {
			cell := rune(0x2800)
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if bm[4*cy+dy][2*cx+dx] == '#' {
						cell |= brailleDot[dy][dx]
					}
				}
			}
			if cell == 0x2800 {
				b.WriteRune(cell)
				continue
			}
			b.WriteString(p.RGB(Lerp(BrandStart, BrandEnd, float64(cx)/float64(cols-1)), string(cell)))
		}
		lines[cy] = b.String()
	}
	return strings.Join(lines, "\n")
}

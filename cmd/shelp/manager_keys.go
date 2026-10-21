package main

import (
	"slices"
	"strings"

	"github.com/ericandrechek/shelp/internal/style"
)

// The manager's key hints, in words: one dim line of "KEY to ACTION"
// phrases under the list, for the row under the cursor, and on `?` a grid
// of every key instead. Modifiers are spelled out (shift, opt, ctrl); arrow
// glyphs are fine. The alternates (j/k, K/J, +/-, ...) work everywhere but
// the hints lead with the obvious keys.

// keyHint is one "KEY to ACTION" phrase of the footer line. keep ranks it
// for a narrow terminal: the lowest goes first.
type keyHint struct {
	text string
	keep int
}

// Hint ranks: the row's own action stays longest; "? for shortcuts" always.
const (
	keepLow = iota
	keepMid
	keepHigh
	keepAlways
)

const hintSep = " · "

// fitHints joins hs with " · ", dropping the lowest-ranked phrases (the
// later of equals first) until the line fits in width-1 columns; width 0
// is unknown and keeps them all.
func fitHints(hs []keyHint, width int) string {
	hs = slices.Clone(hs)
	join := func() string {
		parts := make([]string, len(hs))
		for i, h := range hs {
			parts[i] = h.text
		}
		return strings.Join(parts, hintSep)
	}
	for width > 0 && len(hs) > 1 && style.Width(join()) > width-1 {
		drop := -1
		for i, h := range hs {
			if h.keep < keepAlways && (drop < 0 || h.keep <= hs[drop].keep) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		hs = slices.Delete(hs, drop, drop+1)
	}
	return join()
}

// gridGap is the space between the grid's columns.
const gridGap = "   "

// keyGrid lays cells out in rows of up to four columns, each column as
// wide as its widest cell, using as many columns as fit in width-1
// (width 0: four). Cells read left to right, then down.
func keyGrid(cells []string, width int) []string {
	for cols := min(4, len(cells)); cols >= 1; cols-- {
		ws := make([]int, cols)
		for i, c := range cells {
			ws[i%cols] = max(ws[i%cols], style.Width(c))
		}
		total := style.Width(gridGap) * (cols - 1)
		for _, w := range ws {
			total += w
		}
		if width > 0 && total > width-1 && cols > 1 {
			continue
		}
		var out []string
		for r := 0; r*cols < len(cells); r++ {
			row := cells[r*cols : min(len(cells), (r+1)*cols)]
			var b strings.Builder
			for i, c := range row {
				if i > 0 {
					b.WriteString(gridGap)
				}
				if i < len(row)-1 {
					c = style.PadRight(c, ws[i])
				}
				b.WriteString(c)
			}
			out = append(out, b.String())
		}
		return out
	}
	return nil
}

// wrapPhrases joins phrases with " · " into as few lines of width-1
// columns as they take (width 0: one line).
func wrapPhrases(prefix string, phrases []string, width int) []string {
	var out []string
	line := prefix
	indent := strings.Repeat(" ", style.Width(prefix))
	for i, ph := range phrases {
		if i == 0 {
			line += ph
			continue
		}
		if next := line + hintSep + ph; width <= 0 || style.Width(next) <= width-1 {
			line = next
			continue
		}
		out = append(out, line)
		line = indent + ph
	}
	return append(out, line)
}

// listKeyCells are the list's keys for the ? grid.
func listKeyCells() []string {
	return []string{
		"↑↓ to move", "→ for details", "space to add or remove", "shift+↑↓ to reorder",
		"a to add a route", "x to remove a route", "r to check again", "s to save",
		"q to quit", "ctrl+c to quit now", "? to close",
	}
}

// listAlternates are the other keys the list takes, under the grid.
var listAlternates = []string{"j/k to move", "K/J or +/- to reorder", "enter or l for details", "g/G or home/end for top/bottom", "pgup/pgdn to page", "esc to quit"}

// listHints are the footer's phrases for the row under the cursor.
func (m *manager) listHints() []keyHint {
	r := m.rows[m.cur]
	inChain := slices.Contains(m.chain, m.cur)
	var hs []keyHint
	switch {
	case r == nil:
		hs = append(hs, keyHint{"space to remove", keepHigh})
	case inChain:
		hs = append(hs, keyHint{"space to remove", keepHigh}, keyHint{"shift+↑↓ to reorder", keepMid})
	default:
		hs = append(hs, keyHint{"space to add", keepHigh})
	}
	switch c := r.classOr(); {
	case c == clsSetup || c == clsUnavailable:
		hs = append(hs, keyHint{"→ for how to set it up", keepHigh})
	default:
		hs = append(hs, keyHint{"→ for details", keepMid})
	}
	if r != nil && r.hasRoute && (r.route.Configured || r.isNew) {
		hs = append(hs, keyHint{"x to remove", keepLow})
	}
	switch {
	case m.dirty():
		hs = append(hs, keyHint{"s to save", keepMid})
	case !inChain:
		hs = append(hs, keyHint{"a to add a route", keepLow})
	}
	return append(hs, keyHint{"? for shortcuts", keepAlways})
}

// keyLines are the footer's key lines for the page on screen: the hint
// line, or with ? the grid.
func (m *manager) keyLines(width int) []string {
	p := m.pal
	hints, cells, also := m.listHints, listKeyCells, listAlternates
	if m.page() == modeDetail {
		hints, cells, also = m.detailHints, detailKeyCells, detailAlternates
	}
	if !m.help {
		return []string{p.Dim(fitHints(hints(), width))}
	}
	var out []string
	for _, l := range keyGrid(cells(), width) {
		out = append(out, p.Dim(l))
	}
	for _, l := range wrapPhrases("also: ", also, width) {
		out = append(out, p.Dim(l))
	}
	return out
}

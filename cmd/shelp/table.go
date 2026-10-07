package main

import (
	"io"
	"strings"

	"github.com/ericandrechek/shelp/internal/style"
)

// writeTable writes rows as aligned columns, reproducing
// text/tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) exactly for plain cells:
// each column but the last is padded to its widest cell plus two spaces and
// the last column is left alone. Cells may carry color codes (tabwriter
// would count them as text); width is measured on the visible text.
func writeTable(w io.Writer, rows [][]string) error {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i == len(r)-1 {
				continue
			}
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			if cw := style.Width(c); cw > widths[i] {
				widths[i] = cw
			}
		}
	}
	var b strings.Builder
	for _, r := range rows {
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(style.PadRight(c, widths[i]+2))
			}
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// providerRow styles one `shelp providers` row. Plain palettes return the
// cells unchanged.
func providerRow(p style.Palette, id, kind, status, details string) []string {
	cells := []string{id, kind, status, details}
	if !p.On() {
		return cells
	}
	switch status {
	case "ok":
		cells[0] = p.Bold(id)
		cells[2] = p.OK(status)
	case "cooldown":
		cells[2] = p.Warn(status)
	case "unavailable":
		cells[0], cells[1], cells[3] = p.Dim(id), p.Dim(kind), p.Dim(details)
		cells[2] = p.Dim(status)
	}
	return cells
}

func providerHeader(p style.Palette) []string {
	h := []string{"PROVIDER", "KIND", "STATUS", "DETAILS"}
	for i := range h {
		h[i] = p.Bold(h[i])
	}
	return h
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ericandrechek/shelp/internal/config"
	"github.com/ericandrechek/shelp/internal/history"
	"github.com/ericandrechek/shelp/internal/style"
)

// historyPath is the seam for the history file's location; tests also
// point $XDG_STATE_HOME at a temp dir (see TestMain), so nothing can reach
// the user's real history.
var historyPath = history.Path

// historyValueFlags are `shelp history` flags that take a value.
var historyValueFlags = map[string]bool{
	"-n": true, "-s": true, "--search": true, "--provider": true,
}

// isHistoryArgs reports whether `shelp history ARGS` is the history
// command: no args, only flags (and the values of those that take one), or
// a first arg of show/clear/path. Anything else ("history of git commits
// for main.go") is a task.
func isHistoryArgs(rest []string) bool {
	if len(rest) == 0 {
		return true
	}
	switch rest[0] {
	case "show", "clear", "path":
		return true
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			return false
		}
		if historyValueFlags[a] {
			i++ // its value, whatever it is
		}
	}
	return true
}

func runHistory(_ context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "show":
			return historyShow(args[1:], stdout, stderr)
		case "clear":
			return historyClear(args[1:], stdout, stderr)
		case "path":
			if len(args) > 1 {
				if isHelpFlag(args[1]) {
					printHelp(stdout, "history")
					return nil
				}
				printHelp(stderr, "history")
				return usageError{fmt.Errorf("history path takes no arguments, got %q", args[1])}
			}
			fmt.Fprintln(stdout, historyPath())
			return nil
		}
	}
	return historyList(args, stdout, stderr)
}

func historyList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var n int
	var all, asJSON bool
	var search, prov string
	fs.IntVar(&n, "n", 20, "")
	fs.BoolVar(&all, "all", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.StringVar(&search, "s", "", "")
	fs.StringVar(&search, "search", "", "")
	fs.StringVar(&prov, "provider", "", "")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "history", stdout, stderr)
	}
	if fs.NArg() > 0 {
		printHelp(stderr, "history")
		return usageError{fmt.Errorf("unexpected argument %q", fs.Arg(0))}
	}
	if n < 1 && !all {
		printHelp(stderr, "history")
		return usageError{errors.New("-n needs a positive number")}
	}
	recs, err := history.Read(historyPath())
	if err != nil {
		return err
	}
	type numbered struct {
		n int
		r history.Record
	}
	var rows []numbered
	needle := strings.ToLower(search)
	for i, r := range recs {
		if prov != "" && r.Provider != prov {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(r.Input+"\n"+r.Output), needle) {
			continue
		}
		rows = append(rows, numbered{i + 1, r})
	}
	if !all && len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		for _, r := range rows {
			if err := enc.Encode(r.r); err != nil {
				return err
			}
		}
		return nil
	}
	if len(rows) == 0 {
		msg := "no history yet"
		if len(recs) > 0 {
			msg = "no matching history"
		}
		fmt.Fprintln(stdout, style.For(stdout).Dim("shelp: "+msg))
		return nil
	}
	p := style.For(stdout)
	width := style.TermWidth(stdout)
	numW := len(fmt.Sprint(rows[len(rows)-1].n))
	for _, r := range rows {
		line := historyLine(p, r.n, numW, r.r)
		if width > 0 {
			line = style.Truncate(line, width)
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// oneLine collapses all whitespace (newlines included) to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// historyTime formats t in local time: just the clock for today, the date
// too otherwise.
func historyTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	t = t.Local()
	y, m, d := t.Date()
	if y2, m2, d2 := time.Now().Date(); y == y2 && m == m2 && d == d2 {
		return t.Format("15:04")
	}
	return t.Format("Jan 02 15:04")
}

func historyDur(ms int64) string { return formatLatency(time.Duration(ms) * time.Millisecond) }

// historyLine is one list row: number, time, mode, provider, latency
// (and tokens), the input, then the output.
func historyLine(p style.Palette, n, numW int, r history.Record) string {
	prov := r.Provider
	if prov == "" {
		prov = "-"
	}
	meta := historyDur(r.LatencyMS)
	if r.Tokens != nil {
		meta += fmt.Sprintf(" · %d tok", r.Tokens.Input+r.Tokens.Output)
	}
	badge := p.Accent(fmt.Sprintf("%-7s", r.Mode))
	if r.Mode == "explain" {
		badge = p.Warn(fmt.Sprintf("%-7s", r.Mode))
	}
	out := oneLine(r.Output)
	if r.Mode == "explain" {
		out = oneLine(firstLine(r.Output))
	}
	if r.Error != "" {
		out = p.Err("error: " + oneLine(firstLine(r.Error)))
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s  %s %s",
		p.Dim(fmt.Sprintf("%*d", numW, n)), p.Dim(historyTime(r.Time)), badge, prov, p.Dim(meta),
		oneLine(r.Input), p.Dim("→"), out)
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

func historyShow(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && isHelpFlag(args[0]) {
		printHelp(stdout, "history")
		return nil
	}
	if len(args) != 1 {
		printHelp(stderr, "history")
		return usageError{errors.New("usage: shelp history show N")}
	}
	var n int
	if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n < 1 || fmt.Sprint(n) != args[0] {
		return usageError{fmt.Errorf("history show: %q is not an entry number (see `shelp history`)", args[0])}
	}
	recs, err := history.Read(historyPath())
	if err != nil {
		return err
	}
	if n > len(recs) {
		return fmt.Errorf("history show: no entry %d (the history has %d)", n, len(recs))
	}
	r := recs[n-1]
	p := style.For(stdout)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(stdout, "%s %s\n", p.Dim(fmt.Sprintf("%-9s", k)), v)
		}
	}
	row("entry", fmt.Sprint(n))
	row("time", r.Time)
	row("mode", r.Mode)
	row("provider", r.Provider)
	if len(r.Tried) > 0 {
		row("tried", strings.Join(r.Tried, ", "))
	}
	lat := historyDur(r.LatencyMS)
	if r.FirstChunkMS > 0 {
		lat += fmt.Sprintf(" (first chunk %s)", historyDur(r.FirstChunkMS))
	}
	row("latency", lat)
	if r.Tokens != nil {
		row("tokens", fmt.Sprintf("%d in, %d out", r.Tokens.Input, r.Tokens.Output))
	}
	row("template", r.Template)
	row("shell", r.Shell)
	row("key", r.Key)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, p.Dim("input"))
	fmt.Fprintln(stdout, r.Input)
	if r.Error != "" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, p.Err("error"))
		fmt.Fprintln(stdout, r.Error)
		return nil
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, p.Dim("output"))
	fmt.Fprintln(stdout, strings.TrimRight(r.Output, "\n"))
	return nil
}

func historyClear(args []string, stdout, stderr io.Writer) error {
	yes := false
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		case "-h", "--help":
			printHelp(stdout, "history")
			return nil
		default:
			printHelp(stderr, "history")
			return usageError{fmt.Errorf("history clear: unknown argument %q", a)}
		}
	}
	path := historyPath()
	if !yes {
		if !isInteractive(stdout) {
			return usageError{errors.New("history clear: not a terminal; pass --yes to delete the history")}
		}
		fmt.Fprintf(stdout, "Delete all request history (%s)? [y/N] ", tildePath(path))
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "kept")
			return nil
		}
	}
	if err := history.Clear(path); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "shelp: history cleared")
	return nil
}

// recordHistory appends one record for a finished call. Best effort and
// silent: history never changes how the real command ends.
func recordHistory(cfg *config.Config, r history.Record) {
	s := history.Load(cfg)
	if !s.IsEnabled() {
		return
	}
	if r.Time == "" {
		r.Time = history.Now()
	}
	history.Append(historyPath(), r, s.Max())
}

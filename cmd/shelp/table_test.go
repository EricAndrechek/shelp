package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"

	"github.com/ericandrechek/shelp/internal/style"
)

var sampleProviders = [][4]string{
	{"fm", "builtin", "ok", "Apple on-device model"},
	{"shortcuts:apple-pro", "shortcuts", "cooldown", "until 14:32: usage limit"},
	{"claude:haiku", "agentcli", "unavailable", "claude not found on PATH"},
	{"lmstudio:qwen/qwen3-coder", "local", "ok", ""},
	{"é:ünï", "x", "unavailable", "ünïcode"},
}

func TestTablePlainMatchesTabwriter(t *testing.T) {
	var want bytes.Buffer
	tw := tabwriter.NewWriter(&want, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tKIND\tSTATUS\tDETAILS")
	rows := [][]string{providerHeader(style.Plain())}
	for _, r := range sampleProviders {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r[0], r[1], r[2], r[3])
		rows = append(rows, providerRow(style.Plain(), r[0], r[1], r[2], r[3]))
	}
	tw.Flush()
	var got bytes.Buffer
	if err := writeTable(&got, rows); err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("plain table differs from tabwriter:\n got %q\nwant %q", got.String(), want.String())
	}
}

func TestTableColoredAlignsAndStripsToPlain(t *testing.T) {
	p := style.Forced(style.TrueColor)
	rows := [][]string{providerHeader(p)}
	plain := [][]string{providerHeader(style.Plain())}
	for _, r := range sampleProviders {
		rows = append(rows, providerRow(p, r[0], r[1], r[2], r[3]))
		plain = append(plain, providerRow(style.Plain(), r[0], r[1], r[2], r[3]))
	}
	var c, pl bytes.Buffer
	writeTable(&c, rows)
	writeTable(&pl, plain)
	if !strings.Contains(c.String(), "\x1b[") {
		t.Fatal("expected color codes")
	}
	if style.Strip(c.String()) != pl.String() {
		t.Errorf("colored table does not strip to plain:\n%q\n%q", style.Strip(c.String()), pl.String())
	}
}

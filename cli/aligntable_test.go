// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestAlignedTablePlainText(t *testing.T) {
	var buf bytes.Buffer
	w := newAlignedTable(&buf)
	fmt.Fprintf(w, "ID\tHOSTNAME\tIP\n")
	fmt.Fprintf(w, "%s\t%s\t%s\n", "6e6d1edf", "plex", "192.168.1.219")
	fmt.Fprintf(w, "%s\t%s\t%s\n", "3a0e47a4", "danlap", "192.168.1.215")
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	want := "" +
		"ID        HOSTNAME  IP\n" +
		"6e6d1edf  plex      192.168.1.219\n" +
		"3a0e47a4  danlap    192.168.1.215\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestAlignedTableANSIInvisibleInWidth(t *testing.T) {
	// Cells wrapped in ANSI CSI escapes — these must not affect column
	// widths. This is the exact bug tabwriter has.
	red := func(s string) string { return "\x1b[31m" + s + "\x1b[0m" }
	bold := func(s string) string { return "\x1b[1m" + s + "\x1b[0m" }

	var buf bytes.Buffer
	w := newAlignedTable(&buf)
	fmt.Fprintf(w, "%s\t%s\n", bold("ID"), bold("STATUS"))
	fmt.Fprintf(w, "%s\t%s\n", "6e6d1edf", red("revoked"))
	fmt.Fprintf(w, "%s\t%s\n", "3a0e47a4", "active")
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Strip ANSI from output and confirm visible alignment.
	visible := ansiCSIRe.ReplaceAllString(buf.String(), "")
	want := "" +
		"ID        STATUS\n" +
		"6e6d1edf  revoked\n" +
		"3a0e47a4  active\n"
	if visible != want {
		t.Errorf("visible output mismatch\ngot:\n%q\nwant:\n%q", visible, want)
	}

	// And the ANSI bytes themselves survived intact.
	if !strings.Contains(buf.String(), red("revoked")) {
		t.Error("ANSI wrap on 'revoked' was lost")
	}
	if !strings.Contains(buf.String(), bold("ID")) {
		t.Error("ANSI wrap on 'ID' was lost")
	}
}

func TestAlignedTableEmptyCells(t *testing.T) {
	var buf bytes.Buffer
	w := newAlignedTable(&buf)
	fmt.Fprintf(w, "A\tB\tC\n")
	fmt.Fprintf(w, "x\t\tz\n")
	fmt.Fprintf(w, "longer\tmid\t-\n")
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	want := "" +
		"A       B    C\n" +
		"x            z\n" +
		"longer  mid  -\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestAlignedTableUnicode(t *testing.T) {
	var buf bytes.Buffer
	w := newAlignedTable(&buf)
	fmt.Fprintf(w, "NAME\tSTATUS\n")
	fmt.Fprintf(w, "café\tok\n")
	fmt.Fprintf(w, "naïve\tok\n")
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	want := "" +
		"NAME   STATUS\n" +
		"café   ok\n" +
		"naïve  ok\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestAlignedTableTrailingLineWithoutNewline(t *testing.T) {
	var buf bytes.Buffer
	w := newAlignedTable(&buf)
	fmt.Fprintf(w, "A\tB\n")
	fmt.Fprintf(w, "x\ty") // no newline
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	want := "" +
		"A  B\n" +
		"x  y\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestVisibleWidthIgnoresANSI(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello", 5},
		{"\x1b[31mhello\x1b[0m", 5},
		{"\x1b[1m\x1b[38;2;100;200;50mID\x1b[0m", 2},
		{"café", 4},
	}
	for _, c := range cases {
		if got := visibleWidth(c.in); got != c.want {
			t.Errorf("visibleWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

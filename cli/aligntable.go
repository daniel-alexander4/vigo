// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ansiCSIRe matches ANSI CSI escape sequences — the only ANSI form the
// CLI's color helpers in format.go emit. Used to compute visible cell
// widths.
var ansiCSIRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// alignedTable is an ANSI-aware drop-in for the project-wide
// text/tabwriter usage `tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)`. It
// buffers tab-separated rows, computes column widths from visible
// characters only (stripping ANSI CSI escapes), and emits left-aligned
// columns with a two-space gutter on Flush.
//
// Why not tabwriter: tabwriter pads by raw byte count and counts the
// invisible bytes of ANSI escape codes as visible content. Every CLI
// table whose cells pass through format.go helpers (header, dim,
// colorStatus, …) ends up over-padded and wraps in narrow terminals
// when useColor is true.
type alignedTable struct {
	w    io.Writer
	buf  bytes.Buffer
	rows [][]string
}

func newAlignedTable(w io.Writer) *alignedTable {
	return &alignedTable{w: w}
}

// Write parses bytes into tab-separated rows on newline boundaries.
// Any trailing bytes without a newline are held until the next Write
// or treated as the final row on Flush.
func (t *alignedTable) Write(p []byte) (int, error) {
	n := len(p)
	t.buf.Write(p)
	for {
		idx := bytes.IndexByte(t.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := t.buf.Next(idx + 1)
		t.rows = append(t.rows, strings.Split(string(line[:len(line)-1]), "\t"))
	}
	return n, nil
}

// Flush emits the buffered rows. Column widths are the max visible
// width of any cell in that column; cells are padded with spaces to
// reach their column width, and columns are joined with two spaces.
// The last cell on each row is not padded.
func (t *alignedTable) Flush() error {
	if t.buf.Len() > 0 {
		t.rows = append(t.rows, strings.Split(t.buf.String(), "\t"))
		t.buf.Reset()
	}

	var widths []int
	for _, row := range t.rows {
		for c, cell := range row {
			if c >= len(widths) {
				widths = append(widths, 0)
			}
			if w := visibleWidth(cell); w > widths[c] {
				widths[c] = w
			}
		}
	}

	var out strings.Builder
	for _, row := range t.rows {
		for c, cell := range row {
			if c > 0 {
				out.WriteString("  ")
			}
			out.WriteString(cell)
			if c < len(row)-1 {
				if pad := widths[c] - visibleWidth(cell); pad > 0 {
					out.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		out.WriteByte('\n')
	}
	t.rows = nil

	_, err := io.WriteString(t.w, out.String())
	return err
}

// visibleWidth returns the number of visible rune positions in s,
// ignoring ANSI CSI escape sequences. The CLI does not emit East-asian
// wide characters in table cells, so a rune count is sufficient.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiCSIRe.ReplaceAllString(s, ""))
}

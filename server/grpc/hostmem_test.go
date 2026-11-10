// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import "testing"

func TestParseMemTotal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want uint64
	}{
		{"typical", "MemTotal:       32717312 kB\nMemFree: 100 kB\n", 32717312 * 1024},
		{"first-line", "MemTotal: 1024 kB\n", 1024 * 1024},
		{"absent", "MemFree: 100 kB\n", 0},
		{"garbage-value", "MemTotal: notanumber kB\n", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseMemTotal(c.in); got != c.want {
				t.Errorf("parseMemTotal(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestParseCgroupMax(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want uint64
	}{
		{"v2-real-limit", "2147483648\n", 2147483648},
		{"v2-unlimited", "max\n", 0},
		{"v1-unlimited-sentinel", "9223372036854771712\n", 0}, // ~PAGE_COUNTER_MAX
		{"empty", "", 0},
		{"garbage", "nope\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseCgroupMax(c.in); got != c.want {
				t.Errorf("parseCgroupMax(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestDefaultMaxConnections(t *testing.T) {
	const giB = 1 << 30
	cases := []struct {
		name   string
		memGiB float64
		wantLo int // inclusive lower bound (the published safe-ceiling table)
		wantHi int // inclusive upper bound
	}{
		// 0.40 × RAM ÷ 220 KB reproduces the sizing-guide safe ceilings and
		// stays below the ~47%-of-RAM OOM wall.
		{"31GiB→~60k", 31, 55_000, 62_000},
		{"16GiB→~30k", 16, 27_000, 32_000},
		{"64GiB→~120k", 64, 115_000, 125_000},
		{"unreadable→0", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := defaultMaxConnections(uint64(c.memGiB * giB))
			if got < c.wantLo || got > c.wantHi {
				t.Errorf("defaultMaxConnections(%.0f GiB) = %d, want in [%d, %d]", c.memGiB, got, c.wantLo, c.wantHi)
			}
		})
	}
}

func TestDefaultMaxConnectionsBelowOOMWall(t *testing.T) {
	// The auto-derived cap must sit below the measured OOM point (~47% of RAM
	// ÷ 220 KB) on the reference host — otherwise the default wouldn't shed
	// before OOM, defeating the whole purpose.
	const giB = 1 << 30
	const rss = 220 * 1024
	var ramGiB float64 = 31 // var (not const) so the float→int truncation is a runtime op
	oomWall := int(0.47 * ramGiB * float64(giB) / float64(rss))
	got := defaultMaxConnections(31 * giB)
	if got >= oomWall {
		t.Errorf("auto cap %d must be below the OOM wall %d on a 31 GiB host", got, oomWall)
	}
}

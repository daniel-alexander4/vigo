// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseRetention(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"", defaultRetention},
		{"30d", 30 * day},
		{"1d", day},
		{"720h", 720 * time.Hour},
		{"45m", 45 * time.Minute},
		{"garbage", defaultRetention},
		{"0d", defaultRetention},  // days must be > 0; falls through to ParseDuration which rejects "0d"
		{"-5d", defaultRetention}, // same fall-through path
	}
	for _, tt := range tests {
		if got := parseRetention(tt.input); got != tt.want {
			t.Errorf("parseRetention(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"invalid", 0},
		{"1024", 1024},
		{"100B", 100},
		{"512KiB", 512 * 1024},
		{"256MiB", 256 * 1024 * 1024},
		{"4GiB", 4 * 1024 * 1024 * 1024},
		{"1TiB", 1 << 40},
		{"0", 0},
		{"-1", 0},
	}

	for _, tt := range tests {
		got := parseMemoryLimit(tt.input)
		if got != tt.want {
			t.Errorf("parseMemoryLimit(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestSameStringSet(t *testing.T) {
	cases := []struct {
		name   string
		a, b   []string
		expect bool
	}{
		{"both empty", nil, nil, true},
		{"a empty, b populated", nil, []string{"x"}, false},
		{"identical order", []string{"a", "b"}, []string{"a", "b"}, true},
		{"reordered", []string{"a", "b"}, []string{"b", "a"}, true},
		{"single mismatch", []string{"a", "b"}, []string{"a", "c"}, false},
		{"length mismatch", []string{"a", "b"}, []string{"a"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sameStringSet(c.a, c.b); got != c.expect {
				t.Errorf("sameStringSet(%v, %v) = %v, want %v", c.a, c.b, got, c.expect)
			}
		})
	}
}

func TestDiffStringSets(t *testing.T) {
	added, removed := diffStringSets([]string{"a", "b"}, []string{"b", "c"})
	if !reflect.DeepEqual(added, []string{"c"}) {
		t.Errorf("added = %v, want [c]", added)
	}
	if !reflect.DeepEqual(removed, []string{"a"}) {
		t.Errorf("removed = %v, want [a]", removed)
	}

	added, removed = diffStringSets(nil, []string{"x"})
	if !reflect.DeepEqual(added, []string{"x"}) || removed != nil {
		t.Errorf("nil → [x]: added=%v removed=%v", added, removed)
	}

	added, removed = diffStringSets([]string{"x"}, nil)
	if added != nil || !reflect.DeepEqual(removed, []string{"x"}) {
		t.Errorf("[x] → nil: added=%v removed=%v", added, removed)
	}

	added, removed = diffStringSets([]string{"a", "b"}, []string{"a", "b"})
	if added != nil || removed != nil {
		t.Errorf("identical: added=%v removed=%v", added, removed)
	}
}

func TestBlacklistChangeSummary(t *testing.T) {
	if got := blacklistChangeSummary([]string{"x"}, nil); got != "added: x" {
		t.Errorf("added-only: got %q", got)
	}
	if got := blacklistChangeSummary(nil, []string{"y"}); got != "removed: y" {
		t.Errorf("removed-only: got %q", got)
	}
	if got := blacklistChangeSummary([]string{"a"}, []string{"b"}); got != "added: a; removed: b" {
		t.Errorf("both: got %q", got)
	}
	if got := blacklistChangeSummary(nil, nil); got != "no change" {
		t.Errorf("neither: got %q", got)
	}
}

func TestJSONStringArray(t *testing.T) {
	if got := jsonStringArray(nil); got != "[]" {
		t.Errorf("nil: got %s", got)
	}
	if got := jsonStringArray([]string{}); got != "[]" {
		t.Errorf("empty: got %s", got)
	}
	got := jsonStringArray([]string{"raspberrypi", "*.test.example.com"})
	if got != `["raspberrypi","*.test.example.com"]` {
		t.Errorf("normal: got %s", got)
	}
}

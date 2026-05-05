// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"reflect"
	"sort"
	"testing"
)

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

func TestDiffNewlyMatched(t *testing.T) {
	hosts := []string{"danlap", "annlap", "plex", "girlslaptop", "raspberrypi"}

	tests := []struct {
		name string
		prev []string
		next []string
		want []string
	}{
		{
			name: "empty to empty",
			prev: nil,
			next: nil,
			want: nil,
		},
		{
			name: "add a single hostname",
			prev: nil,
			next: []string{"raspberrypi"},
			want: []string{"raspberrypi"},
		},
		{
			name: "add a glob that matches multiple",
			prev: nil,
			next: []string{"*lap"},
			want: []string{"annlap", "danlap"},
		},
		{
			name: "remove returns nothing",
			prev: []string{"raspberrypi"},
			next: nil,
			want: nil,
		},
		{
			name: "no change returns nothing",
			prev: []string{"raspberrypi"},
			next: []string{"raspberrypi"},
			want: nil,
		},
		{
			name: "expand glob — only newly matching hostnames are returned",
			prev: []string{"danlap"},
			next: []string{"*lap"},
			want: []string{"annlap"},
		},
		{
			name: "host already matched by overlapping glob is not re-emitted",
			prev: []string{"*lap"},
			next: []string{"*lap", "raspberrypi"},
			want: []string{"raspberrypi"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffNewlyMatched(tt.prev, tt.next, hosts)
			sort.Strings(got)
			sort.Strings(tt.want)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("diffNewlyMatched(%v, %v, %v) = %v, want %v",
					tt.prev, tt.next, hosts, got, tt.want)
			}
		})
	}
}

func TestSubstrateEnabled(t *testing.T) {
	if !substrateEnabled(nil) {
		t.Error("nil pointer must default to enabled (operator did not set the flag)")
	}
	tr := true
	fa := false
	if !substrateEnabled(&tr) {
		t.Error("explicit true must be enabled")
	}
	if substrateEnabled(&fa) {
		t.Error("explicit false must be disabled")
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

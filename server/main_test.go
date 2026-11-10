// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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

func TestShouldAutoDeriveMemLimit(t *testing.T) {
	tests := []struct {
		gomemlimitEnv string
		want          bool
	}{
		{"", true},          // unset → auto-derive
		{"4GiB", false},     // explicit value → defer to the env (don't clobber)
		{"off", false},      // explicit "no limit" → defer, must NOT override with auto-derive
		{"26843545600", false},
	}
	for _, tt := range tests {
		if got := shouldAutoDeriveMemLimit(tt.gomemlimitEnv); got != tt.want {
			t.Errorf("shouldAutoDeriveMemLimit(%q) = %v, want %v", tt.gomemlimitEnv, got, tt.want)
		}
	}
}

func TestDeriveMemoryLimit(t *testing.T) {
	tests := []struct {
		budget uint64
		want   int64
	}{
		{0, 0},                                   // unknown budget → leave unset
		{16 << 30, int64((16 << 30) / 100 * 80)}, // 16 GiB → ~12.8 GiB
		{32 << 30, int64((32 << 30) / 100 * 80)},
		{128 << 30, int64((128 << 30) / 100 * 80)}, // overflow-safe at large RAM
	}
	for _, tt := range tests {
		got := deriveMemoryLimit(tt.budget)
		if got != tt.want {
			t.Errorf("deriveMemoryLimit(%d) = %d, want %d", tt.budget, got, tt.want)
		}
		// Sanity: result is ~80% and strictly below the budget (GC-overshoot margin).
		if tt.budget > 0 && (uint64(got) >= tt.budget || uint64(got) < tt.budget/100*79) {
			t.Errorf("deriveMemoryLimit(%d) = %d not in the ~80%%-with-headroom band", tt.budget, got)
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

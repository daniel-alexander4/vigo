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

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"strings"
	"testing"
	"time"
)

// TestUserFriendlyBlobName pins the label-to-display transforms that
// the Swarm Load page applies to every row's Name column. Cairn labels
// must render in human-readable form; admin payload labels pass
// through unchanged; unlabeled blobs fall back to a short sha so the
// operator always has something to grab.
func TestUserFriendlyBlobName(t *testing.T) {
	cases := []struct {
		name  string
		label string
		sha   string
		want  string
	}{
		{
			name:  "cairn label renders as user/project",
			label: "cairn:danlap:dan:vigo",
			sha:   "abc123def456",
			want:  "cairn: dan/vigo (from danlap)",
		},
		{
			name:  "admin payload keeps its filename label",
			label: "firmware-v2.1.bin",
			sha:   "abc123def456",
			want:  "firmware-v2.1.bin",
		},
		{
			name:  "unlabeled blob surfaces short sha",
			label: "",
			sha:   "abc123def4567890fedcba",
			want:  "abc123def456",
		},
		{
			name:  "malformed cairn label passes through",
			label: "cairn:broken",
			sha:   "aabbcc",
			want:  "cairn:broken",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := userFriendlyBlobName(c.label, c.sha)
			if got != c.want {
				t.Errorf("userFriendlyBlobName(%q, %q) = %q, want %q",
					c.label, c.sha, got, c.want)
			}
		})
	}
}

// TestFormatRelative pins the four buckets (just now / m / h / d) and
// the absolute-date fallback for anything over 30 days old. Dash for
// zero-time so rows without a timestamp read as missing rather than
// epoch.
func TestFormatRelative(t *testing.T) {
	now := time.Now()
	t.Run("zero time returns dash", func(t *testing.T) {
		if got := formatRelative(time.Time{}); got != "—" {
			t.Errorf("zero time = %q, want dash", got)
		}
	})
	t.Run("sub-minute reads as just now", func(t *testing.T) {
		if got := formatRelative(now.Add(-10 * time.Second)); got != "just now" {
			t.Errorf("10s ago = %q", got)
		}
	})
	t.Run("minutes bucket", func(t *testing.T) {
		got := formatRelative(now.Add(-5 * time.Minute))
		if got != "5m ago" {
			t.Errorf("5m ago = %q", got)
		}
	})
	t.Run("hours bucket", func(t *testing.T) {
		got := formatRelative(now.Add(-3 * time.Hour))
		if got != "3h ago" {
			t.Errorf("3h ago = %q", got)
		}
	})
	t.Run("days bucket", func(t *testing.T) {
		got := formatRelative(now.Add(-48 * time.Hour))
		if got != "2d ago" {
			t.Errorf("2d ago = %q", got)
		}
	})
	t.Run("over 30 days falls back to iso date", func(t *testing.T) {
		old := now.Add(-90 * 24 * time.Hour)
		got := formatRelative(old)
		// Absolute date format — just confirm it's not a relative bucket.
		if strings.HasSuffix(got, "ago") || got == "just now" {
			t.Errorf("90d should fall back to date, got %q", got)
		}
	})
}

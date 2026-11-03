// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEnvoySwarmLoadTemplateRenders pins the template against a
// populated page-data struct. Regression guard for the
// humanBytes(int64) / uint64 field-type mismatch that shipped
// broken the first time around — the template compiled and the
// page loaded, but Render failed mid-execute with "wrong type for
// value" and only the first stat card surfaced before the write
// stream aborted.
//
// Any future template-func signature drift against this page's
// data shape will fail here instead of in the browser.
func TestEnvoySwarmLoadTemplateRenders(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	data := EnvoySwarmLoadPageData{
		PageData: PageData{Title: "Swarm Load: test", ActiveNav: "envoys"},
		Hostname: "raspberrypi",
		EnvoyID:  "00000000-0000-0000-0000-000000000001",
		Filecast: []SwarmLoadRow{
			{
				Name: "firmware.bin", Detail: "abc123 · admin",
				Size: 1_048_576, SizeHuman: "1.0 MiB",
				Status: "complete", When: "2h ago", Link: "/swarm/filecast/abc",
			},
			{
				Name: "gitback: dan/vigo (from danlap)", Detail: "abc456 · danlap",
				Size: 8192, SizeHuman: "8.0 KiB",
				Status: "downloading", Progress: "3/16 chunks", When: "1m ago", Link: "/swarm/filecast/abc456",
			},
		},
		Gitback: []SwarmLoadRow{{
			Name: "/var/lib/vigo/swarm/gitback/mirrors/danlap/dan/vigo.git",
			Detail: "dan/vigo — from danlap · git deadbeef",
			Size: 4_096, SizeHuman: "4.0 KiB",
			Status: "stale", When: "45d ago", Link: "/swarm/gitback/gitback:danlap:dan:vigo",
		}},
		Longdrawer: []SwarmLoadRow{{
			Name: "~dan/longdrawer/notes.md", Detail: "user dan",
			Size: 2_048, SizeHuman: "2.0 KiB",
			Status: "live", When: "just now",
		}},
		Lockbox: []SwarmLoadRow{{
			Name: "~dan/lockbox/secrets.age", Detail: "user dan",
			Size: 512, SizeHuman: "512 B",
			Status: "live", When: "10m ago",
		}},
		FilecastTotal: 1_056_768, GitbackTotal: 4_096, LongdrawerTotal: 2_048, LockboxTotal: 512,
		GrandTotal: 1_063_424,
	}
	rec := httptest.NewRecorder()
	renderer.RenderFragment(rec, "envoy-detail", "envoy-swarm-load-fragment", data)
	body := rec.Body.String()
	// Render uses an internal bytes.Buffer and only writes to the
	// ResponseWriter on success; a template-execute failure surfaces
	// as a truncated response. The four section titles below span
	// the entire template, so if any intermediate render step blows
	// up at least one won't appear.
	for _, must := range []string{
		"Total Swarm Footprint",
		">Filecast<", ">Gitback<", ">Longdrawer<", ">Lockbox<",
		"firmware.bin",
		"/var/lib/vigo/swarm/gitback/mirrors/danlap/dan/vigo.git",
		"~dan/longdrawer/notes.md",
		"~dan/lockbox/secrets.age",
	} {
		if !strings.Contains(body, must) {
			t.Errorf("rendered body missing %q\n--- body snippet ---\n%s", must, truncateForTest(body, 1200))
		}
	}
	// Belt-and-suspenders — "template error" would indicate the
	// renderer caught an execute failure and surfaced the fallback.
	if bytes.Contains([]byte(body), []byte("template error")) {
		t.Errorf("template-error sentinel present in rendered body; see diff: %s", truncateForTest(body, 1200))
	}
}

func truncateForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestUserFriendlyBlobName pins the label-to-display transforms that
// the Swarm Load page applies to every row's Name column. Gitback labels
// must render in human-readable form; admin filecast labels pass
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
			name:  "gitback label renders as user/project",
			label: "gitback:danlap:dan:vigo",
			sha:   "abc123def456",
			want:  "gitback: dan/vigo (from danlap)",
		},
		{
			name:  "admin filecast keeps its filename label",
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
			name:  "malformed gitback label passes through",
			label: "gitback:broken",
			sha:   "aabbcc",
			want:  "gitback:broken",
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

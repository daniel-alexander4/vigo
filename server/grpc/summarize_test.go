// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/convergence"
)

func rr(action string, changed bool, errMsg string) *pb.ResourceRunResult {
	return &pb.ResourceRunResult{ActionTaken: action, Changed: changed, ErrorMessage: errMsg}
}

func TestSummarizeResults(t *testing.T) {
	cases := []struct {
		name                                     string
		results                                  []*pb.ResourceRunResult
		totalCount                               int32
		total, changed, failed, blocked, skipped int64
		status                                   string
	}{
		{
			name: "all settled elided", results: nil, totalCount: 10,
			total: 10, skipped: 10, status: "success",
		},
		{
			name: "changed/failed/blocked mix → failure wins",
			results: []*pb.ResourceRunResult{
				rr("updated", true, ""), rr("install", false, "boom"), rr("blocked", false, "guardrail"),
			},
			totalCount: 10,
			total:      10, changed: 1, failed: 2, blocked: 1, skipped: 7, status: "failure",
		},
		{
			name:       "blocked only → status blocked, counted as a failed resource",
			results:    []*pb.ResourceRunResult{rr("blocked", false, "guardrail")},
			totalCount: 5,
			total:      5, failed: 1, blocked: 1, skipped: 4, status: "blocked",
		},
		{
			name: "old agent (total_count 0) → falls back to len, counts settled rows",
			results: []*pb.ResourceRunResult{
				rr("updated", true, ""), rr("settled", false, ""), rr("settled", false, ""),
			},
			totalCount: 0,
			total:      3, changed: 1, skipped: 2, status: "success",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			total, changed, failed, blocked, skipped, status := summarizeResults(c.results, c.totalCount)
			if total != c.total || changed != c.changed || failed != c.failed ||
				blocked != c.blocked || skipped != c.skipped || status != c.status {
				t.Errorf("got total=%d changed=%d failed=%d blocked=%d skipped=%d status=%q;\n want total=%d changed=%d failed=%d blocked=%d skipped=%d status=%q",
					total, changed, failed, blocked, skipped, status,
					c.total, c.changed, c.failed, c.blocked, c.skipped, c.status)
			}
		})
	}
}

// TestSummarizeResults_BlockedClassifiesByFraction: one guardrail-blocked
// resource out of 172 is Degraded on the failure axis — the same
// resource-granular split as one errored resource — not Failed outright.
func TestSummarizeResults_BlockedClassifiesByFraction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []*pb.ResourceRunResult
		total   int32
		want    string
	}{
		{"one blocked of 172", []*pb.ResourceRunResult{rr("blocked", false, "guardrail")}, 172, "degraded"},
		{"one errored of 172", []*pb.ResourceRunResult{rr("install", false, "boom")}, 172, "degraded"},
		{"all blocked", []*pb.ResourceRunResult{rr("blocked", false, "g"), rr("blocked", false, "g")}, 2, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, changed, failed, _, _, status := summarizeResults(tc.results, tc.total)
			got := convergence.ComputeConvergence([]convergence.RunInfo{{
				Status: status, ChangedCount: changed, FailedCount: failed, ResourceCount: total,
			}})
			if got != tc.want {
				t.Errorf("classification = %q, want %q (status=%q failed=%d total=%d)", got, tc.want, status, failed, total)
			}
		})
	}
}

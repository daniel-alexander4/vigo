// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
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
			total:      10, changed: 1, failed: 1, blocked: 1, skipped: 7, status: "failure",
		},
		{
			name:       "blocked only → status blocked (not skipped)",
			results:    []*pb.ResourceRunResult{rr("blocked", false, "guardrail")},
			totalCount: 5,
			total:      5, blocked: 1, skipped: 4, status: "blocked",
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

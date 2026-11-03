// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import pb "git.alexander4.io/vigo/generated.code/proto/vigo"

// summarizeResults derives the canonical run counts and status from the
// reported result rows plus the run's claimed resource total. Both report paths
// — the unary ReportResult and the stream DeltaEvent (handleRunDelta) — route
// through it, so the blocked-aware status rule (failure > blocked > success) and
// the no-op accounting live in exactly one place.
//
// `results` carries only the non-no-op rows (settled no-ops elided by the
// agent); `totalCount` is the full resource count (0 from older agents that
// sent every row → falls back to len(results)). The elided settled rows fold
// into skipped. The returned `total` is the resolved resource count.
func summarizeResults(results []*pb.ResourceRunResult, totalCount int32) (total, changed, failed, blocked, skipped int64, status string) {
	total = int64(totalCount)
	if total < int64(len(results)) {
		total = int64(len(results))
	}
	for _, r := range results {
		if r.GetChanged() {
			changed++
		}
		if r.GetActionTaken() == "blocked" {
			blocked++
		} else if r.GetErrorMessage() != "" {
			failed++
		}
		if r.GetActionTaken() == "settled" && !r.GetChanged() {
			skipped++
		}
	}
	// Fold the elided settled no-ops (total minus the rows we received) into skipped.
	skipped += total - int64(len(results))

	status = "success"
	if failed > 0 {
		status = "failure"
	} else if blocked > 0 {
		status = "blocked"
	}
	return
}

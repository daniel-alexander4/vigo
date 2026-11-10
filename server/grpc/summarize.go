// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/convergence"
)

// summarizeResults derives the canonical run counts and status from the
// reported result rows plus the run's claimed resource total. Both report paths
// — the unary ReportResult and the stream DeltaEvent (handleRunDelta) — route
// through it, so the blocked-aware status rule (failure > blocked > success) and
// the no-op accounting live in exactly one place.
//
// A guardrail-blocked resource is a failed resource: it counts into `failed`
// (and separately into `blocked`, so events can say why), which keeps the
// resource-granular Degraded/Failed split in server/convergence honest — one
// blocked resource of 172 is Degraded, exactly like one errored resource.
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
	var errored int64
	for _, r := range results {
		if r.GetChanged() {
			changed++
		}
		if r.GetActionTaken() == convergence.ActionTakenBlocked {
			blocked++
		} else if r.GetErrorMessage() != "" {
			errored++
		}
		if r.GetActionTaken() == convergence.ActionTakenSettled && !r.GetChanged() {
			skipped++
		}
	}
	failed = errored + blocked
	// Fold the elided settled no-ops (total minus the rows we received) into skipped.
	skipped += total - int64(len(results))

	status = convergence.RunStatusSuccess
	if errored > 0 {
		status = convergence.RunStatusFailure
	} else if blocked > 0 {
		status = convergence.RunStatusBlocked
	}
	return
}

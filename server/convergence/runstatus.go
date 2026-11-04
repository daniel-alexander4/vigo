// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package convergence

// Run-level status vocabulary: the values of the runs.status column and of
// RunInfo.Status. The ingest path (server/grpc summarizeResults) produces them
// and ComputeConvergence consumes them, so both sides read one set of names.
// The SQL side (migrations' runs CHECK, server/db/queries/runs.sql) spells the
// same literals; keep them in step.
const (
	RunStatusRunning = "running" // row created, results not yet recorded
	RunStatusSuccess = "success" // no resource failed or was blocked
	RunStatusFailure = "failure" // at least one resource reported an error
	RunStatusBlocked = "blocked" // a guardrail blocked a resource, none errored
	// RunStatusPartial is admitted by the runs CHECK and filtered on by the
	// /runs page, but no ingest path produces it.
	RunStatusPartial = "partial"
)

// Per-resource action_taken strings the agent emits that are not one of the
// create/update/delete verbs ClassifyAction parses.
const (
	ActionTakenBlocked = "blocked" // a guardrail refused the resource
	ActionTakenSettled = "settled" // already in desired state; no change
)

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package convergence

import "strings"

// Action is the canonical bucket for a per-resource run result —
// what the agent did (or would do) on this resource.
type Action int

const (
	ActionNoop Action = iota // no change required ("ok", "none", "noop", "settled")
	ActionCreate
	ActionUpdate
	ActionDelete
	ActionBlocked // a guardrail refused the resource (ActionTakenBlocked)
)

// String returns the canonical lowercase form.
func (a Action) String() string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	case ActionBlocked:
		return ActionTakenBlocked
	default:
		return "noop"
	}
}

// ClassifyAction parses an action string into the canonical bucket.
// Accepts both present-tense ("create") and past-tense ("created")
// forms because the Rust agent emits past-tense via its Display impl
// (Action::Created -> "created") while UI/CLI plan-preview surfaces
// emit present-tense. Whitespace and case tolerant. The agent's "blocked"
// outcome is its own bucket (a guardrail refused the change — not a no-op);
// "settled", "skipped" and unknown vocabularies land in ActionNoop.
func ClassifyAction(s string) Action {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "create", "created":
		return ActionCreate
	case "update", "updated":
		return ActionUpdate
	case "delete", "deleted":
		return ActionDelete
	case ActionTakenBlocked:
		return ActionBlocked
	default:
		return ActionNoop
	}
}

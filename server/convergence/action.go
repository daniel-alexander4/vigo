// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	default:
		return "noop"
	}
}

// ClassifyAction parses an action string into the canonical bucket.
// Whitespace and case tolerant. Unknown vocabularies land in ActionNoop.
func ClassifyAction(s string) Action {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "create":
		return ActionCreate
	case "update":
		return ActionUpdate
	case "delete":
		return ActionDelete
	default:
		return ActionNoop
	}
}

// Package compliance computes compliance status for managed nodes based on
// their last run results and last-seen timestamps.
package compliance

import "time"

// RunInfo holds the minimal information needed to compute compliance status.
// Decoupled from db.Run to avoid package coupling.
type RunInfo struct {
	Status       string
	ChangedCount int64
}

// ComputeStatus determines the compliance status for a single node.
// Returns one of: "compliant", "drifted", "failing", "stale", "unknown".
//
// Priority order:
//  1. Stale — node hasn't checked in within 2x base interval
//  2. Unknown — no run data available
//  3. Compliant — last run succeeded with no changes
//  4. Drifted — last run succeeded but applied changes
//  5. Failing — last run failed or partially failed
//  6. Unknown — unrecognized run status (fallback)
func ComputeStatus(lastRun *RunInfo, lastSeen time.Time, baseInterval time.Duration) string {
	if time.Since(lastSeen) > 2*baseInterval {
		return "stale"
	}

	if lastRun == nil {
		return "unknown"
	}

	switch lastRun.Status {
	case "success":
		if lastRun.ChangedCount == 0 {
			return "compliant"
		}
		return "drifted"
	case "failure", "partial":
		return "failing"
	default:
		return "unknown"
	}
}

// Summary aggregates a slice of status strings into counts by status.
func Summary(statuses []string) map[string]int32 {
	counts := make(map[string]int32)
	for _, s := range statuses {
		counts[s]++
	}
	return counts
}

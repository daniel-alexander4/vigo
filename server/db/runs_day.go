// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package db

import "time"

// FailedRunsDayLimit caps ListFailedRunsForDate for one day. The MTTR job
// (its only caller) follows each row with a lookup, so an unbounded day — a
// fleet failing every run — can't turn into an unbounded scan + N queries.
const FailedRunsDayLimit = 10000

// FailedRunsDayParams returns the ListFailedRunsForDate bounds for the UTC
// calendar day containing t: [00:00:00, next day 00:00:00). runs.started_at is
// written by datetime('now'), which is UTC, so the day is a UTC day.
func FailedRunsDayParams(t time.Time, limit int64) ListFailedRunsForDateParams {
	u := t.UTC()
	start := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return ListFailedRunsForDateParams{
		DayStart: start,
		DayEnd:   start.AddDate(0, 0, 1),
		RowLimit: limit,
	}
}

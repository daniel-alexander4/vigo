// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package uifmt

import "time"

// ParseTimestamp parses a timestamp string emitted by grpc-gateway JSON or
// other API surfaces, accepting the several RFC3339 variants we see in
// practice. Returns the zero time and a non-nil error on no match.
//
// Formatting back to a string is per-surface: the CLI prefers
// "2006-01-02 15:04"; the UI applies its own freshness-aware formatting
// in ui/render.go. Each surface owns its rendering; this parser is the
// only piece worth sharing.
func ParseTimestamp(ts string) (time.Time, error) {
	var lastErr error
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

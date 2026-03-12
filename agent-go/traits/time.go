// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TimeCollector gathers local and UTC time traits: hour, weekday, day, month,
// year, and timezone. Useful for conditionals like maintenance windows or
// weekend-only tasks.
type TimeCollector struct{}

// Name returns "time".
func (c *TimeCollector) Name() string { return "time" }

// Collect returns time traits from the system clock.
func (c *TimeCollector) Collect(_ context.Context) (map[string]any, error) {
	now := time.Now()

	return map[string]any{
		"hour":     now.Hour(),
		"minute":   now.Minute(),
		"weekday":  strings.ToLower(now.Weekday().String()),
		"day":      now.Day(),
		"month":    int(now.Month()),
		"year":     now.Year(),
		"timezone": now.Location().String(),
		"utc_hour": now.UTC().Hour(),
		"clock":    fmt.Sprintf("%02d:%02d", now.Hour(), now.Minute()),
		"date":     now.Format("2006-01-02"),
	}, nil
}

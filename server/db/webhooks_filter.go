// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"encoding/json"
)

// ListActiveWebhooksByEvent returns active webhooks whose JSON-encoded events
// array includes the given event. SQLite's json_each is not supported by sqlc,
// so we filter in Go after fetching all active webhooks.
func (q *Queries) ListActiveWebhooksByEvent(ctx context.Context, event string) ([]Webhook, error) {
	all, err := q.ListActiveWebhooks(ctx)
	if err != nil {
		return nil, err
	}

	var matched []Webhook
	for _, wh := range all {
		var events []string
		if err := json.Unmarshal([]byte(wh.Events), &events); err != nil {
			continue // skip webhooks with malformed events
		}
		for _, e := range events {
			if e == event || e == "*" {
				matched = append(matched, wh)
				break
			}
		}
	}
	return matched, nil
}

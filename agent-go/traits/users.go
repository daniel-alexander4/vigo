// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// UsersCollector gathers logged-in user traits via the `who` command.
type UsersCollector struct{}

func (c *UsersCollector) Name() string { return "users" }

func (c *UsersCollector) Collect(ctx context.Context) (map[string]any, error) {
	runner := &cmdrun.Default{}
	out, err := runner.Run(ctx, "who")
	if err != nil {
		return map[string]any{
			"count": 0,
			"list":  []string{},
		}, nil
	}

	seen := make(map[string]bool)
	var users []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if !seen[name] {
			seen[name] = true
			users = append(users, name)
		}
	}

	if users == nil {
		users = []string{}
	}

	return map[string]any{
		"count": len(users),
		"list":  users,
	}, nil
}

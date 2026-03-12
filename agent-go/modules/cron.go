// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CronExecutor manages cron jobs idempotently via /etc/cron.d/ drop-in files.
// Each resource maps to a single file in /etc/cron.d/ named after the resource.
type CronExecutor struct{}

// Execute applies the desired cron state based on params.
// Supported params:
//   - name: cron job name, used as the filename in /etc/cron.d/ (required)
//   - job: the command to run (required for state=present)
//   - schedule: full cron expression e.g. "0 2 * * *" (optional, overrides individual fields)
//   - minute, hour, day, month, weekday: individual schedule fields (default "*")
//   - user: the user to run the job as (default "root")
//   - state: "present" (default) or "absent"
func (c *CronExecutor) Execute(_ context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	result := ResourceResult{
		ResourceType: "cron",
		ResourceName: name,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if name == "" {
		result.Error = fmt.Errorf("cron executor: name param is required")
		return result
	}

	// Sanitize name for use as a filename — only allow alphanumeric, dash, underscore.
	filename := sanitizeCronName(name)
	path := filepath.Join("/etc/cron.d", filename)

	state := params["state"]
	if state == "" {
		state = "present"
	}

	if state == "absent" {
		return c.handleAbsent(path, &result, dryRun)
	}

	job := params["job"]
	if job == "" {
		result.Error = fmt.Errorf("cron executor: job param is required when state=present")
		return result
	}

	schedule := buildSchedule(params)
	user := params["user"]
	if user == "" {
		user = "root"
	}

	content := fmt.Sprintf("# Managed by hobnob — do not edit\n%s %s %s\n", schedule, user, job)

	return c.handlePresent(path, content, &result, dryRun)
}

// handlePresent ensures the cron.d file exists with the desired content.
func (c *CronExecutor) handlePresent(path, content string, result *ResourceResult, dryRun bool) ResourceResult {
	desiredHash := sha256Hex([]byte(content))

	existing, err := os.ReadFile(path)
	if err == nil && sha256Hex(existing) == desiredHash {
		result.Action = ActionNone
		result.Changed = false
		return *result
	}

	result.Action = ActionUpdated
	if err != nil && os.IsNotExist(err) {
		result.Action = ActionCreated
	}
	result.Changed = true

	if dryRun {
		return *result
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		result.Error = fmt.Errorf("writing %s: %w", path, err)
		return *result
	}

	return *result
}

// handleAbsent ensures the cron.d file does not exist.
func (c *CronExecutor) handleAbsent(path string, result *ResourceResult, dryRun bool) ResourceResult {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		result.Action = ActionNone
		result.Changed = false
		return *result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return *result
	}

	if err := os.Remove(path); err != nil {
		result.Error = fmt.Errorf("removing %s: %w", path, err)
	}

	return *result
}

// buildSchedule returns a cron schedule string from params.
// If "schedule" is set, it is returned directly. Otherwise individual fields
// (minute, hour, day, month, weekday) are used, defaulting to "*".
func buildSchedule(params map[string]string) string {
	if s := params["schedule"]; s != "" {
		return s
	}
	fields := [5]string{"*", "*", "*", "*", "*"}
	keys := [5]string{"minute", "hour", "day", "month", "weekday"}
	for i, k := range keys {
		if v := params[k]; v != "" {
			fields[i] = v
		}
	}
	return strings.Join(fields[:], " ")
}

// sanitizeCronName converts a resource name to a valid /etc/cron.d/ filename.
// cron.d files must not contain dots or other special characters.
func sanitizeCronName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// sha256Hex is defined in file.go

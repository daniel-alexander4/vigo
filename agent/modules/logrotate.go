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

// LogrotateExecutor manages logrotate configuration files in /etc/logrotate.d/.
// Params:
//   - name: config name (used as filename in logrotate.d/)
//   - path: log file path or glob (e.g., /var/log/myapp/*.log)
//   - state: present (default) or absent
//   - frequency: daily, weekly, monthly, yearly (default: weekly)
//   - rotate: number of rotated files to keep (default: "4")
//   - compress: "true" (default) or "false"
//   - delaycompress: "true" (default) or "false"
//   - missingok: "true" (default) or "false"
//   - notifempty: "true" (default) or "false"
//   - create: file mode, owner, group (e.g., "0640 root adm")
//   - postrotate: script to run after rotation
//   - size: rotate based on size instead of time (e.g., "100M")
//   - maxage: remove rotated logs older than N days
//   - copytruncate: "true" or "false" (default)
type LogrotateExecutor struct {
	ConfDir string // override for testing (default /etc/logrotate.d)
}

func (e *LogrotateExecutor) confDir() string {
	if e.ConfDir != "" {
		return e.ConfDir
	}
	return "/etc/logrotate.d"
}

func (e *LogrotateExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	state := params["state"]
	if state == "" {
		state = "present"
	}

	result := ResourceResult{
		ResourceType: "logrotate",
		ResourceName: name,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if name == "" {
		result.Error = fmt.Errorf("logrotate executor: name param is required")
		return result
	}

	confPath := filepath.Join(e.confDir(), name)

	switch state {
	case "present":
		logPath := params["path"]
		if logPath == "" {
			result.Error = fmt.Errorf("logrotate executor: path param is required for state=present")
			return result
		}
		return e.ensurePresent(confPath, logPath, params, dryRun, result)
	case "absent":
		return e.ensureAbsent(confPath, dryRun, result)
	default:
		result.Error = fmt.Errorf("logrotate executor: unknown state %q", state)
		return result
	}
}

func (e *LogrotateExecutor) ensurePresent(confPath, logPath string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	desired := e.buildConfig(logPath, params)

	existing, err := os.ReadFile(confPath)
	if err == nil && string(existing) == desired {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if os.IsNotExist(err) {
		result.Action = ActionCreated
	} else {
		result.Action = ActionUpdated
	}

	if dryRun {
		return result
	}

	if err := os.MkdirAll(e.confDir(), 0755); err != nil {
		result.Error = fmt.Errorf("creating %s: %w", e.confDir(), err)
		return result
	}

	if err := os.WriteFile(confPath, []byte(desired), 0644); err != nil {
		result.Error = fmt.Errorf("writing %s: %w", confPath, err)
		return result
	}

	return result
}

func (e *LogrotateExecutor) ensureAbsent(confPath string, dryRun bool, result ResourceResult) ResourceResult {
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if !dryRun {
		if err := os.Remove(confPath); err != nil && !os.IsNotExist(err) {
			result.Error = fmt.Errorf("removing %s: %w", confPath, err)
		}
	}

	return result
}

func (e *LogrotateExecutor) buildConfig(logPath string, params map[string]string) string {
	frequency := params["frequency"]
	if frequency == "" {
		frequency = "weekly"
	}
	rotate := params["rotate"]
	if rotate == "" {
		rotate = "4"
	}

	var b strings.Builder
	b.WriteString("# Managed by Hobnob — do not edit manually\n")
	b.WriteString(logPath + " {\n")
	b.WriteString("    " + frequency + "\n")
	b.WriteString("    rotate " + rotate + "\n")

	if params["size"] != "" {
		b.WriteString("    size " + params["size"] + "\n")
	}
	if params["maxage"] != "" {
		b.WriteString("    maxage " + params["maxage"] + "\n")
	}
	if params["compress"] != "false" {
		b.WriteString("    compress\n")
	}
	if params["delaycompress"] != "false" {
		b.WriteString("    delaycompress\n")
	}
	if params["missingok"] != "false" {
		b.WriteString("    missingok\n")
	}
	if params["notifempty"] != "false" {
		b.WriteString("    notifempty\n")
	}
	if params["copytruncate"] == "true" {
		b.WriteString("    copytruncate\n")
	}
	if params["create"] != "" {
		b.WriteString("    create " + params["create"] + "\n")
	}
	if params["postrotate"] != "" {
		b.WriteString("    postrotate\n")
		b.WriteString("        " + params["postrotate"] + "\n")
		b.WriteString("    endscript\n")
	}
	b.WriteString("}\n")

	return b.String()
}

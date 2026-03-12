// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// BlockinfileExecutor inserts or replaces a marked block of text in a file.
// The block is delimited by marker lines that include a unique label.
// Params:
//   - path: target file path (required)
//   - block: the content to insert between markers (required for state=present)
//   - marker_begin: opening marker (default: "# BEGIN HOBNOB MANAGED BLOCK")
//   - marker_end: closing marker (default: "# END HOBNOB MANAGED BLOCK")
//   - label: unique label appended to markers (default: resource name)
//   - state: present (default) or absent
//   - create: "true" to create the file if it doesn't exist (default: "false")
//   - owner: file owner (only when creating)
//   - group: file group (only when creating)
//   - mode: file mode (only when creating, default: "0644")
type BlockinfileExecutor struct{}

func (e *BlockinfileExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	path := params["path"]
	block := params["block"]
	state := params["state"]
	if state == "" {
		state = "present"
	}
	label := params["label"]
	if label == "" {
		label = params["name"]
	}

	markerBegin := params["marker_begin"]
	if markerBegin == "" {
		markerBegin = "# BEGIN HOBNOB MANAGED BLOCK"
	}
	markerEnd := params["marker_end"]
	if markerEnd == "" {
		markerEnd = "# END HOBNOB MANAGED BLOCK"
	}

	// Append label to markers for uniqueness.
	if label != "" {
		markerBegin += " " + label
		markerEnd += " " + label
	}

	result := ResourceResult{
		ResourceType: "blockinfile",
		ResourceName: path,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if path == "" {
		result.Error = fmt.Errorf("blockinfile executor: path param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(path, block, markerBegin, markerEnd, params, dryRun, result)
	case "absent":
		return e.ensureAbsent(path, markerBegin, markerEnd, dryRun, result)
	default:
		result.Error = fmt.Errorf("blockinfile executor: unknown state %q", state)
		return result
	}
}

func (e *BlockinfileExecutor) ensurePresent(path, block, markerBegin, markerEnd string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			result.Error = fmt.Errorf("reading %s: %w", path, err)
			return result
		}
		if params["create"] != "true" {
			result.Error = fmt.Errorf("blockinfile executor: %s does not exist (set create=true to create)", path)
			return result
		}
		content = nil
	}

	fullBlock := markerBegin + "\n" + block + "\n" + markerEnd

	existing := string(content)
	beginIdx := strings.Index(existing, markerBegin)
	endIdx := strings.Index(existing, markerEnd)

	var newContent string
	if beginIdx >= 0 && endIdx >= 0 && endIdx > beginIdx {
		// Replace existing block.
		newContent = existing[:beginIdx] + fullBlock + existing[endIdx+len(markerEnd):]
	} else {
		// Append block.
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			newContent = existing + "\n" + fullBlock + "\n"
		} else {
			newContent = existing + fullBlock + "\n"
		}
	}

	if newContent == existing {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if content == nil {
		result.Action = ActionCreated
	} else if beginIdx >= 0 && endIdx >= 0 {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionUpdated
	}

	if dryRun {
		return result
	}

	mode := os.FileMode(0644)
	if params["mode"] != "" {
		if m, err := parseFileMode(params["mode"]); err == nil {
			mode = m
		}
	}

	if err := os.WriteFile(path, []byte(newContent), mode); err != nil {
		result.Error = fmt.Errorf("writing %s: %w", path, err)
	}

	return result
}

func (e *BlockinfileExecutor) ensureAbsent(path, markerBegin, markerEnd string, dryRun bool, result ResourceResult) ResourceResult {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Action = ActionNone
			return result
		}
		result.Error = fmt.Errorf("reading %s: %w", path, err)
		return result
	}

	existing := string(content)
	beginIdx := strings.Index(existing, markerBegin)
	endIdx := strings.Index(existing, markerEnd)

	if beginIdx < 0 || endIdx < 0 || endIdx < beginIdx {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	// Remove the block including markers and trailing newline.
	end := endIdx + len(markerEnd)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	// Also remove leading newline if the block starts at a line boundary.
	start := beginIdx
	if start > 0 && existing[start-1] == '\n' {
		start--
	}

	newContent := existing[:start] + existing[end:]

	info, _ := os.Stat(path)
	mode := os.FileMode(0644)
	if info != nil {
		mode = info.Mode()
	}

	if err := os.WriteFile(path, []byte(newContent), mode); err != nil {
		result.Error = fmt.Errorf("writing %s: %w", path, err)
	}

	return result
}

// parseFileMode converts a string like "0644" to os.FileMode.
func parseFileMode(s string) (os.FileMode, error) {
	var mode uint32
	if _, err := fmt.Sscanf(s, "%o", &mode); err != nil {
		return 0, err
	}
	return os.FileMode(mode), nil
}

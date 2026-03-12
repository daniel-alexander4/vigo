// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FileLineExecutor manages individual lines in a file without owning the whole
// file. It can ensure a line is present (appended, prepended, or inserted after
// a pattern) or absent, and can replace lines matching a regex.
type FileLineExecutor struct{}

// Execute applies the desired line state based on params. Supported params:
//   - path: target file path (required)
//   - line: the line content to ensure (required for state=present)
//   - match: regex pattern to match existing lines for replacement (optional)
//   - state: "present" (default) or "absent"
//   - position: "end" (default), "begin", or "after:<pattern>" — where to insert if not found
func (f *FileLineExecutor) Execute(_ context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	result := ResourceResult{
		ResourceType: "file_line",
		ResourceName: params["path"],
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	path := params["path"]
	if path == "" {
		result.Error = fmt.Errorf("file_line executor: path param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}

	switch state {
	case "present":
		return f.handlePresent(path, params, dryRun, result)
	case "absent":
		return f.handleAbsent(path, params, dryRun, result)
	default:
		result.Error = fmt.Errorf("file_line executor: unknown state %q", state)
		return result
	}
}

// handlePresent ensures a line exists in the file.
func (f *FileLineExecutor) handlePresent(path string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	line := params["line"]
	if line == "" {
		result.Error = fmt.Errorf("file_line executor: line param is required for state=present")
		return result
	}

	matchPattern := params["match"]
	position := params["position"]
	if position == "" {
		position = "end"
	}

	// Read existing file content. If file doesn't exist, start with empty.
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			result.Error = fmt.Errorf("read %s: %w", path, err)
			return result
		}
		// File doesn't exist — we'll create it.
		content = nil
	}

	var lines []string
	trailingNewline := false
	if len(content) > 0 {
		raw := string(content)
		trailingNewline = strings.HasSuffix(raw, "\n")
		lines = strings.Split(raw, "\n")
		// If there's a trailing newline, Split produces an empty final element.
		if trailingNewline && len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	}

	if matchPattern != "" {
		return f.handlePresentWithMatch(path, line, matchPattern, position, lines, trailingNewline, dryRun, result)
	}

	return f.handlePresentNoMatch(path, line, position, lines, trailingNewline, dryRun, result)
}

// handlePresentNoMatch ensures the exact line exists somewhere in the file.
func (f *FileLineExecutor) handlePresentNoMatch(path, line, position string, lines []string, trailingNewline bool, dryRun bool, result ResourceResult) ResourceResult {
	// Check if line already exists.
	for _, l := range lines {
		if l == line {
			result.Action = ActionNone
			result.Changed = false
			return result
		}
	}

	// Line not found — insert it.
	result.Action = ActionCreated
	result.Changed = true

	if dryRun {
		return result
	}

	lines = insertLine(lines, line, position)
	return f.writeLines(path, lines, true, result)
}

// handlePresentWithMatch finds a line matching the regex and replaces or inserts.
func (f *FileLineExecutor) handlePresentWithMatch(path, line, matchPattern, position string, lines []string, trailingNewline bool, dryRun bool, result ResourceResult) ResourceResult {
	re, err := regexp.Compile(matchPattern)
	if err != nil {
		result.Error = fmt.Errorf("file_line executor: invalid match regex %q: %w", matchPattern, err)
		return result
	}

	matchIdx := -1
	for i, l := range lines {
		if re.MatchString(l) {
			matchIdx = i
			break
		}
	}

	if matchIdx >= 0 {
		// Found a matching line.
		if lines[matchIdx] == line {
			result.Action = ActionNone
			result.Changed = false
			return result
		}
		// Replace it.
		result.Action = ActionUpdated
		result.Changed = true
		if dryRun {
			return result
		}
		lines[matchIdx] = line
		return f.writeLines(path, lines, trailingNewline || true, result)
	}

	// No match found — insert the line.
	result.Action = ActionCreated
	result.Changed = true
	if dryRun {
		return result
	}
	lines = insertLine(lines, line, position)
	return f.writeLines(path, lines, true, result)
}

// handleAbsent removes lines matching the exact line text or the match regex.
func (f *FileLineExecutor) handleAbsent(path string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Action = ActionNone
			result.Changed = false
			return result
		}
		result.Error = fmt.Errorf("read %s: %w", path, err)
		return result
	}

	raw := string(content)
	trailingNewline := strings.HasSuffix(raw, "\n")
	lines := strings.Split(raw, "\n")
	if trailingNewline && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	matchPattern := params["match"]
	exactLine := params["line"]

	var re *regexp.Regexp
	if matchPattern != "" {
		re, err = regexp.Compile(matchPattern)
		if err != nil {
			result.Error = fmt.Errorf("file_line executor: invalid match regex %q: %w", matchPattern, err)
			return result
		}
	}

	var kept []string
	removed := 0
	for _, l := range lines {
		shouldRemove := false
		if re != nil && re.MatchString(l) {
			shouldRemove = true
		}
		if exactLine != "" && l == exactLine {
			shouldRemove = true
		}
		if shouldRemove {
			removed++
		} else {
			kept = append(kept, l)
		}
	}

	if removed == 0 {
		result.Action = ActionNone
		result.Changed = false
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	return f.writeLines(path, kept, trailingNewline, result)
}

// writeLines joins the lines and writes the file, preserving a trailing newline.
func (f *FileLineExecutor) writeLines(path string, lines []string, trailingNewline bool, result ResourceResult) ResourceResult {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		result.Error = fmt.Errorf("create parent dir %s: %w", dir, err)
		return result
	}

	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}

	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		result.Error = fmt.Errorf("write %s: %w", path, err)
		return result
	}

	return result
}

// insertLine inserts a line into the slice at the specified position.
func insertLine(lines []string, line, position string) []string {
	switch {
	case position == "begin":
		return append([]string{line}, lines...)

	case strings.HasPrefix(position, "after:"):
		pattern := strings.TrimPrefix(position, "after:")
		re, err := regexp.Compile(pattern)
		if err != nil {
			// Fall back to end if pattern is invalid.
			return append(lines, line)
		}
		for i, l := range lines {
			if re.MatchString(l) {
				// Insert after this line.
				result := make([]string, 0, len(lines)+1)
				result = append(result, lines[:i+1]...)
				result = append(result, line)
				result = append(result, lines[i+1:]...)
				return result
			}
		}
		// Pattern not found — append at end.
		return append(lines, line)

	default: // "end" or anything else
		return append(lines, line)
	}
}

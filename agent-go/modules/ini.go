// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// INIExecutor manages individual keys in INI-style config files without owning
// the whole file. It can set, update, or remove keys within specific sections.
//
// Params:
//   - path: target file path (required)
//   - section: INI section name (empty string = global/top-level)
//   - key: the key to manage (required)
//   - value: desired value (required for state=present)
//   - state: "present" (default) or "absent"
//   - separator: key-value separator (default "="); common alternatives: ": ", " = "
type INIExecutor struct{}

// Execute applies the desired INI key state.
func (e *INIExecutor) Execute(_ context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	result := ResourceResult{
		ResourceType: "ini",
		ResourceName: params["path"],
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	path := params["path"]
	if path == "" {
		result.Error = fmt.Errorf("ini executor: path param is required")
		return result
	}

	key := params["key"]
	if key == "" {
		result.Error = fmt.Errorf("ini executor: key param is required")
		return result
	}

	section := params["section"]
	state := params["state"]
	if state == "" {
		state = "present"
	}

	sep := params["separator"]
	if sep == "" {
		sep = "="
	}

	switch state {
	case "present":
		value := params["value"]
		if value == "" {
			// Allow empty string as a valid value — check if param exists.
			if _, ok := params["value"]; !ok {
				result.Error = fmt.Errorf("ini executor: value param is required for state=present")
				return result
			}
		}
		return e.ensurePresent(path, section, key, params["value"], sep, dryRun, result)
	case "absent":
		return e.ensureAbsent(path, section, key, dryRun, result)
	default:
		result.Error = fmt.Errorf("ini executor: unknown state %q", state)
		return result
	}
}

func (e *INIExecutor) ensurePresent(path, section, key, value, sep string, dryRun bool, result ResourceResult) ResourceResult {
	ini, err := parseINI(path)
	if err != nil && !os.IsNotExist(err) {
		result.Error = err
		return result
	}
	if ini == nil {
		ini = &iniFile{}
	}

	current, exists := ini.get(section, key)
	desired := key + sep + value

	if exists && current == desired {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if exists {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionCreated
	}

	if dryRun {
		return result
	}

	ini.set(section, key, desired)
	if err := ini.write(path); err != nil {
		result.Error = err
	}
	return result
}

func (e *INIExecutor) ensureAbsent(path, section, key string, dryRun bool, result ResourceResult) ResourceResult {
	ini, err := parseINI(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Action = ActionNone
			return result
		}
		result.Error = err
		return result
	}

	if _, exists := ini.get(section, key); !exists {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	ini.remove(section, key)
	if err := ini.write(path); err != nil {
		result.Error = err
	}
	return result
}

// iniFile represents a parsed INI file that preserves ordering, comments, and
// blank lines. Each element is either a raw line (comment/blank) or a key-value
// pair within a section.
type iniFile struct {
	// elements preserves the file in order. Each element is one of:
	// - iniComment (comment or blank line)
	// - iniSection (section header like [foo])
	// - iniKeyValue (key=value line within a section)
	elements []iniElement
}

type iniElement interface {
	iniLine() string
}

type iniComment struct{ line string }

func (c iniComment) iniLine() string { return c.line }

type iniSection struct{ name string }

func (s iniSection) iniLine() string { return "[" + s.name + "]" }

type iniKeyValue struct {
	key  string
	line string // full line as written (e.g., "key = value")
}

func (kv iniKeyValue) iniLine() string { return kv.line }

// parseINI reads an INI file into a structure that preserves formatting.
// If the file doesn't exist, returns an empty structure (not an error) for
// state=present (caller creates the file). For state=absent, the caller
// should check os.IsNotExist.
func parseINI(path string) (*iniFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &iniFile{}, err
		}
		return nil, fmt.Errorf("ini executor: read %s: %w", path, err)
	}

	ini := &iniFile{}
	lines := strings.Split(string(data), "\n")

	// Drop trailing empty element from trailing newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Blank or comment line.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			ini.elements = append(ini.elements, iniComment{line: line})
			continue
		}

		// Section header.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := trimmed[1 : len(trimmed)-1]
			ini.elements = append(ini.elements, iniSection{name: strings.TrimSpace(name)})
			continue
		}

		// Key-value pair — extract just the key for lookups.
		key := extractINIKey(trimmed)
		ini.elements = append(ini.elements, iniKeyValue{key: key, line: line})
	}

	return ini, nil
}

// extractINIKey returns the key portion of a "key=value" or "key: value" line.
func extractINIKey(line string) string {
	for i, ch := range line {
		if ch == '=' || ch == ':' {
			return strings.TrimSpace(line[:i])
		}
	}
	// No separator found — treat entire line as key.
	return strings.TrimSpace(line)
}

// currentSection returns the section name that applies at position i.
func (ini *iniFile) currentSection(i int) string {
	for j := i - 1; j >= 0; j-- {
		if s, ok := ini.elements[j].(iniSection); ok {
			return s.name
		}
	}
	return "" // global section
}

// get returns the full line and whether the key exists in the given section.
func (ini *iniFile) get(section, key string) (string, bool) {
	for i, elem := range ini.elements {
		kv, ok := elem.(iniKeyValue)
		if !ok {
			continue
		}
		if kv.key == key && ini.currentSection(i) == section {
			return kv.line, true
		}
	}
	return "", false
}

// set updates or inserts a key in the given section.
func (ini *iniFile) set(section, key, fullLine string) {
	// Try to update existing key.
	for i, elem := range ini.elements {
		kv, ok := elem.(iniKeyValue)
		if !ok {
			continue
		}
		if kv.key == key && ini.currentSection(i) == section {
			ini.elements[i] = iniKeyValue{key: key, line: fullLine}
			return
		}
	}

	// Key doesn't exist — insert it at the end of the section.
	if section == "" {
		// Global section: insert before the first [section] header.
		insertAt := 0
		for i, elem := range ini.elements {
			if _, ok := elem.(iniSection); ok {
				insertAt = i
				break
			}
			insertAt = i + 1
		}
		ini.insertAt(insertAt, iniKeyValue{key: key, line: fullLine})
		return
	}

	// Find the section header, then find the last line in that section.
	sectionIdx := -1
	for i, elem := range ini.elements {
		if s, ok := elem.(iniSection); ok && s.name == section {
			sectionIdx = i
			break
		}
	}

	if sectionIdx < 0 {
		// Section doesn't exist — create it.
		ini.elements = append(ini.elements, iniSection{name: section})
		ini.elements = append(ini.elements, iniKeyValue{key: key, line: fullLine})
		return
	}

	// Find the end of this section (before next section header or EOF).
	insertAt := len(ini.elements)
	for i := sectionIdx + 1; i < len(ini.elements); i++ {
		if _, ok := ini.elements[i].(iniSection); ok {
			insertAt = i
			break
		}
	}

	ini.insertAt(insertAt, iniKeyValue{key: key, line: fullLine})
}

// remove deletes a key from the given section.
func (ini *iniFile) remove(section, key string) {
	for i, elem := range ini.elements {
		kv, ok := elem.(iniKeyValue)
		if !ok {
			continue
		}
		if kv.key == key && ini.currentSection(i) == section {
			ini.elements = append(ini.elements[:i], ini.elements[i+1:]...)
			return
		}
	}
}

func (ini *iniFile) insertAt(idx int, elem iniElement) {
	ini.elements = append(ini.elements, nil)
	copy(ini.elements[idx+1:], ini.elements[idx:])
	ini.elements[idx] = elem
}

// write serializes the INI file back to disk.
func (ini *iniFile) write(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("ini executor: create parent dir %s: %w", dir, err)
	}

	var b strings.Builder
	for _, elem := range ini.elements {
		b.WriteString(elem.iniLine())
		b.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("ini executor: write %s: %w", path, err)
	}
	return nil
}

// sortedSectionKeys returns all key names in a section, sorted. Used for testing.
func (ini *iniFile) sortedSectionKeys(section string) []string {
	var keys []string
	for i, elem := range ini.elements {
		kv, ok := elem.(iniKeyValue)
		if !ok {
			continue
		}
		if ini.currentSection(i) == section {
			keys = append(keys, kv.key)
		}
	}
	sort.Strings(keys)
	return keys
}

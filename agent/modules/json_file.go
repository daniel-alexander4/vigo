package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// JSONFileExecutor manages individual keys in JSON files without owning the
// whole file. It can set, update, or remove keys at any depth using dot-notation
// paths (e.g., "database.host" or "servers.0.name").
//
// Params:
//   - path: target file path (required)
//   - key: dot-notation path to the key (required), e.g. "database.host"
//   - value: desired value as a JSON literal (required for state=present)
//     Strings must be quoted: '"localhost"'. Numbers/bools are bare: '8080', 'true'.
//   - state: "present" (default) or "absent"
//   - indent: indentation string (default "  ", two spaces)
type JSONFileExecutor struct{}

// Execute applies the desired JSON key state.
func (e *JSONFileExecutor) Execute(_ context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	result := ResourceResult{
		ResourceType: "json_file",
		ResourceName: params["path"],
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	path := params["path"]
	if path == "" {
		result.Error = fmt.Errorf("json_file executor: path param is required")
		return result
	}

	key := params["key"]
	if key == "" {
		result.Error = fmt.Errorf("json_file executor: key param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}

	indent := params["indent"]
	if indent == "" {
		indent = "  "
	}

	switch state {
	case "present":
		valueStr, ok := params["value"]
		if !ok {
			result.Error = fmt.Errorf("json_file executor: value param is required for state=present")
			return result
		}
		return e.ensurePresent(path, key, valueStr, indent, dryRun, result)
	case "absent":
		return e.ensureAbsent(path, key, indent, dryRun, result)
	default:
		result.Error = fmt.Errorf("json_file executor: unknown state %q", state)
		return result
	}
}

func (e *JSONFileExecutor) ensurePresent(path, key, valueStr, indent string, dryRun bool, result ResourceResult) ResourceResult {
	// Parse the desired value.
	var desiredValue any
	if err := json.Unmarshal([]byte(valueStr), &desiredValue); err != nil {
		result.Error = fmt.Errorf("json_file executor: invalid JSON value %q: %w", valueStr, err)
		return result
	}

	// Read existing file or start with empty object.
	doc, err := readJSONFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			result.Error = fmt.Errorf("json_file executor: %w", err)
			return result
		}
		doc = make(map[string]any)
	}

	parts := splitJSONKey(key)

	// Check current value.
	current, exists := getJSONPath(doc, parts)
	if exists && jsonEqual(current, desiredValue) {
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

	setJSONPath(doc, parts, desiredValue)
	if err := writeJSONFile(path, doc, indent); err != nil {
		result.Error = err
	}
	return result
}

func (e *JSONFileExecutor) ensureAbsent(path, key, indent string, dryRun bool, result ResourceResult) ResourceResult {
	doc, err := readJSONFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Action = ActionNone
			return result
		}
		result.Error = fmt.Errorf("json_file executor: %w", err)
		return result
	}

	parts := splitJSONKey(key)
	if _, exists := getJSONPath(doc, parts); !exists {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	deleteJSONPath(doc, parts)
	if err := writeJSONFile(path, doc, indent); err != nil {
		result.Error = err
	}
	return result
}

func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, nil
}

func writeJSONFile(path string, doc map[string]any, indent string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("json_file executor: create parent dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(doc, "", indent)
	if err != nil {
		return fmt.Errorf("json_file executor: marshal: %w", err)
	}
	// Add trailing newline.
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("json_file executor: write %s: %w", path, err)
	}
	return nil
}

// splitJSONKey splits a dot-notation key into path segments.
// Numeric segments are kept as strings — the getter/setter checks if the
// parent is a slice to decide whether to index by number.
func splitJSONKey(key string) []string {
	return strings.Split(key, ".")
}

// getJSONPath traverses a nested map/slice to get a value at the given path.
func getJSONPath(doc any, parts []string) (any, bool) {
	current := doc
	for _, part := range parts {
		switch v := current.(type) {
		case map[string]any:
			val, ok := v[part]
			if !ok {
				return nil, false
			}
			current = val
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, false
			}
			current = v[idx]
		default:
			return nil, false
		}
	}
	return current, true
}

// setJSONPath sets a value at the given dot-notation path, creating
// intermediate maps as needed.
func setJSONPath(doc map[string]any, parts []string, value any) {
	current := any(doc)
	for i, part := range parts[:len(parts)-1] {
		switch v := current.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				// Create intermediate map.
				newMap := make(map[string]any)
				v[part] = newMap
				current = newMap
			} else {
				current = next
			}
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(v) {
				// Can't traverse further — bail.
				return
			}
			current = v[idx]
		default:
			// Path segment doesn't match structure — overwrite with map.
			newMap := make(map[string]any)
			// Walk back up to set this.
			if i > 0 {
				parent := any(doc)
				for _, p := range parts[:i] {
					if m, ok := parent.(map[string]any); ok {
						parent = m[p]
					}
				}
				if m, ok := parent.(map[string]any); ok {
					m[parts[i-1]] = newMap
				}
			}
			current = newMap
		}
	}

	// Set the final key.
	lastKey := parts[len(parts)-1]
	switch v := current.(type) {
	case map[string]any:
		v[lastKey] = value
	case []any:
		idx, err := strconv.Atoi(lastKey)
		if err != nil || idx < 0 || idx >= len(v) {
			return
		}
		v[idx] = value
	}
}

// deleteJSONPath removes a key at the given dot-notation path.
func deleteJSONPath(doc map[string]any, parts []string) {
	if len(parts) == 1 {
		delete(doc, parts[0])
		return
	}

	current := any(doc)
	for _, part := range parts[:len(parts)-1] {
		switch v := current.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return
			}
			current = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(v) {
				return
			}
			current = v[idx]
		default:
			return
		}
	}

	lastKey := parts[len(parts)-1]
	if m, ok := current.(map[string]any); ok {
		delete(m, lastKey)
	}
}

// jsonEqual compares two JSON values for deep equality.
func jsonEqual(a, b any) bool {
	ja, err := json.Marshal(a)
	if err != nil {
		return false
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ja) == string(jb)
}

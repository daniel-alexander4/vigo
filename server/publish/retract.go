// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Module retraction: detect removed modules during config publish and generate
// idempotent retract modules. Pure functions with no server dependencies —
// importable by both the server (for AI enhancement) and the CLI.

package publish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daniel-alexander4/vigo/server/modlint"
	"gopkg.in/yaml.v3"
)

// RetractedModule holds the generated retract module and metadata.
type RetractedModule struct {
	// OriginalName is the module name that was removed (e.g., "nginx").
	OriginalName string
	// RelPath is the relative path in the config tree (e.g., "modules/nginx.vgo").
	RelPath string
	// RetractYAML is the generated retract module content.
	RetractYAML string
	// Warnings are per-resource notes for operator review.
	Warnings []RetractWarning
}

// RetractWarning describes a resource that needs manual review.
type RetractWarning struct {
	Resource   string // resource name
	Type       string // resource type
	Confidence string // "high", "medium", "low", "skip"
	Message    string // human-readable explanation
}

// retractModule is a minimal module structure for parsing and generating YAML.
type retractModule struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	Vars        map[string]any    `yaml:"vars,omitempty"`
	Compliance  map[string]any    `yaml:"compliance,omitempty"`
	Resources   []retractResource `yaml:"resources"`
}

// retractResource preserves unknown attributes via inline map.
type retractResource struct {
	Name       string         `yaml:"name"`
	Type       string         `yaml:"type"`
	DependsOn  []string       `yaml:"depends_on,omitempty"`
	Notify     []string       `yaml:"notify,omitempty"`
	Subscribes []string       `yaml:"subscribes,omitempty"`
	Before     []string       `yaml:"before,omitempty"`
	When       string         `yaml:"when,omitempty"`
	Attrs      map[string]any `yaml:",inline"`
}

// DetectRemovedModules filters a list of removed file paths for module .vgo files.
func DetectRemovedModules(removed []string) []string {
	var modules []string
	for _, rel := range removed {
		if strings.HasPrefix(rel, "modules"+string(os.PathSeparator)) && strings.HasSuffix(rel, ".vgo") {
			modules = append(modules, rel)
		}
	}
	return modules
}

// ReadRemovedModuleContent reads the content of removed modules from the live
// directory before sync overwrites it. Returns a map of relPath → content.
func ReadRemovedModuleContent(liveDir string, relPaths []string) map[string][]byte {
	content := make(map[string][]byte, len(relPaths))
	for _, rel := range relPaths {
		data, err := os.ReadFile(filepath.Join(liveDir, rel))
		if err == nil && len(data) > 0 {
			content[rel] = data
		}
	}
	return content
}

// GenerateRetractModule creates an idempotent retract module from the
// original module content. Each resource is reversed to its absent/stopped state.
// Returns the generated YAML and a list of warnings for resources that need review.
func GenerateRetractModule(name string, content []byte) (string, []RetractWarning, error) {
	var mod retractModule
	if err := yaml.Unmarshal(content, &mod); err != nil {
		return "", nil, fmt.Errorf("parsing module %s: %w", name, err)
	}

	if len(mod.Resources) == 0 {
		return "", nil, fmt.Errorf("module %s has no resources", name)
	}

	var warnings []RetractWarning
	var reversed []retractResource

	// Process resources in reverse order so services stop before their
	// config files are removed, packages are removed before repos, etc.
	for i := len(mod.Resources) - 1; i >= 0; i-- {
		res := mod.Resources[i]
		retracted, warn := reverseResource(res)
		if warn != nil {
			warnings = append(warnings, *warn)
		}
		if retracted != nil {
			reversed = append(reversed, *retracted)
		}
	}

	if len(reversed) == 0 {
		return "", warnings, fmt.Errorf("module %s: no resources could be reversed", name)
	}

	retractMod := retractModule{
		Name:        name + "-retract",
		Description: fmt.Sprintf("Auto-generated retract module for %s. Review before activating.", name),
		Resources:   reversed,
	}

	out, err := yaml.Marshal(&retractMod)
	if err != nil {
		return "", warnings, fmt.Errorf("marshaling retract module: %w", err)
	}

	// Run through modlint for validation and normalization.
	result := modlint.Lint(out, modlint.Options{})
	if result.OK() {
		return result.Output, warnings, nil
	}

	// If lint errors, return the raw YAML with lint warnings appended.
	for _, e := range result.Errors {
		warnings = append(warnings, RetractWarning{
			Resource:   "",
			Type:       "",
			Confidence: "low",
			Message:    "lint: " + e,
		})
	}
	return string(out), warnings, nil
}

// reverseResource generates the retract version of a resource. Returns nil
// if the resource type cannot be reversed (e.g., unknown type).
func reverseResource(res retractResource) (*retractResource, *RetractWarning) {
	switch res.Type {
	case "file", "directory", "symlink":
		return reverseFileResource(res)
	case "package":
		return reversePackageResource(res)
	case "service", "service_macos", "service_freebsd", "service_openbsd",
		"service_netbsd", "service_illumos", "service_windows":
		return reverseServiceResource(res)
	case "user", "user_macos", "user_freebsd", "user_openbsd", "user_windows":
		return reverseUserResource(res)
	case "cron", "cron_windows":
		return reverseCronResource(res)
	case "repository":
		return reverseRepositoryResource(res)
	case "sysctl", "sysctl_bsd":
		return reverseSysctlResource(res)
	case "firewall", "firewall_pf", "firewall_windows":
		return reverseFirewallResource(res)
	case "source_package":
		return reverseSourcePackageResource(res)
	case "exec":
		return reverseExecResource(res)
	default:
		return nil, &RetractWarning{
			Resource:   res.Name,
			Type:       res.Type,
			Confidence: "skip",
			Message:    fmt.Sprintf("unknown resource type %q — skipped, manual cleanup required", res.Type),
		}
	}
}

func reverseFileResource(res retractResource) (*retractResource, *RetractWarning) {
	targetPath, _ := res.Attrs["target_path"].(string)
	if targetPath == "" {
		return nil, &RetractWarning{
			Resource: res.Name, Type: res.Type, Confidence: "skip",
			Message: "no target_path — cannot reverse",
		}
	}
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  res.Type,
		When:  res.When,
		Attrs: map[string]any{"target_path": targetPath, "state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: res.Type, Confidence: "high",
		Message: fmt.Sprintf("delete %s", targetPath),
	}
}

func reversePackageResource(res retractResource) (*retractResource, *RetractWarning) {
	pkg, _ := res.Attrs["package"].(string)
	if pkg == "" {
		return nil, nil
	}
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  "package",
		When:  res.When,
		Attrs: map[string]any{"package": pkg, "state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: "package", Confidence: "high",
		Message: fmt.Sprintf("remove %s", pkg),
	}
}

func reverseServiceResource(res retractResource) (*retractResource, *RetractWarning) {
	svc, _ := res.Attrs["service"].(string)
	if svc == "" {
		return nil, nil
	}
	return &retractResource{
		Name:  "stop-" + res.Name,
		Type:  res.Type,
		When:  res.When,
		Attrs: map[string]any{"service": svc, "state": "stopped", "enabled": false},
	}, &RetractWarning{
		Resource: res.Name, Type: res.Type, Confidence: "high",
		Message: fmt.Sprintf("stop and disable %s", svc),
	}
}

func reverseUserResource(res retractResource) (*retractResource, *RetractWarning) {
	username, _ := res.Attrs["username"].(string)
	if username == "" {
		return nil, nil
	}
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  res.Type,
		When:  res.When,
		Attrs: map[string]any{"username": username, "state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: res.Type, Confidence: "medium",
		Message: fmt.Sprintf("remove user %s (home directory may contain data)", username),
	}
}

func reverseCronResource(res retractResource) (*retractResource, *RetractWarning) {
	// Cron resources use the resource name as the cron job identifier.
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  res.Type,
		When:  res.When,
		Attrs: map[string]any{"state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: res.Type, Confidence: "high",
		Message: "remove cron job",
	}
}

func reverseRepositoryResource(res retractResource) (*retractResource, *RetractWarning) {
	repo, _ := res.Attrs["repository"].(string)
	if repo == "" {
		return nil, nil
	}
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  "repository",
		When:  res.When,
		Attrs: map[string]any{"repository": repo, "state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: "repository", Confidence: "high",
		Message: "remove package repository",
	}
}

func reverseSysctlResource(res retractResource) (*retractResource, *RetractWarning) {
	key, _ := res.Attrs["key"].(string)
	if key == "" {
		return nil, nil
	}
	return &retractResource{
		Name:  "reset-" + res.Name,
		Type:  res.Type,
		When:  res.When,
		Attrs: map[string]any{"key": key, "state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: res.Type, Confidence: "high",
		Message: fmt.Sprintf("reset %s to system default", key),
	}
}

func reverseFirewallResource(res retractResource) (*retractResource, *RetractWarning) {
	// Firewall rules need the original rule parameters to identify and remove.
	attrs := map[string]any{"state": "absent"}
	for _, key := range []string{"port", "proto", "action", "from", "to", "comment", "direction", "interface"} {
		if v, ok := res.Attrs[key]; ok {
			attrs[key] = v
		}
	}
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  res.Type,
		When:  res.When,
		Attrs: attrs,
	}, &RetractWarning{
		Resource: res.Name, Type: res.Type, Confidence: "high",
		Message: "remove firewall rule",
	}
}

func reverseSourcePackageResource(res retractResource) (*retractResource, *RetractWarning) {
	targetPath, _ := res.Attrs["target_path"].(string)
	if targetPath == "" {
		return nil, &RetractWarning{
			Resource: res.Name, Type: "source_package", Confidence: "skip",
			Message: "no target_path — cannot reverse",
		}
	}
	return &retractResource{
		Name:  "remove-" + res.Name,
		Type:  "file",
		When:  res.When,
		Attrs: map[string]any{"target_path": targetPath, "state": "absent"},
	}, &RetractWarning{
		Resource: res.Name, Type: "source_package", Confidence: "medium",
		Message: fmt.Sprintf("delete %s (extracted contents not removed)", targetPath),
	}
}

func reverseExecResource(res retractResource) (*retractResource, *RetractWarning) {
	cmd, _ := res.Attrs["command"].(string)
	if cmd == "" {
		return nil, nil
	}
	// Exec resources have no generic reverse. Return a placeholder that the
	// AI will improve, or that the operator can edit manually.
	return &retractResource{
		Name: "undo-" + res.Name,
		Type: "exec",
		When: res.When,
		Attrs: map[string]any{
			"command": fmt.Sprintf("echo 'TODO: reverse of: %s'", sanitizeForShell(cmd)),
			"onlyif":  "false",
		},
	}, &RetractWarning{
		Resource:   res.Name,
		Type:       "exec",
		Confidence: "low",
		Message:    fmt.Sprintf("exec %q — reversal unknown, AI will attempt to generate", cmd),
	}
}

// sanitizeForShell escapes single quotes in a string for use in shell commands.
func sanitizeForShell(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hobnob/hobnob/server/conditionals"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/secrets"
	"github.com/hobnob/hobnob/server/validate"
	"gopkg.in/yaml.v3"
)

// SecretError describes a single secret resolution failure with enough
// context to diagnose and fix without guessing.
type SecretError struct {
	Match    string // thrall match pattern (e.g. "db-*.prod.example.com")
	Module   string // module name (empty if failure is in thrall vars)
	Resource string // resource name (empty if failure is in vars)
	Attr     string // attribute or var name
	Secret   string // the secret key that failed
	Err      error  // underlying error from provider
	Backend  string // secrets backend name ("local", "isopass")
}

func (e *SecretError) Error() string {
	loc := e.Match
	if e.Module != "" {
		loc += " > " + e.Module
	}
	if e.Resource != "" {
		loc += " > " + e.Resource
	}
	return fmt.Sprintf("%s: secret %q (%s): %v", loc, e.Secret, e.Attr, e.Err)
}

// Hint returns an actionable fix suggestion based on the error message and backend.
func (e *SecretError) Hint() string {
	msg := e.Err.Error()

	if strings.Contains(msg, "must use \"secret:\" prefix") {
		return fmt.Sprintf("use \"secret:<path>\" instead of a plaintext value for %s", e.Attr)
	}

	switch e.Backend {
	case "local", "":
		switch {
		case strings.Contains(msg, "not found"):
			return fmt.Sprintf("hobnobcli secrets set %s", e.Secret)
		case strings.Contains(msg, "wrong master key"), strings.Contains(msg, "message authentication failed"):
			return "secret was written with a different key -- re-encrypt with: hobnobcli secrets set " + e.Secret
		case strings.Contains(msg, "ciphertext too short"):
			return "file may be plaintext from a previous config -- re-encrypt with: hobnobcli secrets set " + e.Secret
		default:
			return fmt.Sprintf("check %s in the secrets directory", e.Secret)
		}
	case "isopass":
		switch {
		case strings.Contains(msg, "not found"):
			return fmt.Sprintf("create this secret in isopass: isopass set %s", e.Secret)
		case strings.Contains(msg, "unreachable"):
			return "ensure isopass is running and HOBNOB_ISOPASS_URL is correct"
		case strings.Contains(msg, "authentication failed"):
			return "check HOBNOB_ISOPASS_TOKEN -- regenerate if expired"
		default:
			return "check isopass logs for details"
		}
	default:
		return "check secrets provider configuration"
	}
}

// SecretErrors collects multiple secret resolution failures so operators
// can fix everything in one pass rather than restarting per error.
type SecretErrors struct {
	Errors  []*SecretError
	Backend string // propagated to each error's Hint() during Format()
}

func (se *SecretErrors) Error() string {
	return se.Format()
}

// Format returns a human-readable diagnostic block with fix commands.
func (se *SecretErrors) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n── %d secret(s) could not be resolved ──\n", len(se.Errors))

	for _, e := range se.Errors {
		if e.Backend == "" && se.Backend != "" {
			e.Backend = se.Backend
		}
		b.WriteByte('\n')
		loc := e.Match
		if e.Module != "" {
			loc += " > " + e.Module
		}
		if e.Resource != "" {
			loc += " > " + e.Resource
		}
		fmt.Fprintf(&b, "  %s\n", loc)
		fmt.Fprintf(&b, "    secret: %s\n", e.Secret)
		fmt.Fprintf(&b, "    error:  %v\n", e.Err)
		fmt.Fprintf(&b, "    fix:    %s\n", e.Hint())
	}

	b.WriteByte('\n')
	return b.String()
}

// IdempotencyErrors collects validation failures across multiple modules
// so operators can fix everything in one pass rather than restarting per module.
type IdempotencyErrors struct {
	Modules []idempotencyModuleError
}

type idempotencyModuleError struct {
	Module   string
	Path     string
	Findings []validate.Finding
}

func (ie *IdempotencyErrors) Error() string {
	return ie.Format()
}

// Format returns a human-readable diagnostic block grouped by module.
func (ie *IdempotencyErrors) Format() string {
	total := 0
	for _, m := range ie.Modules {
		for _, f := range m.Findings {
			if f.Severity == validate.SeverityError {
				total++
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n── validation failed: %d error(s) in %d module(s) ──\n", total, len(ie.Modules))

	for _, m := range ie.Modules {
		fmt.Fprintf(&b, "\n  %s\n", m.Path)
		for _, f := range m.Findings {
			fmt.Fprintf(&b, "    %s (%s): %s\n", f.ResourceName, f.ResourceType, f.Message)
		}
	}

	b.WriteByte('\n')
	return b.String()
}

// knownSecretFields are var names that must use the "secret:" prefix.
// If a config sets one of these to a plain value, loading fails.
var knownSecretFields = map[string]bool{
	"dsn":            true,
	"tls_cert_path":  true,
	"tls_key_path":   true,
	"webhook_secret": true,
}

const secretPrefix = "secret:"

// resolvedEntry pairs a match pattern with a resolved thrall config.
type resolvedEntry struct {
	match  string
	config *ThrallConfig
}

// ConfigStore loads, watches, and provides per-thrall configuration.
// The base dir contains thralls/, modules/, and roles/ subdirectories with
// .hnb/.yaml config files. Secret references are resolved via a
// secrets.Provider. Reload triggered by config publish via REST endpoint.
type ConfigStore struct {
	mu            sync.RWMutex
	entries       []resolvedEntry
	index         patternIndex                 // accelerated hostname lookup
	rawModules    map[string]*ModuleDefinition // unresolved, for secret ref scanning
	thrallEntries []ThrallEntry                // original entries for tracing
	roleDefs      map[string]*RoleDefinition   // loaded roles for tracing
	moduleDefs    map[string]*ModuleDefinition // loaded modules for tracing
	dir           string                       // thrall-configs directory
	provider      secrets.Provider
	backend       string // secrets backend name for diagnostic hints
	dangerous       bool   // allow non-idempotent resources
	version         atomic.Uint64 // incremented on each successful reload
	onReloadFailure func(err error) // optional callback on reload failure
	lastReloadErr   error // last reload error (nil if last reload succeeded)
}

// SetBackend sets the secrets backend name for diagnostic hints.
func (cs *ConfigStore) SetBackend(backend string) {
	cs.backend = backend
}

// SetOnReloadFailure sets a callback invoked when a config reload fails.
func (cs *ConfigStore) SetOnReloadFailure(fn func(err error)) {
	cs.onReloadFailure = fn
}

// NewConfigStore creates a ConfigStore that scans baseDir for thrall
// config files (.hnb or .yaml), module definitions from baseDir/modules/,
// and role definitions from baseDir/roles/. Secret references are resolved
// using the given provider.
//
// By default, all resources must pass idempotency validation. Set dangerous
// to true to allow non-idempotent resources (e.g., exec without guards).
func NewConfigStore(baseDir string, provider secrets.Provider, dangerous bool) (*ConfigStore, error) {
	cs := &ConfigStore{
		dir:       baseDir,
		provider:  provider,
		dangerous: dangerous,
	}
	if err := cs.loadAll(); err != nil {
		return nil, err
	}
	return cs, nil
}

// Dir returns the thrall-configs directory path.
func (cs *ConfigStore) Dir() string {
	return cs.dir
}

// ConfigStats holds summary statistics about the loaded configuration.
type ConfigStats struct {
	Modules int
	Entries int
}

// Stats returns the current module and entry counts.
func (cs *ConfigStore) Stats() ConfigStats {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return ConfigStats{
		Modules: len(cs.rawModules),
		Entries: len(cs.entries),
	}
}

// ListModuleNames returns the sorted names of all loaded modules.
func (cs *ConfigStore) ListModuleNames() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	names := make([]string, 0, len(cs.rawModules))
	for name := range cs.rawModules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListRoleNames returns the sorted names of all loaded roles.
// Roles are loaded during loadAll but not stored separately, so we
// re-read them from the roles directory.
func (cs *ConfigStore) ListRoleNames() []string {
	roleDefs, err := loadRoleDefs(cs.rolesDir())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(roleDefs))
	for name := range roleDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Reload re-reads all config files from disk and re-resolves secrets.
// Called by the REST API after config publish and by the secret rotation watcher.
// On failure, the previous config is preserved and the onReloadFailure callback fires.
func (cs *ConfigStore) Reload() error {
	if err := cs.loadAll(); err != nil {
		var se *SecretErrors
		var ie *IdempotencyErrors
		switch {
		case errors.As(err, &se):
			slog.Error("config reload failed, keeping previous config\n" + se.Format())
		case errors.As(err, &ie):
			slog.Error("config reload failed, keeping previous config\n" + ie.Format())
		default:
			slog.Error("config reload failed, keeping previous config", "error", err)
		}
		if cs.onReloadFailure != nil {
			cs.onReloadFailure(err)
		}
		return err
	}
	return nil
}

// Version returns the current config version. Incremented on each successful
// reload, used by the response cache to detect config changes.
func (cs *ConfigStore) Version() uint64 {
	return cs.version.Load()
}

// LastReloadError returns the error from the most recent config reload,
// or nil if the last reload succeeded. Surfaced to agents as a check-in warning.
func (cs *ConfigStore) LastReloadError() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.lastReloadErr
}

// Get returns the resolved config for the given hostname. Entries are
// evaluated in order — first match wins. If no entry matches, an empty
// config is returned (unconfigured thrall gets empty module list).
func (cs *ConfigStore) Get(hostname string) *ThrallConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if i := cs.index.Lookup(hostname); i >= 0 {
		cfg := cloneThrallConfig(cs.entries[i].config)
		cfg.Thrall = hostname
		return cfg
	}

	return &ThrallConfig{
		Thrall:    hostname,
		Modules: []ModuleConfig{},
	}
}

// Match returns the matched pattern and a read-only resolved config for
// hostname using the pattern index. Returns ("", nil) if no entry matches.
// The returned config is shared — callers must not mutate it. The Thrall
// field is NOT set to hostname (use MatchClone if you need that).
func (cs *ConfigStore) Match(hostname string) (string, *ThrallConfig) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if i := cs.index.Lookup(hostname); i >= 0 {
		entry := cs.entries[i]
		return entry.match, entry.config
	}

	return "", nil
}

// MatchClone returns the matched pattern and a cloned config for hostname.
// Safe to mutate. Used by callers that need to modify the result (e.g.
// EffectiveConfigYAML).
func (cs *ConfigStore) MatchClone(hostname string) (string, *ThrallConfig) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if i := cs.index.Lookup(hostname); i >= 0 {
		entry := cs.entries[i]
		cfg := cloneThrallConfig(entry.config)
		cfg.Thrall = hostname
		return entry.match, cfg
	}

	return "", nil
}

// ConfigTrace contains detailed resolution information for a hostname.
type ConfigTrace struct {
	Hostname        string                `json:"hostname"`
	Matched         bool                  `json:"matched"`
	MatchPattern    string                `json:"match_pattern,omitempty"`
	PatternType     string                `json:"pattern_type,omitempty"` // "exact" or "glob"
	Environment     string                `json:"environment,omitempty"`
	Roles           []string              `json:"roles,omitempty"`
	RoleExpansion   []RoleTraceEntry      `json:"role_expansion,omitempty"`
	Modules         []string              `json:"modules"`
	VarSources      []VarSourceEntry      `json:"var_sources"`
	FinalVars       map[string]any        `json:"final_vars"`
	ConditionalVars []string              `json:"conditional_vars,omitempty"`
	ResourceCount   int                   `json:"resource_count"`
}

// RoleTraceEntry shows which modules came from which role (and includes).
type RoleTraceEntry struct {
	Role     string   `json:"role"`
	Includes []string `json:"includes,omitempty"`
	Modules  []string `json:"modules"`
}

// VarSourceEntry tracks where a var's value came from.
type VarSourceEntry struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source string `json:"source"` // "module:<name>", "vars_from:<path>", "inline", "environment_overrides:<env>"
}

// Trace returns a detailed ConfigTrace for the given hostname, showing
// exactly how the config was resolved: which pattern matched, which roles
// expanded to which modules, and where each var came from.
func (cs *ConfigStore) Trace(hostname string) *ConfigTrace {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	trace := &ConfigTrace{
		Hostname:   hostname,
		Modules:    []string{},
		VarSources: []VarSourceEntry{},
		FinalVars:  map[string]any{},
	}

	idx := cs.index.Lookup(hostname)
	if idx < 0 {
		return trace
	}

	entry := cs.entries[idx]
	trace.Matched = true
	trace.MatchPattern = entry.match
	if isGlobPattern(entry.match) {
		trace.PatternType = "glob"
	} else {
		trace.PatternType = "exact"
	}

	// Find the original ThrallEntry to trace var sources.
	var thrallEntry *ThrallEntry
	for i := range cs.thrallEntries {
		if cs.thrallEntries[i].Match == entry.match {
			thrallEntry = &cs.thrallEntries[i]
			break
		}
	}
	if thrallEntry == nil {
		return trace
	}

	trace.Environment = thrallEntry.Environment
	trace.Roles = thrallEntry.Roles

	// Trace role expansion.
	for _, roleName := range thrallEntry.Roles {
		role, ok := cs.roleDefs[roleName]
		if !ok {
			continue
		}
		rte := RoleTraceEntry{Role: roleName}
		for _, incName := range role.Includes {
			rte.Includes = append(rte.Includes, incName)
			if inc, ok := cs.roleDefs[incName]; ok {
				for _, ref := range inc.Modules {
					rte.Modules = append(rte.Modules, ref.Name)
				}
			}
		}
		for _, ref := range role.Modules {
			rte.Modules = append(rte.Modules, ref.Name)
		}
		trace.RoleExpansion = append(trace.RoleExpansion, rte)
	}

	// Collect module names from resolved config.
	cfg := entry.config
	for _, mod := range cfg.Modules {
		trace.Modules = append(trace.Modules, mod.Name)
	}

	// Trace var sources by replaying the merge order.
	varSource := make(map[string]string) // key → source

	// 1. Module defaults
	seen := make(map[string]bool)
	for _, roleName := range thrallEntry.Roles {
		role, ok := cs.roleDefs[roleName]
		if !ok {
			continue
		}
		allRefs := make([]ModuleRef, 0)
		for _, incName := range role.Includes {
			if inc, ok := cs.roleDefs[incName]; ok {
				allRefs = append(allRefs, inc.Modules...)
			}
		}
		allRefs = append(allRefs, role.Modules...)
		for _, ref := range allRefs {
			if seen[ref.Name] {
				continue
			}
			seen[ref.Name] = true
			if def, ok := cs.moduleDefs[ref.Name]; ok {
				for k := range def.Vars {
					varSource[k] = "module:" + ref.Name
				}
			}
		}
	}
	for _, ref := range thrallEntry.Modules {
		if seen[ref.Name] {
			continue
		}
		seen[ref.Name] = true
		if def, ok := cs.moduleDefs[ref.Name]; ok {
			for k := range def.Vars {
				varSource[k] = "module:" + ref.Name
			}
		}
	}

	// 2. vars_from files
	for _, relPath := range thrallEntry.VarsFrom {
		expanded := relPath
		if strings.Contains(expanded, "{{") {
			expanded = strings.ReplaceAll(expanded, "{{ .Hostname }}", hostname)
			expanded = strings.ReplaceAll(expanded, "{{.Hostname}}", hostname)
		}
		fileVars, _, err := loadVarsFile(cs.dir, expanded)
		if err != nil {
			continue
		}
		for k := range fileVars {
			varSource[k] = "vars_from:" + relPath
		}
	}

	// 3. Inline vars
	for k := range thrallEntry.Vars {
		varSource[k] = "inline"
	}

	// 4. environment_overrides
	if thrallEntry.Environment != "" {
		if envOverrides, ok := thrallEntry.EnvironmentOverrides[thrallEntry.Environment]; ok {
			for k := range envOverrides {
				varSource[k] = "environment_overrides:" + thrallEntry.Environment
			}
		}
	}

	// Build ordered var sources from the resolved vars.
	for _, k := range cfg.VarOrder {
		src := varSource[k]
		if src == "" {
			src = "unknown"
		}
		trace.VarSources = append(trace.VarSources, VarSourceEntry{
			Key:    k,
			Value:  cfg.Vars[k],
			Source: src,
		})
	}
	trace.FinalVars = cfg.Vars

	// Conditional vars
	for _, cv := range cfg.ConditionalVars {
		trace.ConditionalVars = append(trace.ConditionalVars, cv.Name)
	}

	// Resource count
	for _, mod := range cfg.Modules {
		trace.ResourceCount += len(mod.Resources)
	}

	return trace
}

// thrallsDir returns the path to the thralls subdirectory.
func (cs *ConfigStore) thrallsDir() string { return filepath.Join(cs.dir, "thralls") }

// modulesDir returns the path to the modules subdirectory.
func (cs *ConfigStore) modulesDir() string { return filepath.Join(cs.dir, "modules") }

// rolesDir returns the path to the roles subdirectory.
func (cs *ConfigStore) rolesDir() string { return filepath.Join(cs.dir, "roles") }

// templatesDir returns the path to the templates subdirectory.
func (cs *ConfigStore) templatesDir() string { return filepath.Join(cs.dir, "templates") }

// varsDir returns the path to the vars subdirectory.
func (cs *ConfigStore) varsDir() string { return filepath.Join(cs.dir, "vars") }

// loadAll reads module defs, role defs, and thrall YAML files, resolves
// all entries, and atomically swaps the config.
func (cs *ConfigStore) loadAll() error {
	start := time.Now()
	err := cs.loadAllInner()
	elapsed := time.Since(start)

	cs.mu.Lock()
	cs.lastReloadErr = err
	cs.mu.Unlock()

	if err != nil {
		metrics.ConfigReloadErrors.Inc()
		return err
	}
	metrics.ConfigReloadDuration.Observe(elapsed.Seconds())
	return nil
}

func (cs *ConfigStore) loadAllInner() error {
	moduleDefs, err := loadModuleDefs(cs.modulesDir(), cs.dangerous)
	if err != nil {
		return fmt.Errorf("loading modules: %w", err)
	}

	if err := resolveSourceFiles(moduleDefs, cs.dir); err != nil {
		return fmt.Errorf("resolving source files: %w", err)
	}

	roleDefs, err := loadRoleDefs(cs.rolesDir())
	if err != nil {
		return fmt.Errorf("loading roles: %w", err)
	}

	thrallEntries, err := loadThrallsDir(cs.thrallsDir())
	if err != nil {
		return fmt.Errorf("loading thralls: %w", err)
	}

	// Validate role references.
	for _, role := range roleDefs {
		for _, ref := range role.Modules {
			if _, ok := moduleDefs[ref.Name]; !ok {
				return fmt.Errorf("role %q references module %q which does not exist — add a module definition to modules/", role.Name, ref.Name)
			}
		}
	}

	// Validate network device entries: gateway references, device types, credentials.
	if err := validateDeviceEntries(thrallEntries); err != nil {
		return err
	}

	var entries []resolvedEntry
	var allSecretErrs []*SecretError
	for i, entry := range thrallEntries {
		cfg, err := cs.resolveEntry(entry, moduleDefs, roleDefs)
		if err != nil {
			// Secret errors are collected across all entries so the
			// operator can fix everything in one restart cycle.
			var se *SecretErrors
			if errors.As(err, &se) {
				allSecretErrs = append(allSecretErrs, se.Errors...)
				continue
			}
			return fmt.Errorf("resolving thrall entry %d (match=%q): %w", i, entry.Match, err)
		}
		entries = append(entries, resolvedEntry{match: entry.Match, config: cfg})
	}
	if len(allSecretErrs) > 0 {
		return &SecretErrors{Errors: allSecretErrs, Backend: cs.backend}
	}

	// Pre-compute module content hashes for delta transfer.
	for i := range entries {
		cfg := entries[i].config
		for j := range cfg.Modules {
			cfg.Modules[j].ContentHash = HashModuleContent(cfg.Modules[j])
		}
	}

	// Build pattern index for fast hostname lookup.
	idx := buildPatternIndex(entries)

	cs.mu.Lock()
	cs.entries = entries
	cs.index = idx
	cs.rawModules = moduleDefs
	cs.thrallEntries = thrallEntries
	cs.roleDefs = roleDefs
	cs.moduleDefs = moduleDefs
	cs.mu.Unlock()

	v := cs.version.Add(1)

	slog.Info("config loaded",
		"thralls", len(entries),
		"modules", len(moduleDefs),
		"roles", len(roleDefs),
		"version", v)

	return nil
}

// loadVarsFile reads a YAML file containing a flat key-value map of vars.
// The path is relative to the thrall-configs directory. Returns the vars map
// and insertion-ordered keys.
func loadVarsFile(baseDir, relPath string) (map[string]any, []string, error) {
	fullPath := filepath.Clean(filepath.Join(baseDir, relPath))
	if !strings.HasPrefix(fullPath, baseDir+string(filepath.Separator)) && fullPath != baseDir {
		return nil, nil, fmt.Errorf("vars_from path %q escapes config directory", relPath)
	}

	// Resolve symlinks and re-check containment.
	// If EvalSymlinks fails because the file doesn't exist, fall
	// through to ReadFile which produces the expected error.
	readPath := fullPath
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err == nil {
		resolvedBase, err := filepath.EvalSymlinks(baseDir)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving config directory: %w", err)
		}
		if !strings.HasPrefix(resolved, resolvedBase+string(filepath.Separator)) && resolved != resolvedBase {
			return nil, nil, fmt.Errorf("vars_from path %q resolves outside config directory", relPath)
		}
		readPath = resolved
	}

	data, err := os.ReadFile(readPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading vars file %q: %w", relPath, err)
	}

	// Parse with yaml.Node to preserve key order.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parsing vars file %q: %w", relPath, err)
	}

	if doc.Kind == 0 {
		// Empty file.
		return nil, nil, nil
	}

	// doc is a document node wrapping the actual content.
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("vars file %q: expected a key-value mapping, got %v", relPath, root.Kind)
	}

	vars := make(map[string]any, len(root.Content)/2)
	keys := make([]string, 0, len(root.Content)/2)
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		var val any
		if err := root.Content[i+1].Decode(&val); err != nil {
			return nil, nil, fmt.Errorf("vars file %q key %q: %w", relPath, key, err)
		}
		if _, exists := vars[key]; !exists {
			keys = append(keys, key)
		}
		vars[key] = val
	}

	return vars, keys, nil
}

// resolveEntry converts a ThrallEntry into a fully resolved ThrallConfig.
func (cs *ConfigStore) resolveEntry(entry ThrallEntry, moduleDefs map[string]*ModuleDefinition, roleDefs map[string]*RoleDefinition) (*ThrallConfig, error) {
	// Collect module refs: roles first (with includes), then direct modules.
	// First ref wins for conditional fields (matching existing var merge behavior).
	var moduleRefs []ModuleRef
	seen := make(map[string]bool)
	for _, roleName := range entry.Roles {
		role, ok := roleDefs[roleName]
		if !ok {
			return nil, fmt.Errorf("role %q does not exist — add a role definition to roles/", roleName)
		}
		// Expand single-level includes before the role's own modules.
		for _, incName := range role.Includes {
			inc, ok := roleDefs[incName]
			if !ok {
				return nil, fmt.Errorf("role %q includes %q which does not exist", roleName, incName)
			}
			for _, ref := range inc.Modules {
				if !seen[ref.Name] {
					seen[ref.Name] = true
					moduleRefs = append(moduleRefs, ref)
				}
			}
		}
		for _, ref := range role.Modules {
			if !seen[ref.Name] {
				seen[ref.Name] = true
				moduleRefs = append(moduleRefs, ref)
			}
		}
	}
	for _, ref := range entry.Modules {
		if !seen[ref.Name] {
			seen[ref.Name] = true
			moduleRefs = append(moduleRefs, ref)
		}
	}

	// Resolve module definitions into ModuleConfigs, carrying conditional fields.
	var modules []ModuleConfig
	for _, ref := range moduleRefs {
		def, ok := moduleDefs[ref.Name]
		if !ok {
			return nil, fmt.Errorf("module %q does not exist — add a module definition to modules/", ref.Name)
		}
		modules = append(modules, ModuleConfig{
			Name:      def.Name,
			DependsOn: append([]string(nil), def.DependsOn...),
			When:      ref.When,
			Resources: cloneResources(def.Resources),
		})
	}

	// Resolve "before" into reverse "depends_on". Module A with before: [B]
	// means B depends on A — so we add A to B's DependsOn list.
	modulesByName := make(map[string]int, len(modules))
	for i, mod := range modules {
		modulesByName[mod.Name] = i
	}
	for _, mod := range modules {
		for _, target := range moduleDefs[mod.Name].Before {
			idx, ok := modulesByName[target]
			if !ok {
				return nil, fmt.Errorf("module %q has before: [%s] but module %q is not assigned to this thrall", mod.Name, target, target)
			}
			modules[idx].DependsOn = appendUnique(modules[idx].DependsOn, mod.Name)
		}
	}

	// Extract module names for var collection below.
	moduleNames := make([]string, len(moduleRefs))
	for i, ref := range moduleRefs {
		moduleNames[i] = ref.Name
	}

	// Collect conditional vars from modules. Deduplicate by name
	// (first definition wins, matching var merge behavior).
	var condVars []conditionals.ConditionalVar
	condVarNames := make(map[string]bool)
	for _, name := range moduleNames {
		for _, cv := range moduleDefs[name].ConditionalVars {
			if !condVarNames[cv.Name] {
				condVarNames[cv.Name] = true
				condVars = append(condVars, cv)
			}
		}
	}

	// Build vars: module defaults → vars_from files → thrall-level overrides.
	// Track insertion order so RenderVars can process top-down.
	vars := make(map[string]any)
	var varOrder []string
	for _, name := range moduleNames {
		for k, v := range moduleDefs[name].Vars {
			if _, exists := vars[k]; !exists {
				varOrder = append(varOrder, k)
			}
			vars[k] = v
		}
	}
	// Load vars_from files in order. Later files override earlier ones.
	// Supports {{ .Hostname }} substitution in paths.
	for _, relPath := range entry.VarsFrom {
		if strings.Contains(relPath, "{{") {
			relPath = strings.ReplaceAll(relPath, "{{ .Hostname }}", entry.Match)
			relPath = strings.ReplaceAll(relPath, "{{.Hostname}}", entry.Match)
		}
		fileVars, fileKeys, err := loadVarsFile(cs.dir, relPath)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", entry.Match, err)
		}
		for _, k := range fileKeys {
			if _, exists := vars[k]; !exists {
				varOrder = append(varOrder, k)
			}
			vars[k] = fileVars[k]
		}
	}
	for k, v := range entry.Vars {
		// Skip thrall-level vars that would conflict with conditional var names.
		// Conditional vars take precedence -- they are evaluated at check-in time.
		if condVarNames[k] {
			continue
		}
		if _, exists := vars[k]; !exists {
			varOrder = append(varOrder, k)
		}
		vars[k] = v
	}

	cfg := &ThrallConfig{
		Thrall:          entry.Match,
		MatchPattern:    entry.Match,
		Environment:     entry.Environment,
		Roles:           entry.Roles,
		Modules:         modules,
		Vars:            vars,
		VarOrder:        varOrder,
		EnvironmentOverrides: entry.EnvironmentOverrides,
		ConditionalVars:      condVars,
		Gateway:         entry.Gateway,
		DeviceType:      entry.DeviceType,
		Credential:      entry.Credential,
		Username:        entry.Username,
		Port:            entry.Port,
		IPAddress:       entry.IPAddress,
	}

	// Merge environment-specific vars.
	mergeEnvironmentOverrides(cfg)

	// Expand foreach resources now that vars are fully merged.
	for i := range cfg.Modules {
		expanded, err := ExpandForEach(cfg.Modules[i].Resources, cfg.Vars)
		if err != nil {
			return nil, fmt.Errorf("module %q: %w", cfg.Modules[i].Name, err)
		}
		cfg.Modules[i].Resources = expanded
	}

	// Enforce known-secret fields and resolve secret: prefixes.
	// Collect all failures so the operator can fix everything in one pass.
	var secretErrs []*SecretError
	secretErrs = append(secretErrs, cs.resolveVars(entry.Match, cfg.Vars)...)
	for i := range cfg.Modules {
		secretErrs = append(secretErrs, cs.resolveModuleSecrets(entry.Match, &cfg.Modules[i])...)
	}

	// Resolve the device credential secret if this is a network device entry.
	if cfg.Credential != "" && strings.HasPrefix(cfg.Credential, secretPrefix) {
		key := strings.TrimPrefix(cfg.Credential, secretPrefix)
		val, err := cs.provider.Get(context.Background(), key)
		if err != nil {
			secretErrs = append(secretErrs, &SecretError{
				Match:   entry.Match,
				Attr:    "credential",
				Secret:  key,
				Err:     err,
				Backend: cs.backend,
			})
		} else {
			cfg.Credential = val
		}
	}

	if len(secretErrs) > 0 {
		for range secretErrs {
			metrics.SecretResolutionErrorsTotal.Inc()
		}
		return nil, &SecretErrors{Errors: secretErrs, Backend: cs.backend}
	}

	return cfg, nil
}

// validateDeviceEntries checks network device entry constraints:
// - device_type must be a supported type
// - gateway must reference an existing thrall entry without a device_type
// - credential must use the secret: prefix
// - gateway entries must not themselves have a device_type
func validateDeviceEntries(entries []ThrallEntry) error {
	// Build lookup: match pattern → entry for gateway reference validation.
	byMatch := make(map[string]*ThrallEntry, len(entries))
	for i := range entries {
		byMatch[entries[i].Match] = &entries[i]
	}

	for _, entry := range entries {
		if entry.DeviceType == "" && entry.Gateway == "" {
			continue // normal thrall
		}

		// device_type and gateway must be set together.
		if entry.DeviceType != "" && entry.Gateway == "" {
			return fmt.Errorf("thrall entry %q has device_type %q but no gateway — network devices require a gateway", entry.Match, entry.DeviceType)
		}
		if entry.Gateway != "" && entry.DeviceType == "" {
			return fmt.Errorf("thrall entry %q has gateway %q but no device_type — gateway is only for network devices", entry.Match, entry.Gateway)
		}

		// Validate device_type.
		if !ValidDeviceTypes[entry.DeviceType] {
			valid := make([]string, 0, len(ValidDeviceTypes))
			for k := range ValidDeviceTypes {
				valid = append(valid, k)
			}
			sort.Strings(valid)
			return fmt.Errorf("thrall entry %q has unsupported device_type %q (valid: %s)", entry.Match, entry.DeviceType, strings.Join(valid, ", "))
		}

		// Validate credential uses secret: prefix.
		if entry.Credential == "" {
			return fmt.Errorf("thrall entry %q (device_type=%s) requires a credential field", entry.Match, entry.DeviceType)
		}
		if !strings.HasPrefix(entry.Credential, secretPrefix) {
			return fmt.Errorf("thrall entry %q credential must use \"secret:\" prefix", entry.Match)
		}

		// Validate gateway references an existing entry.
		gw, ok := byMatch[entry.Gateway]
		if !ok {
			return fmt.Errorf("thrall entry %q references gateway %q which does not exist", entry.Match, entry.Gateway)
		}

		// Gateway must be a normal thrall, not itself a device.
		if gw.DeviceType != "" {
			return fmt.Errorf("thrall entry %q references gateway %q which is itself a network device", entry.Match, entry.Gateway)
		}
	}

	return nil
}

// DeviceConfig holds the resolved config and connection details for a network device.
type DeviceConfig struct {
	Hostname   string       // device hostname (match pattern)
	DeviceType string       // cisco_ios, cisco_nxos, juniper_junos
	Credential string       // resolved secret value
	Username   string       // SSH username
	Port       int          // SSH port
	IPAddress  string       // management IP
	Config     *ThrallConfig // resolved config (modules, vars, etc.)
}

// DevicesForGateway returns all network device entries whose gateway field
// matches the given gateway hostname. Used during gateway check-in to build
// the ProxiedDevice list.
func (cs *ConfigStore) DevicesForGateway(gatewayHostname string) []DeviceConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var devices []DeviceConfig
	for _, entry := range cs.entries {
		cfg := entry.config
		if cfg.Gateway != gatewayHostname {
			continue
		}
		port := cfg.Port
		if port == 0 {
			port = 22
		}
		username := cfg.Username
		if username == "" {
			username = "admin"
		}
		devices = append(devices, DeviceConfig{
			Hostname:   cfg.Thrall,
			DeviceType: cfg.DeviceType,
			Credential: cfg.Credential,
			Username:   username,
			Port:       port,
			IPAddress:  cfg.IPAddress,
			Config:     cfg,
		})
	}
	return devices
}

// DeviceEntry holds minimal info about a network device config entry.
type DeviceEntry struct {
	Hostname   string // match pattern / hostname
	Gateway    string // gateway hostname
	DeviceType string // cisco_ios, cisco_nxos, juniper_junos
}

// AllDeviceEntries returns all network device entries (entries with a non-empty DeviceType).
func (cs *ConfigStore) AllDeviceEntries() []DeviceEntry {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var entries []DeviceEntry
	for _, e := range cs.entries {
		if e.config.DeviceType != "" {
			entries = append(entries, DeviceEntry{
				Hostname:   e.config.Thrall,
				Gateway:    e.config.Gateway,
				DeviceType: e.config.DeviceType,
			})
		}
	}
	return entries
}

// ThrallsReferencingSecret returns the match patterns of all thrall entries
// whose configuration contains a "secret:<secretPath>" reference. Used by
// the rotation watcher to identify which thralls need force-push.
func (cs *ConfigStore) ThrallsReferencingSecret(secretPath string) []string {
	needle := secretPrefix + secretPath

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	// Check which modules reference this secret.
	affectedModules := make(map[string]bool)
	for name, mod := range cs.rawModules {
		if moduleDefReferencesSecret(mod, needle) {
			affectedModules[name] = true
		}
	}

	// Find entries that use affected modules or have the secret in their vars.
	var patterns []string
	for _, entry := range cs.entries {
		if entryReferencesSecret(entry.config, affectedModules, needle) {
			patterns = append(patterns, entry.match)
		}
	}
	return patterns
}

// SecretPaths returns the deduplicated set of secret paths referenced across
// all raw module definitions. Used by the watcher to know which secrets to poll.
func (cs *ConfigStore) SecretPaths() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	seen := make(map[string]bool)
	for _, mod := range cs.rawModules {
		for _, v := range mod.Vars {
			if s, ok := v.(string); ok && strings.HasPrefix(s, secretPrefix) {
				seen[strings.TrimPrefix(s, secretPrefix)] = true
			}
		}
		for _, res := range mod.Resources {
			for _, v := range res.Attributes {
				if s, ok := v.(string); ok && strings.HasPrefix(s, secretPrefix) {
					seen[strings.TrimPrefix(s, secretPrefix)] = true
				}
			}
		}
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	return paths
}

// EffectiveConfigYAML returns the resolved config for hostname as YAML,
// with secret values redacted. Used by the UI config viewer.
func (cs *ConfigStore) EffectiveConfigYAML(hostname string) (string, error) {
	cfg := cs.Get(hostname)
	if cfg == nil || len(cfg.Modules) == 0 {
		return "", fmt.Errorf("no config found for %s", hostname)
	}

	// Redact resolved secrets in vars.
	redacted := cloneThrallConfig(cfg)
	for k, v := range redacted.Vars {
		if _, ok := v.(string); ok && knownSecretFields[k] {
			redacted.Vars[k] = "[REDACTED]"
		}
	}

	data, err := yaml.Marshal(redacted)
	if err != nil {
		return "", fmt.Errorf("marshaling config: %w", err)
	}
	return string(data), nil
}

// --- internal helpers ---

// loadModuleDefs reads all config files from the modules directory.
func loadModuleDefs(dir string, dangerous bool) (map[string]*ModuleDefinition, error) {
	paths, err := walkConfigFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("reading modules dir: %w", err)
	}

	defs := make(map[string]*ModuleDefinition)
	var idempErrs []idempotencyModuleError
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		def, err := ParseModuleFile(data)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if def.Name == "" {
			return nil, fmt.Errorf("%s: missing required 'name' field", path)
		}
		if _, exists := defs[def.Name]; exists {
			return nil, fmt.Errorf("duplicate module name %q in %s", def.Name, path)
		}

		// Separate conditional vars from regular vars.
		// A conditional var is any map value with a "trait" key or any
		// known operator key (in, not_in, gt, lt, gte, lte, regex).
		// Maps with operator keys but no trait are caught as validation
		// errors by ParseConditionalVar.
		var condVars []conditionals.ConditionalVar
		for name, v := range def.Vars {
			if !conditionals.IsConditionalVar(v) && !conditionals.HasOperatorKey(v) {
				continue
			}
			rawMap, _ := v.(map[string]any)
			cv, err := conditionals.ParseConditionalVar(name, rawMap)
			if err != nil {
				return nil, fmt.Errorf("module %q: %w", def.Name, err)
			}
			condVars = append(condVars, cv)
			delete(def.Vars, name)
		}
		def.ConditionalVars = condVars

		// Idempotency validation — collect errors across all modules.
		findings := validate.ValidateModule(moduleToValidate(def))
		for _, f := range findings {
			if f.Severity == validate.SeverityWarning {
				slog.Warn("idempotency warning", "module", def.Name, "finding", f.String())
			}
		}
		if !dangerous && validate.HasErrors(findings) {
			idempErrs = append(idempErrs, idempotencyModuleError{
				Module:   def.Name,
				Path:     path,
				Findings: findings,
			})
		}

		// Self-protection guardrails — always enforced, even in dangerous mode.
		spFindings := validate.ValidateSelfProtection(moduleToValidate(def))
		for _, f := range spFindings {
			slog.Error("self-protection violation", "module", def.Name, "finding", f.String())
		}
		if validate.HasErrors(spFindings) {
			idempErrs = append(idempErrs, idempotencyModuleError{
				Module:   def.Name,
				Path:     path,
				Findings: spFindings,
			})
		}

		defs[def.Name] = def
	}
	if len(idempErrs) > 0 {
		return nil, &IdempotencyErrors{Modules: idempErrs}
	}
	return defs, nil
}

// loadRoleDefs reads all config files from the roles directory.
func loadRoleDefs(dir string) (map[string]*RoleDefinition, error) {
	paths, err := walkConfigFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("reading roles dir: %w", err)
	}

	defs := make(map[string]*RoleDefinition)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var def RoleDefinition
		if err := yaml.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if def.Name == "" {
			return nil, fmt.Errorf("%s: missing required 'name' field", path)
		}
		if _, exists := defs[def.Name]; exists {
			return nil, fmt.Errorf("duplicate role name %q in %s", def.Name, path)
		}
		defs[def.Name] = &def
	}

	// Validate includes: single level only — included roles must not
	// themselves have includes.
	for _, def := range defs {
		for _, incName := range def.Includes {
			inc, ok := defs[incName]
			if !ok {
				return nil, fmt.Errorf("role %q includes %q which does not exist", def.Name, incName)
			}
			if len(inc.Includes) > 0 {
				return nil, fmt.Errorf("role %q includes %q which itself has includes — only single-level nesting is allowed", def.Name, incName)
			}
		}
	}

	return defs, nil
}

// loadThrallsDir reads all config files (.hnb/.yaml) from the thralls directory
// and merges their thrall entries in filesystem order.
func loadThrallsDir(dir string) ([]ThrallEntry, error) {
	paths, err := walkConfigFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("reading thralls dir: %w", err)
	}

	var allEntries []ThrallEntry
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var cfg ThrallsConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		for i, entry := range cfg.Thralls {
			if entry.Match == "" {
				return nil, fmt.Errorf("%s entry %d: missing required 'match' field", path, i)
			}
		}
		allEntries = append(allEntries, cfg.Thralls...)
	}
	return allEntries, nil
}

// mergeEnvironmentOverrides merges base vars with environment-specific overrides.
func mergeEnvironmentOverrides(cfg *ThrallConfig) {
	if cfg.Vars == nil {
		cfg.Vars = make(map[string]any)
	}
	if cfg.Environment == "" || cfg.EnvironmentOverrides == nil {
		return
	}
	envOverrides, ok := cfg.EnvironmentOverrides[cfg.Environment]
	if !ok {
		return
	}
	for k, v := range envOverrides {
		if _, exists := cfg.Vars[k]; !exists {
			cfg.VarOrder = append(cfg.VarOrder, k)
		}
		cfg.Vars[k] = v
	}
}

// resolveVars enforces known-secret fields and resolves secret: prefixes.
// It collects all failures rather than stopping at the first.
func (cs *ConfigStore) resolveVars(match string, vars map[string]any) []*SecretError {
	var errs []*SecretError
	for k, v := range vars {
		str, ok := v.(string)
		if !ok {
			continue
		}
		if knownSecretFields[k] && !strings.HasPrefix(str, secretPrefix) {
			errs = append(errs, &SecretError{
				Match:  match,
				Attr:   k,
				Secret: str,
				Err: fmt.Errorf(
					"known-secret field %q must use %q prefix (got %q) -- "+
						"store secrets in isopass, not in config files",
					k, secretPrefix, str,
				),
			})
			continue
		}
		if strings.HasPrefix(str, secretPrefix) {
			key := strings.TrimPrefix(str, secretPrefix)
			resolved, err := cs.provider.Get(context.Background(), key)
			if err != nil {
				errs = append(errs, &SecretError{
					Match:  match,
					Attr:   k,
					Secret: key,
					Err:    err,
				})
				continue
			}
			vars[k] = resolved
		}
	}
	return errs
}

// resolveModuleSecrets resolves secret: prefixes in module resource attributes.
// It collects all failures rather than stopping at the first.
func (cs *ConfigStore) resolveModuleSecrets(match string, mod *ModuleConfig) []*SecretError {
	var errs []*SecretError
	for i := range mod.Resources {
		attrs := mod.Resources[i].Attributes
		for k, v := range attrs {
			str, ok := v.(string)
			if !ok {
				continue
			}
			if strings.HasPrefix(str, secretPrefix) {
				key := strings.TrimPrefix(str, secretPrefix)
				resolved, err := cs.provider.Get(context.Background(), key)
				if err != nil {
					errs = append(errs, &SecretError{
						Match:    match,
						Module:   mod.Name,
						Resource: mod.Resources[i].Name,
						Attr:     k,
						Secret:   key,
						Err:      err,
					})
					continue
				}
				attrs[k] = resolved
			}
		}
	}
	return errs
}

// resolveSourceFiles reads external template files referenced by the "source"
// attribute on file resources. The source path is relative to the thrall-configs
// directory (e.g., "templates/nginx/nginx.conf.tmpl"). The file contents replace
// the "source" attribute with "content".
// It is an error to set both "source" and "content" on the same resource.
func resolveSourceFiles(defs map[string]*ModuleDefinition, baseDir string) error {
	for _, def := range defs {
		for i := range def.Resources {
			attrs := def.Resources[i].Attributes
			if attrs == nil {
				continue
			}
			source, hasSource := attrs["source"]
			_, hasContent := attrs["content"]

			if hasSource && hasContent {
				return fmt.Errorf("module %q resource %q: cannot set both 'source' and 'content'",
					def.Name, def.Resources[i].Name)
			}
			if !hasSource {
				continue
			}

			sourcePath, ok := source.(string)
			if !ok {
				return fmt.Errorf("module %q resource %q: 'source' must be a string",
					def.Name, def.Resources[i].Name)
			}

			fullPath := filepath.Clean(filepath.Join(baseDir, sourcePath))
			if !strings.HasPrefix(fullPath, baseDir+string(filepath.Separator)) && fullPath != baseDir {
				return fmt.Errorf("module %q resource %q: source path %q escapes config directory",
					def.Name, def.Resources[i].Name, sourcePath)
			}

			// Resolve symlinks and re-check containment.
			// If EvalSymlinks fails because the file doesn't exist, fall
			// through to ReadFile which produces the expected error.
			readPath := fullPath
			resolved, err := filepath.EvalSymlinks(fullPath)
			if err == nil {
				resolvedBase, err := filepath.EvalSymlinks(baseDir)
				if err != nil {
					return fmt.Errorf("resolving config directory: %w", err)
				}
				if !strings.HasPrefix(resolved, resolvedBase+string(filepath.Separator)) && resolved != resolvedBase {
					return fmt.Errorf("module %q resource %q: source path %q resolves outside config directory",
						def.Name, def.Resources[i].Name, sourcePath)
				}
				readPath = resolved
			}

			data, err := os.ReadFile(readPath)
			if err != nil {
				return fmt.Errorf("module %q resource %q: reading source %q: %w",
					def.Name, def.Resources[i].Name, sourcePath, err)
			}

			attrs["content"] = string(data)
			delete(attrs, "source")
		}
	}
	return nil
}

// moduleDefReferencesSecret checks if a module definition's resources contain
// the given secret reference string.
func moduleDefReferencesSecret(def *ModuleDefinition, needle string) bool {
	for _, v := range def.Vars {
		if s, ok := v.(string); ok && s == needle {
			return true
		}
	}
	for _, res := range def.Resources {
		for _, v := range res.Attributes {
			if s, ok := v.(string); ok && s == needle {
				return true
			}
		}
	}
	return false
}

// entryReferencesSecret checks if a thrall entry uses any affected module
// or has the secret in its environment_overrides (non-active environment overrides retain
// raw secret: prefixes since only the active environment is resolved).
func entryReferencesSecret(cfg *ThrallConfig, affectedModules map[string]bool, needle string) bool {
	// Check if any module is affected.
	for _, mod := range cfg.Modules {
		if affectedModules[mod.Name] {
			return true
		}
	}
	// Check environment_overrides — non-active environments retain raw secret: references.
	// (cfg.Vars is already resolved, so checking it would never match.)
	if cfg.EnvironmentOverrides != nil {
		for _, envMap := range cfg.EnvironmentOverrides {
			for _, v := range envMap {
				if s, ok := v.(string); ok && s == needle {
					return true
				}
			}
		}
	}
	return false
}

// ComputeEnvDiff compares environment_overrides between two environments across all thrall
// entries that include the given module. Returns a map of var_name -> "old → new".
func (cs *ConfigStore) ComputeEnvDiff(moduleName, fromEnv, toEnv string) map[string]string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	diff := make(map[string]string)
	for _, entry := range cs.entries {
		hasModule := false
		for _, mod := range entry.config.Modules {
			if mod.Name == moduleName {
				hasModule = true
				break
			}
		}
		if !hasModule {
			continue
		}

		fromVars := entry.config.EnvironmentOverrides[fromEnv]
		toVars := entry.config.EnvironmentOverrides[toEnv]

		for k, fromVal := range fromVars {
			fromStr := fmt.Sprintf("%v", fromVal)
			toVal, exists := toVars[k]
			if !exists {
				diff[k] = fromStr + " → (unset)"
			} else {
				toStr := fmt.Sprintf("%v", toVal)
				if fromStr != toStr {
					diff[k] = fromStr + " → " + toStr
				}
			}
		}
		for k, toVal := range toVars {
			if _, exists := fromVars[k]; !exists {
				diff[k] = "(unset) → " + fmt.Sprintf("%v", toVal)
			}
		}
	}

	return diff
}

// cloneThrallConfig returns a shallow copy of the ThrallConfig with a
// fresh Modules slice and Vars map, so modifications don't affect the original.
func cloneThrallConfig(cfg *ThrallConfig) *ThrallConfig {
	clone := *cfg
	clone.Modules = make([]ModuleConfig, len(cfg.Modules))
	copy(clone.Modules, cfg.Modules)
	clone.Vars = make(map[string]any, len(cfg.Vars))
	for k, v := range cfg.Vars {
		clone.Vars[k] = v
	}
	clone.VarOrder = append([]string(nil), cfg.VarOrder...)
	clone.ConditionalVars = append([]conditionals.ConditionalVar(nil), cfg.ConditionalVars...)
	return &clone
}

// cloneResources returns a deep-enough copy of the resource configs.
func cloneResources(resources []ResourceConfig) []ResourceConfig {
	cloned := make([]ResourceConfig, len(resources))
	for i, r := range resources {
		cloned[i] = r
		if r.Attributes != nil {
			cloned[i].Attributes = make(map[string]any, len(r.Attributes))
			for k, v := range r.Attributes {
				cloned[i].Attributes[k] = v
			}
		}
		if r.DependsOn != nil {
			cloned[i].DependsOn = append([]string(nil), r.DependsOn...)
		}
		if r.Notify != nil {
			cloned[i].Notify = append([]string(nil), r.Notify...)
		}
		if r.Subscribes != nil {
			cloned[i].Subscribes = append([]string(nil), r.Subscribes...)
		}
		if r.Before != nil {
			cloned[i].Before = append([]string(nil), r.Before...)
		}
	}
	return cloned
}

// walkConfigFiles returns all config file paths under dir, recursively.
// Skips .example files and non-config extensions.
func walkConfigFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && isConfigFile(d.Name()) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// isConfigFile checks if a filename has a recognized config extension.
// Accepts .hnb (preferred), .yaml, and .yml. Ignores .example files.
func isConfigFile(name string) bool {
	if strings.HasSuffix(name, ".example") {
		return false
	}
	ext := filepath.Ext(name)
	return ext == ".hnb" || ext == ".yaml" || ext == ".yml"
}

// appendUnique appends val to slice if not already present.
func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// moduleToValidate converts a ModuleDefinition to a validate.Module.
func moduleToValidate(def *ModuleDefinition) validate.Module {
	mod := validate.Module{Name: def.Name}
	for _, r := range def.Resources {
		mod.Resources = append(mod.Resources, validate.Resource{
			Name:             r.Name,
			Type:             r.Type,
			Attributes:       r.Attributes,
			DependsOn:        r.DependsOn,
			Before:           r.Before,
			Notify:           r.Notify,
			Subscribes:       r.Subscribes,
			WatchSecret: r.WatchSecret,
		})
	}
	return mod
}

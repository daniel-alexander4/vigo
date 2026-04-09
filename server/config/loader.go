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
	texttemplate "text/template"
	"time"

	"github.com/daniel-alexander4/vigo/server/conditionals"
	"github.com/daniel-alexander4/vigo/server/metrics"
	"github.com/daniel-alexander4/vigo/server/secrets"
	"github.com/daniel-alexander4/vigo/server/validate"
	"gopkg.in/yaml.v3"
)

// SecretError describes a single secret resolution failure with enough
// context to diagnose and fix without guessing.
type SecretError struct {
	Match    string // envoy match pattern (e.g. "db-*.prod.example.com")
	Module   string // module name (empty if failure is in envoy vars)
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
			return fmt.Sprintf("vigocli secrets set %s", e.Secret)
		case strings.Contains(msg, "wrong master key"), strings.Contains(msg, "message authentication failed"):
			return "secret was written with a different key -- re-encrypt with: vigocli secrets set " + e.Secret
		case strings.Contains(msg, "ciphertext too short"):
			return "file may be plaintext from a previous config -- re-encrypt with: vigocli secrets set " + e.Secret
		default:
			return fmt.Sprintf("check %s in the secrets directory", e.Secret)
		}
	case "isopass":
		switch {
		case strings.Contains(msg, "not found"):
			return fmt.Sprintf("create this secret in isopass: isopass set %s", e.Secret)
		case strings.Contains(msg, "unreachable"):
			return "ensure isopass is running and VIGO_ISOPASS_URL is correct"
		case strings.Contains(msg, "authentication failed"):
			return "check VIGO_ISOPASS_TOKEN -- regenerate if expired"
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
	Modules []IdempotencyModuleError
}

// IdempotencyModuleError holds validation findings for a single module file.
type IdempotencyModuleError struct {
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

// resolvedEntry pairs a match pattern with a resolved envoy config.
type resolvedEntry struct {
	match  string
	config *EnvoyConfig
}

// ConfigStore loads, watches, and provides per-envoy configuration.
// The base dir contains modules/ and templates/ subdirectories plus
// .vgo/.yaml config files organized in a directory hierarchy for
// cascading inheritance. Secret references are resolved via a
// secrets.Provider. Reload triggered by config publish via REST endpoint.
type ConfigStore struct {
	mu            sync.RWMutex
	entries       []resolvedEntry
	index         *patternIndex                // accelerated hostname lookup
	rawModules    map[string]*ModuleDefinition // unresolved, for secret ref scanning
	envoyEntries []EnvoyEntry                // original entries for tracing
	roleDefs      map[string]*RoleDefinition   // loaded roles for tracing
	moduleDefs      map[string]*ModuleDefinition       // loaded modules for tracing
	complianceIndex map[string]map[string][]string    // standard → control_id → []module_name
	commonConfigs    map[string]*CommonConfig                    // dir path → common.vgo inheritance config
	perEnvOverrides  map[string]map[string]*PerEnvOverrideBlock // dir path → env name → override block
	waivers          map[string]map[string]*WaiverEntry         // standard → control_id → waiver
	waiversByFile   []WaiverFileEntry                  // per-file waiver data for trace display
	dir           string                       // examples directory
	provider      secrets.Provider
	backend       string // secrets backend name for diagnostic hints
	dangerous       bool   // allow non-idempotent resources
	version         atomic.Uint64 // incremented on each successful reload
	onReloadFailure  func(err error) // optional callback on reload failure
	onReloadSuccess  func()          // optional callback after successful reload
	lastReloadErr    error // last reload error (nil if last reload succeeded)
	enabledStandards map[string]bool // nil = all enabled; non-nil = only these standard keys
	serverYamlPath   string          // path to server.yaml for re-reading compliance on reload
}

// NewEmptyConfigStore creates a ConfigStore with no loaded config.
// Used when initial config loading fails so the server can still start.
func NewEmptyConfigStore(provider secrets.Provider) *ConfigStore {
	return &ConfigStore{
		entries:       nil,
		index:         buildPatternIndex(nil),
		rawModules:    make(map[string]*ModuleDefinition),
		envoyEntries: nil,
		roleDefs:      make(map[string]*RoleDefinition),
		moduleDefs:      make(map[string]*ModuleDefinition),
		complianceIndex: make(map[string]map[string][]string),
		commonConfigs:    make(map[string]*CommonConfig),
		perEnvOverrides: make(map[string]map[string]*PerEnvOverrideBlock),
		provider:        provider,
	}
}

// SetBackend sets the secrets backend name for diagnostic hints.
func (cs *ConfigStore) SetBackend(backend string) {
	cs.backend = backend
}

// SetOnReloadFailure sets a callback invoked when a config reload fails.
func (cs *ConfigStore) SetOnReloadFailure(fn func(err error)) {
	cs.onReloadFailure = fn
}

// SetOnReloadSuccess sets a callback invoked after a successful config reload.
func (cs *ConfigStore) SetOnReloadSuccess(fn func()) {
	cs.onReloadSuccess = fn
}

// SetServerYamlPath stores the path to server.yaml so Reload() can
// re-read the compliance standards list on config publish.
func (cs *ConfigStore) SetServerYamlPath(path string) {
	cs.serverYamlPath = path
}

// SetEnabledStandards configures which compliance frameworks are active.
// Pass nil or empty to enable all frameworks (default).
// Keys are standard tag keys (e.g. "hipaa", "soc2", "pci-dss"), not URL slugs.
func (cs *ConfigStore) SetEnabledStandards(standards []string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(standards) == 0 {
		cs.enabledStandards = nil
		return
	}
	m := make(map[string]bool, len(standards))
	for _, s := range standards {
		if _, ok := SlugForStandardKey[s]; !ok {
			slog.Warn("unrecognized compliance standard key in server.yaml", "key", s)
		}
		m[s] = true
	}
	cs.enabledStandards = m
	slog.Info("compliance standards configured", "enabled", standards)
}

// IsStandardEnabled reports whether the given standard key is active.
// Returns true when no filtering is configured (all-enabled default).
func (cs *ConfigStore) IsStandardEnabled(standardKey string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if cs.enabledStandards == nil {
		return true
	}
	return cs.enabledStandards[standardKey]
}

// NewConfigStore creates a ConfigStore that scans baseDir for envoy
// config files (.vgo or .yaml), module definitions from baseDir/modules/,
// and role definitions from baseDir/roles/. Secret references are resolved
// using the given provider.
//
// By default, all resources must pass idempotency validation. Set dangerous
// to true to allow non-idempotent resources (e.g., exec without guards).
//
// Idempotency validation errors are non-fatal: the store starts with zero
// modules loaded and the error is stored in LastReloadError() for surfacing
// via doctor/dashboard. Structural errors (YAML syntax, missing name,
// dangling role→module refs, secret resolution) remain fatal.
// NewConfigStoreQuiet is like NewConfigStore but suppresses log output for
// validation errors. The caller is responsible for checking LastReloadError().
func NewConfigStoreQuiet(baseDir string, provider secrets.Provider, dangerous bool) (*ConfigStore, error) {
	return newConfigStore(baseDir, provider, dangerous, true)
}

func NewConfigStore(baseDir string, provider secrets.Provider, dangerous bool) (*ConfigStore, error) {
	return newConfigStore(baseDir, provider, dangerous, false)
}

func newConfigStore(baseDir string, provider secrets.Provider, dangerous, quiet bool) (*ConfigStore, error) {
	cs := &ConfigStore{
		dir:             baseDir,
		provider:        provider,
		dangerous:       dangerous,
		index:           buildPatternIndex(nil),
		rawModules:      make(map[string]*ModuleDefinition),
		roleDefs:        make(map[string]*RoleDefinition),
		moduleDefs:      make(map[string]*ModuleDefinition),
		complianceIndex: make(map[string]map[string][]string),
		commonConfigs:   make(map[string]*CommonConfig),
		perEnvOverrides: make(map[string]map[string]*PerEnvOverrideBlock),
	}
	if err := cs.loadAll(); err != nil {
		// Idempotency errors are non-fatal at startup — start with empty
		// config so the REST API, UI, and gRPC remain available for
		// diagnosis. The error is stored in lastReloadErr and surfaced
		// via LastReloadError(), doctor, and the dashboard.
		var ie *IdempotencyErrors
		if errors.As(err, &ie) {
			if !quiet {
				slog.Error("config loaded with validation errors — starting with empty config\n" + ie.Format())
			}
			cs.mu.Lock()
			cs.lastReloadErr = err
			cs.mu.Unlock()
			return cs, nil
		}
		return nil, err
	}
	return cs, nil
}

// Dir returns the examples directory path.
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
func (cs *ConfigStore) ListRoleNames() []string {
	cs.mu.RLock()
	roleDefs := cs.roleDefs
	cs.mu.RUnlock()
	if roleDefs == nil {
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
	// Re-read compliance standards from server.yaml on reload.
	if cs.serverYamlPath != "" {
		cs.reloadComplianceStandards()
	}
	if cs.onReloadSuccess != nil {
		cs.onReloadSuccess()
	}
	return nil
}

// reloadComplianceStandards re-reads just the compliance section from server.yaml.
func (cs *ConfigStore) reloadComplianceStandards() {
	data, err := os.ReadFile(cs.serverYamlPath)
	if err != nil {
		slog.Warn("failed to re-read server.yaml for compliance standards", "error", err)
		return
	}
	var raw struct {
		Compliance ComplianceSection `yaml:"compliance"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		slog.Warn("failed to parse compliance section from server.yaml", "error", err)
		return
	}
	cs.SetEnabledStandards(raw.Compliance.Standards)
}

// Version returns the current config version. Incremented on each successful
// reload, used by the response cache to detect config changes.
func (cs *ConfigStore) Version() uint64 {
	return cs.version.Load()
}

// UserAuthorizedKeys returns the first authorized_keys value found across all
// module definitions for a user resource matching the given Unix username.
func (cs *ConfigStore) UserAuthorizedKeys(username string) string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for _, mod := range cs.moduleDefs {
		for _, res := range mod.Resources {
			if res.Type != "user" {
				continue
			}
			if u, _ := res.Attributes["username"].(string); u == username {
				if keys, _ := res.Attributes["authorized_keys"].(string); keys != "" {
					return strings.TrimSpace(keys)
				}
			}
		}
	}
	return ""
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
// config is returned (unconfigured envoy gets empty module list).
func (cs *ConfigStore) Get(hostname string) *EnvoyConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if i := cs.index.Lookup(hostname); i >= 0 {
		cfg := cloneEnvoyConfig(cs.entries[i].config)
		cfg.Envoy = hostname
		return cfg
	}

	// No explicit match — apply root common.vgo inheritance so every
	// enrolled envoy gets the baseline config without needing match: "*".
	if cc, ok := cs.commonConfigs[""]; ok && (len(cc.Modules) > 0 || len(cc.Roles) > 0) {
		entry := EnvoyEntry{Modules: cc.Modules, Roles: cc.Roles}
		if cc.Vars != nil {
			entry.Vars = make(map[string]any, len(cc.Vars))
			for k, v := range cc.Vars {
				entry.Vars[k] = v
			}
		}
		if cfg, err := cs.resolveEntry(entry, cs.moduleDefs, cs.roleDefs); err == nil {
			resolved := cloneEnvoyConfig(cfg)
			resolved.Envoy = hostname
			return resolved
		}
	}

	return &EnvoyConfig{
		Envoy:  hostname,
		Modules: []ModuleConfig{},
	}
}

// Match returns the matched pattern and a read-only resolved config for
// hostname using the pattern index. Returns ("", nil) if no entry matches.
// The returned config is shared — callers must not mutate it. The Envoy
// field is NOT set to hostname (use MatchClone if you need that).
func (cs *ConfigStore) Match(hostname string) (string, *EnvoyConfig) {
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
func (cs *ConfigStore) MatchClone(hostname string) (string, *EnvoyConfig) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if i := cs.index.Lookup(hostname); i >= 0 {
		entry := cs.entries[i]
		cfg := cloneEnvoyConfig(entry.config)
		cfg.Envoy = hostname
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
	Inheritance     []InheritanceLevel    `json:"inheritance,omitempty"`
	Modules         []string              `json:"modules"`
	ModuleSources   map[string]string     `json:"module_sources,omitempty"` // module name → source file/role
	VarSources      []VarSourceEntry      `json:"var_sources"`
	FinalVars       map[string]any        `json:"final_vars"`
	ConditionalVars []string              `json:"conditional_vars,omitempty"`
	ResourceCount   int                   `json:"resource_count"`

	// Password Inheritance
	SecretRefs      []SecretTraceEntry      `json:"secret_refs,omitempty"`
	SecretsProvider string                  `json:"secrets_provider,omitempty"`

	// Dependency Inheritance
	Dependencies    []DependencyTraceEntry  `json:"dependencies,omitempty"`

	// Compliance Inheritance
	ComplianceTags  []ComplianceTraceEntry  `json:"compliance_tags,omitempty"`

	// Compliance Waivers
	WaiverFiles     []string                `json:"waiver_files,omitempty"` // waivers.vgo paths in the inheritance chain
	Waivers         []WaiverTraceEntry      `json:"waivers,omitempty"`

	// Per-Environment Overrides
	EnvOverrideFiles []string               `json:"env_override_files,omitempty"` // per_env_overrides.vgo paths in ancestry
	EnvOverrideEnv   string                 `json:"env_override_env,omitempty"`   // which env block was applied

	// Conditional Inheritance
	Conditionals    []ConditionalTraceEntry `json:"conditionals,omitempty"`
}

// FilterByModule returns a copy of the trace filtered to only show
// inheritance levels, modules, and role expansions relevant to the
// given module name. Vars are kept unfiltered since they apply globally.
func (t *ConfigTrace) FilterByModule(module string) *ConfigTrace {
	if module == "" || !t.Matched {
		return t
	}

	out := *t

	// Filter inheritance to levels that contribute this module.
	out.Inheritance = nil
	for _, lvl := range t.Inheritance {
		for _, m := range lvl.Modules {
			if m == module {
				out.Inheritance = append(out.Inheritance, lvl)
				break
			}
		}
	}

	// Filter role expansion to entries that include this module.
	out.RoleExpansion = nil
	for _, re := range t.RoleExpansion {
		for _, m := range re.Modules {
			if m == module {
				out.RoleExpansion = append(out.RoleExpansion, re)
				break
			}
		}
	}

	// Filter modules list.
	out.Modules = nil
	for _, m := range t.Modules {
		if m == module {
			out.Modules = append(out.Modules, m)
		}
	}

	// Recount resources for filtered modules only.
	// (ResourceCount stays approximate — exact filtering would require
	// access to the module defs which the trace struct doesn't carry.)

	// Filter secret refs to the given module.
	out.SecretRefs = nil
	for _, sr := range t.SecretRefs {
		if sr.Module == module || sr.Module == "" {
			out.SecretRefs = append(out.SecretRefs, sr)
		}
	}

	// Filter dependencies to the given module.
	out.Dependencies = nil
	for _, d := range t.Dependencies {
		if strings.HasPrefix(d.From, module+"/") || d.From == module ||
			strings.HasPrefix(d.To, module+"/") || d.To == module {
			out.Dependencies = append(out.Dependencies, d)
		}
	}

	// Filter compliance tags to the given module.
	out.ComplianceTags = nil
	filteredFrameworks := make(map[string]bool)
	for _, ct := range t.ComplianceTags {
		if ct.Module == module {
			out.ComplianceTags = append(out.ComplianceTags, ct)
			for fw := range ct.Frameworks {
				filteredFrameworks[fw] = true
			}
		}
	}

	// Filter waivers to frameworks relevant to the filtered module.
	out.Waivers = nil
	for _, w := range t.Waivers {
		if filteredFrameworks[w.Framework] {
			out.Waivers = append(out.Waivers, w)
		}
	}

	// Filter conditionals to the given module.
	out.Conditionals = nil
	for _, c := range t.Conditionals {
		if c.Module == module {
			out.Conditionals = append(out.Conditionals, c)
		}
	}

	return &out
}

// InheritanceLevel describes one level in the directory inheritance chain.
type InheritanceLevel struct {
	Dir       string         `json:"dir"`                // relative dir path (e.g., "production/web")
	File      string         `json:"file"`               // filename (e.g., "common.vgo")
	Modules   []string       `json:"modules,omitempty"`  // modules contributed at this level
	Roles     []string       `json:"roles,omitempty"`    // roles contributed at this level
	Vars      map[string]any `json:"vars,omitempty"`     // vars contributed at this level
	Overrides []string       `json:"overrides,omitempty"` // var keys that override a parent value
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

// SecretTraceEntry tracks a secret: reference in the resolved config.
type SecretTraceEntry struct {
	Key    string `json:"key"`    // var name or resource attribute path
	Path   string `json:"path"`   // the secret path (e.g., "vigo/db/dsn") — never the resolved value
	Source string `json:"source"` // "var", "resource:<module>/<resource>/<attr>", "credential"
	Module string `json:"module"` // module that introduced it (empty for envoy-level vars)
}

// DependencyTraceEntry tracks a dependency relationship between modules/resources.
type DependencyTraceEntry struct {
	From  string `json:"from"`  // source module or module/resource
	To    string `json:"to"`    // target module or module/resource
	Type  string `json:"type"`  // "depends_on", "before", "notify", "subscribes", "watch_secret"
	Level string `json:"level"` // "module" or "resource"
}

// ComplianceTraceEntry tracks compliance framework tags on a resolved module.
type ComplianceTraceEntry struct {
	Module     string              `json:"module"`
	Frameworks map[string][]string `json:"frameworks"` // framework → control IDs
}

// WaiverFileEntry stores per-file waiver data for trace display.
type WaiverFileEntry struct {
	RelPath string                      // e.g., "waivers.vgo" or "production/waivers.vgo"
	Waivers map[string][]*WaiverEntry   // standard → entries
}

// WaiverTraceEntry tracks a compliance waiver from waivers.vgo.
type WaiverTraceEntry struct {
	SourceFile string `json:"source_file"`
	Framework  string `json:"framework"`
	Control    string `json:"control"`
	Reason     string `json:"reason"`
	ApprovedBy string `json:"approved_by"`
	Expires    string `json:"expires,omitempty"`
}

// ConditionalTraceEntry tracks a when: expression on a module or resource.
type ConditionalTraceEntry struct {
	Module       string `json:"module"`                  // module name
	Resource     string `json:"resource,omitempty"`      // resource name (empty for module-level)
	When         string `json:"when"`                    // the raw when expression
	ResolvedWhen string `json:"resolved_when,omitempty"` // expression with vars replaced by resolved values
	EvalSide     string `json:"eval_side"`               // "server" or "agent"
}

// Trace returns a detailed ConfigTrace for the given hostname, showing
// MatchDirPaths returns a map of match pattern → directory path for all entries.
// Used by the inventory page for directory-based grouping.
func (cs *ConfigStore) MatchDirPaths() map[string]string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	result := make(map[string]string, len(cs.envoyEntries))
	for _, e := range cs.envoyEntries {
		path := e.DirPath
		if path == "" {
			path = "(root)"
		}
		result[e.Match] = path
	}
	return result
}

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

	// Find the original EnvoyEntry to trace var sources.
	var envoyEntry *EnvoyEntry
	for i := range cs.envoyEntries {
		if cs.envoyEntries[i].Match == entry.match {
			envoyEntry = &cs.envoyEntries[i]
			break
		}
	}
	if envoyEntry == nil {
		return trace
	}

	// Build inheritance chain for trace output, tracking vars and overrides.
	moduleSources := make(map[string]string)
	seenVars := make(map[string]bool)
	if envoyEntry.DirPath != "" && len(cs.commonConfigs) > 0 {
		chain := inheritanceChain(envoyEntry.DirPath, cs.commonConfigs)
		for _, cc := range chain {
			level := InheritanceLevel{
				Dir:  cc.DirPath,
				File: "common.vgo",
			}
			for _, ref := range cc.Modules {
				level.Modules = append(level.Modules, ref.Name)
				if _, exists := moduleSources[ref.Name]; !exists {
					src := "common.vgo"
					if cc.DirPath != "" {
						src = cc.DirPath + "/common.vgo"
					}
					moduleSources[ref.Name] = src
				}
			}
			level.Roles = roleRefNames(cc.Roles)
			if len(cc.Vars) > 0 {
				level.Vars = make(map[string]any, len(cc.Vars))
				for k, v := range cc.Vars {
					level.Vars[k] = v
					if seenVars[k] {
						level.Overrides = append(level.Overrides, k)
					}
					seenVars[k] = true
				}
			}
			trace.Inheritance = append(trace.Inheritance, level)
		}
	}

	// Collect waivers.vgo files along the inheritance path from per-file data.
	{
		// Build the set of directories in the inheritance path.
		pathDirs := map[string]bool{"": true} // root
		if envoyEntry.DirPath != "" {
			parts := strings.Split(envoyEntry.DirPath, "/")
			for i := range parts {
				pathDirs[strings.Join(parts[:i+1], "/")] = true
			}
		}
		for _, wf := range cs.waiversByFile {
			wfDir := filepath.ToSlash(filepath.Dir(wf.RelPath))
			if wfDir == "." {
				wfDir = ""
			}
			if pathDirs[wfDir] {
				trace.WaiverFiles = append(trace.WaiverFiles, wf.RelPath)
			}
		}
	}

	// Collect per_env_overrides.vgo files along the inheritance path.
	if envoyEntry.Environment != "" && len(cs.perEnvOverrides) > 0 {
		pathDirs := map[string]bool{"": true}
		if envoyEntry.DirPath != "" {
			parts := strings.Split(envoyEntry.DirPath, "/")
			for i := range parts {
				pathDirs[strings.Join(parts[:i+1], "/")] = true
			}
		}
		for dp := range pathDirs {
			if envMap, ok := cs.perEnvOverrides[dp]; ok {
				if _, ok := envMap[envoyEntry.Environment]; ok {
					path := "per_env_overrides.vgo"
					if dp != "" {
						path = dp + "/per_env_overrides.vgo"
					}
					trace.EnvOverrideFiles = append(trace.EnvOverrideFiles, path)
				}
			}
		}
		trace.EnvOverrideEnv = envoyEntry.Environment
	}

	// Add the leaf entry file as the final level in the inheritance chain.
	{
		sourceFile := envoyEntry.SourceFile
		if sourceFile == "" {
			sourceFile = "envoys.vgo"
		}
		leafLevel := InheritanceLevel{
			Dir:  envoyEntry.DirPath,
			File: sourceFile,
		}
		for _, ref := range envoyEntry.OwnModules {
			leafLevel.Modules = append(leafLevel.Modules, ref.Name)
			if _, exists := moduleSources[ref.Name]; !exists {
				src := sourceFile
				if envoyEntry.DirPath != "" {
					src = envoyEntry.DirPath + "/" + sourceFile
				}
				moduleSources[ref.Name] = src
			}
		}
		leafLevel.Roles = roleRefNames(envoyEntry.OwnRoles)
		if len(envoyEntry.OwnVars) > 0 {
			leafLevel.Vars = make(map[string]any, len(envoyEntry.OwnVars))
			for k, v := range envoyEntry.OwnVars {
				leafLevel.Vars[k] = v
				if seenVars[k] {
					leafLevel.Overrides = append(leafLevel.Overrides, k)
				}
				seenVars[k] = true
			}
		}
		trace.Inheritance = append(trace.Inheritance, leafLevel)
	}

	trace.Environment = envoyEntry.Environment
	trace.Roles = roleRefNames(envoyEntry.Roles)

	// Trace role expansion.
	for _, roleRef := range envoyEntry.Roles {
		role, ok := cs.roleDefs[roleRef.Name]
		if !ok {
			continue
		}
		rte := RoleTraceEntry{Role: roleRef.Name}
		for _, incName := range role.Includes {
			rte.Includes = append(rte.Includes, incName)
			if inc, ok := cs.roleDefs[incName]; ok {
				for _, ref := range inc.Modules {
					rte.Modules = append(rte.Modules, ref.Name)
					if _, exists := moduleSources[ref.Name]; !exists {
						moduleSources[ref.Name] = "role:" + roleRef.Name
					}
				}
			}
		}
		for _, ref := range role.Modules {
			rte.Modules = append(rte.Modules, ref.Name)
			if _, exists := moduleSources[ref.Name]; !exists {
				moduleSources[ref.Name] = "role:" + roleRef.Name
			}
		}
		trace.RoleExpansion = append(trace.RoleExpansion, rte)
	}

	// Collect module names from resolved config.
	cfg := entry.config
	for _, mod := range cfg.Modules {
		trace.Modules = append(trace.Modules, mod.Name)
		if _, exists := moduleSources[mod.Name]; !exists {
			moduleSources[mod.Name] = "direct"
		}
	}
	trace.ModuleSources = moduleSources

	// Trace var sources by replaying the merge order.
	varSource := make(map[string]string) // key → source

	// 1. Module defaults
	seen := make(map[string]bool)
	for _, roleRef := range envoyEntry.Roles {
		role, ok := cs.roleDefs[roleRef.Name]
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
	for _, ref := range envoyEntry.Modules {
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
	for _, relPath := range envoyEntry.VarsFrom {
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
	for k := range envoyEntry.Vars {
		varSource[k] = "inline"
	}

	// 4. environment_overrides
	if envoyEntry.Environment != "" {
		if envOverrides, ok := envoyEntry.EnvironmentOverrides[envoyEntry.Environment]; ok {
			for k := range envOverrides {
				varSource[k] = "environment_overrides:" + envoyEntry.Environment
			}
		}
	}

	// Build ordered var sources: trunk-to-leaf by source priority.
	// module defaults → vars_from → inline → environment_overrides.
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
	sort.SliceStable(trace.VarSources, func(i, j int) bool {
		return varSourcePriority(trace.VarSources[i].Source) < varSourcePriority(trace.VarSources[j].Source)
	})
	trace.FinalVars = cfg.Vars

	// Conditional vars
	for _, cv := range cfg.ConditionalVars {
		trace.ConditionalVars = append(trace.ConditionalVars, cv.Name)
	}

	// Resource count
	for _, mod := range cfg.Modules {
		trace.ResourceCount += len(mod.Resources)
	}

	// --- Password Inheritance ---
	trace.SecretsProvider = cs.backend

	// Scan module definition vars for secret: references.
	for _, mod := range cfg.Modules {
		if def, ok := cs.moduleDefs[mod.Name]; ok {
			for k, v := range def.Vars {
				if s, ok := v.(string); ok && strings.HasPrefix(s, "secret:") {
					trace.SecretRefs = append(trace.SecretRefs, SecretTraceEntry{
						Key:    k,
						Path:   strings.TrimPrefix(s, "secret:"),
						Source: "var",
						Module: mod.Name,
					})
				}
			}
			// Scan resource attributes for secret: references.
			for _, res := range def.Resources {
				for attrKey, attrVal := range res.Attributes {
					if s, ok := attrVal.(string); ok && strings.HasPrefix(s, "secret:") {
						trace.SecretRefs = append(trace.SecretRefs, SecretTraceEntry{
							Key:    attrKey,
							Path:   strings.TrimPrefix(s, "secret:"),
							Source: "resource:" + mod.Name + "/" + res.Name + "/" + attrKey,
							Module: mod.Name,
						})
					}
				}
			}
		}
	}

	// Scan envoy-level vars for secret: references.
	for k, v := range envoyEntry.Vars {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "secret:") {
			trace.SecretRefs = append(trace.SecretRefs, SecretTraceEntry{
				Key:    k,
				Path:   strings.TrimPrefix(s, "secret:"),
				Source: "var",
				Module: "",
			})
		}
	}

	// Network device credential.
	if envoyEntry.Credential != "" && strings.HasPrefix(envoyEntry.Credential, "secret:") {
		trace.SecretRefs = append(trace.SecretRefs, SecretTraceEntry{
			Key:    "credential",
			Path:   strings.TrimPrefix(envoyEntry.Credential, "secret:"),
			Source: "credential",
			Module: "",
		})
	}

	// --- Dependency Inheritance ---
	for _, mod := range cfg.Modules {
		// Module-level depends_on.
		for _, dep := range mod.DependsOn {
			trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
				From: mod.Name, To: dep, Type: "depends_on", Level: "module",
			})
		}
		// Module-level before (from definition).
		if def, ok := cs.moduleDefs[mod.Name]; ok {
			for _, before := range def.Before {
				trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
					From: mod.Name, To: before, Type: "before", Level: "module",
				})
			}
		}
		// Resource-level dependencies.
		for _, res := range mod.Resources {
			prefix := mod.Name + "/" + res.Name
			for _, dep := range res.DependsOn {
				trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
					From: prefix, To: mod.Name + "/" + dep, Type: "depends_on", Level: "resource",
				})
			}
			for _, before := range res.Before {
				trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
					From: prefix, To: mod.Name + "/" + before, Type: "before", Level: "resource",
				})
			}
			for _, n := range res.Notify {
				trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
					From: prefix, To: mod.Name + "/" + n, Type: "notify", Level: "resource",
				})
			}
			for _, s := range res.Subscribes {
				trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
					From: prefix, To: mod.Name + "/" + s, Type: "subscribes", Level: "resource",
				})
			}
			for _, ws := range res.WatchSecret {
				trace.Dependencies = append(trace.Dependencies, DependencyTraceEntry{
					From: prefix, To: ws, Type: "watch_secret", Level: "resource",
				})
			}
		}
	}

	// --- Compliance Inheritance ---
	for _, mod := range cfg.Modules {
		if def, ok := cs.moduleDefs[mod.Name]; ok && len(def.Compliance) > 0 {
			trace.ComplianceTags = append(trace.ComplianceTags, ComplianceTraceEntry{
				Module:     mod.Name,
				Frameworks: def.Compliance,
			})
		}
	}

	// --- Compliance Waivers ---
	// Collect waivers from per-file data along the inheritance path,
	// filtered to frameworks that apply to this envoy's compliance tags.
	envoyFrameworks := make(map[string]bool)
	for _, ct := range trace.ComplianceTags {
		for fw := range ct.Frameworks {
			envoyFrameworks[fw] = true
		}
	}
	if len(envoyFrameworks) > 0 {
		pathDirs := map[string]bool{"": true}
		if envoyEntry.DirPath != "" {
			parts := strings.Split(envoyEntry.DirPath, "/")
			for i := range parts {
				pathDirs[strings.Join(parts[:i+1], "/")] = true
			}
		}
		seen := make(map[string]bool)
		for _, wf := range cs.waiversByFile {
			wfDir := filepath.ToSlash(filepath.Dir(wf.RelPath))
			if wfDir == "." {
				wfDir = ""
			}
			if !pathDirs[wfDir] {
				continue
			}
			for framework, entries := range wf.Waivers {
				if !envoyFrameworks[framework] {
					continue
				}
				for _, w := range entries {
					key := framework + "/" + w.Control
					if seen[key] {
						continue
					}
					seen[key] = true
					trace.Waivers = append(trace.Waivers, WaiverTraceEntry{
						SourceFile: wf.RelPath,
						Framework:  framework,
						Control:    w.Control,
						Reason:     w.Reason,
						ApprovedBy: w.ApprovedBy,
						Expires:    w.Expires,
					})
				}
			}
		}
		// Sort waivers by path depth (shallowest first, root at top).
		sort.Slice(trace.Waivers, func(i, j int) bool {
			di := strings.Count(trace.Waivers[i].SourceFile, "/")
			dj := strings.Count(trace.Waivers[j].SourceFile, "/")
			if di != dj {
				return di < dj
			}
			return trace.Waivers[i].SourceFile < trace.Waivers[j].SourceFile
		})
	}

	// --- Conditional Inheritance ---
	for _, mod := range cfg.Modules {
		// Module-level when.
		if mod.When != "" {
			rendered := renderWhenTemplates(mod.When, cfg.Vars)
			resolved := resolveWhenDisplay(rendered, cfg.Vars)
			// Show original template expression when Go templates were rendered.
			if strings.Contains(mod.When, "{{") && rendered != mod.When {
				resolved = mod.When
			}
			trace.Conditionals = append(trace.Conditionals, ConditionalTraceEntry{
				Module:       mod.Name,
				When:         rendered,
				ResolvedWhen: resolved,
				EvalSide:     classifyWhen(rendered),
			})
		}
		// Resource-level when.
		for _, res := range mod.Resources {
			if res.When != "" {
				rendered := renderWhenTemplates(res.When, cfg.Vars)
				resolved := resolveWhenDisplay(rendered, cfg.Vars)
				// Show original template expression when Go templates were rendered.
				if strings.Contains(res.When, "{{") && rendered != res.When {
					resolved = res.When
				}
				trace.Conditionals = append(trace.Conditionals, ConditionalTraceEntry{
					Module:       mod.Name,
					Resource:     res.Name,
					When:         rendered,
					ResolvedWhen: resolved,
					EvalSide:     classifyWhen(rendered),
				})
			}
		}
	}

	return trace
}

// classifyWhen determines whether a when: expression evaluates server-side or agent-side.
func classifyWhen(expr string) string {
	parsed, err := conditionals.ParseWhenExpr(expr)
	if err != nil {
		return "agent" // unknown expressions assumed agent-side
	}
	if conditionals.ContainsFuncCalls(parsed) {
		return "agent"
	}
	return "server"
}

// resolveWhenDisplay returns the when expression with vars and trait functions
// shown with their resolved values in {{braces}}. Vars are resolved from the
// final vars map; trait function calls are shown as {{func(args)}} since they
// require agent-side evaluation.
//
// When expressions may contain Go template syntax ({{ .Vars.name }}) which is
// rendered first against the vars map before AST parsing.
func resolveWhenDisplay(expr string, vars map[string]any) string {
	// Render Go template expressions in the when string.
	rendered := renderWhenTemplates(expr, vars)

	parsed, err := conditionals.ParseWhenExpr(rendered)
	if err != nil {
		return rendered
	}
	return resolveExprDisplay(parsed, vars)
}

// renderWhenTemplates evaluates Go template expressions ({{ .Vars.x }}) in a
// when string using the resolved vars map. Returns the original string on error.
func renderWhenTemplates(expr string, vars map[string]any) string {
	if !strings.Contains(expr, "{{") {
		return expr
	}
	tmpl, err := texttemplate.New("when").Parse(expr)
	if err != nil {
		return expr
	}
	data := struct{ Vars map[string]any }{Vars: vars}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return expr
	}
	return buf.String()
}

func resolveExprDisplay(expr conditionals.WhenExpr, vars map[string]any) string {
	switch e := expr.(type) {
	case *conditionals.BoolLitExpr:
		if e.Value {
			return "true"
		}
		return "false"
	case *conditionals.VarRefExpr:
		if val, ok := vars[e.Name]; ok {
			return fmt.Sprintf("%v", val)
		}
		return e.Name
	case *conditionals.NotWhenExpr:
		inner := resolveExprDisplay(e.Operand, vars)
		switch e.Operand.(type) {
		case *conditionals.AndWhenExpr, *conditionals.OrWhenExpr:
			return "!(" + inner + ")"
		}
		return "!" + inner
	case *conditionals.AndWhenExpr:
		left := resolveExprDisplay(e.Left, vars)
		right := resolveExprDisplay(e.Right, vars)
		if _, ok := e.Left.(*conditionals.OrWhenExpr); ok {
			left = "(" + left + ")"
		}
		if _, ok := e.Right.(*conditionals.OrWhenExpr); ok {
			right = "(" + right + ")"
		}
		return left + " && " + right
	case *conditionals.OrWhenExpr:
		return resolveExprDisplay(e.Left, vars) + " || " + resolveExprDisplay(e.Right, vars)
	case *conditionals.FuncCallExpr:
		var quotedArgs []string
		for _, arg := range e.Args {
			quotedArgs = append(quotedArgs, "'"+arg+"'")
		}
		return e.Name + "(" + strings.Join(quotedArgs, ", ") + ")"
	default:
		return expr.(fmt.Stringer).String()
	}
}

// varSourcePriority returns a sort key for VarSourceEntry ordering.
// Lower values come first: module defaults (trunk) → vars_from → inline → environment_overrides (leaf).
func varSourcePriority(source string) int {
	switch {
	case strings.HasPrefix(source, "module:"):
		return 0
	case strings.HasPrefix(source, "vars_from:"):
		return 1
	case source == "inline":
		return 2
	case strings.HasPrefix(source, "environment_overrides:"):
		return 3
	default:
		return 4
	}
}

// RecommendModulesForControl returns module names that cover the given
// control ID for a standard. Checks both loaded modules and example modules.
func (cs *ConfigStore) RecommendModulesForControl(standard, controlID string) []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if idx, ok := cs.complianceIndex[standard]; ok {
		if mods, ok := idx[controlID]; ok {
			return mods
		}
	}
	return nil
}

// ComplianceControlIndex returns the control_id → module_names map for a standard.
// Returns nil if the standard has no entries in the compliance index.
func (cs *ConfigStore) ComplianceControlIndex(standard string) map[string][]string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.complianceIndex[standard]
}

// StandardKeyForSlug maps URL slugs to the compliance tag keys used in
// module definitions.
var StandardKeyForSlug = map[string]string{
	"compliance":      "hipaa",
	"hitrust":         "hitrust",
	"soc2":            "soc2",
	"pci":             "pci-dss",
	"nist":            "nist-800-53",
	"iso27001":        "iso-27001",
	"gdpr":            "gdpr",
	"nerccip":         "nerc-cip",
	"cyberessentials": "cyber-essentials",
	"iec62443":        "iec-62443",
	"sox":             "sox",
	"finra":           "finra",
	"mifid2":          "mifid2",
	"ccss":             "ccss",
	"fda21cfr11":       "fda-21cfr11",
	"gaming":           "gaming",
	"ciswindows":        "cis-windows",
	"cisubuntu":         "cis-ubuntu",
	"cisrhel":           "cis-rhel",
	"abacyber":          "aba-cyber",
	"nydfscyber":        "ny-dfs-cyber",
}

// SlugForStandardKey is the inverse of StandardKeyForSlug.
var SlugForStandardKey = func() map[string]string {
	m := make(map[string]string, len(StandardKeyForSlug))
	for slug, key := range StandardKeyForSlug {
		m[key] = slug
	}
	return m
}()

// buildComplianceIndex creates a reverse index from module compliance tags.
func buildComplianceIndex(moduleDefs map[string]*ModuleDefinition) map[string]map[string][]string {
	idx := make(map[string]map[string][]string)
	for _, mod := range moduleDefs {
		for standard, controlIDs := range mod.Compliance {
			if _, ok := idx[standard]; !ok {
				idx[standard] = make(map[string][]string)
			}
			for _, cid := range controlIDs {
				idx[standard][cid] = append(idx[standard][cid], mod.Name)
			}
		}
	}
	return idx
}

// EnvoyStandardCoverage resolves which controls of a given compliance standard
// an envoy covers through its assigned modules. The standard parameter is a
// compliance tag key (e.g. "pci-dss"), not a URL slug.
func (cs *ConfigStore) EnvoyStandardCoverage(hostname, standard string) *StandardCoverage {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	controlIndex, ok := cs.complianceIndex[standard]
	if !ok || len(controlIndex) == 0 {
		return nil
	}

	// Resolve which modules this envoy has assigned.
	envoyModules := make(map[string]bool)
	if i := cs.index.Lookup(hostname); i >= 0 {
		cfg := cs.entries[i].config
		for _, mod := range cfg.Modules {
			envoyModules[mod.Name] = true
		}
	}

	sc := &StandardCoverage{Standard: standard}

	controlIDs := make([]string, 0, len(controlIndex))
	for cid := range controlIndex {
		controlIDs = append(controlIDs, cid)
	}
	sort.Strings(controlIDs)

	for _, cid := range controlIDs {
		modulesForControl := controlIndex[cid]
		var assigned []string
		for _, mod := range modulesForControl {
			if envoyModules[mod] {
				assigned = append(assigned, mod)
			}
		}
		if len(assigned) > 0 {
			sc.CoveredControls = append(sc.CoveredControls, ControlCoverage{
				ControlID: cid,
				Modules:   assigned,
			})
		} else {
			sc.MissingControls = append(sc.MissingControls, cid)
		}
	}

	sc.TotalControls = len(controlIDs)
	if sc.TotalControls > 0 {
		sc.CoveragePercent = float64(len(sc.CoveredControls)) / float64(sc.TotalControls) * 100
	}

	return sc
}

// envoysDir returns the path to the envoys subdirectory.
// TreeNode represents one node in the config directory hierarchy for
// the config tree command. It shows inheritance at each level.
type TreeNode struct {
	Dir      string      `json:"dir"`                // relative dir path ("", "production", "production/web")
	File     string      `json:"file,omitempty"`     // filename if this is a leaf or common.vgo
	Modules  []string    `json:"modules,omitempty"`  // modules defined at this level
	Roles    []string    `json:"roles,omitempty"`    // roles defined at this level
	Match    string      `json:"match,omitempty"`    // match pattern (leaf entries only)
	Total    []string    `json:"total,omitempty"`    // accumulated modules (leaf entries only)
	Excluded []string    `json:"excluded,omitempty"` // excluded modules (leaf entries only)
	Children []*TreeNode `json:"children,omitempty"` // subdirectories
}

// Tree returns the directory inheritance structure for visualization.
func (cs *ConfigStore) Tree() *TreeNode {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	root := &TreeNode{Dir: ""}

	// Add common.vgo at root.
	if cc, ok := cs.commonConfigs[""]; ok {
		root.File = "common.vgo"
		for _, ref := range cc.Modules {
			root.Modules = append(root.Modules, ref.Name)
		}
		root.Roles = roleRefNames(cc.Roles)
	}

	// Build tree from envoy entries grouped by directory.
	dirNodes := map[string]*TreeNode{"": root}

	// Ensure all common.vgo directories have nodes.
	for dirPath, cc := range cs.commonConfigs {
		if dirPath == "" {
			continue
		}
		node := getOrCreateNode(dirNodes, root, dirPath)
		node.File = "common.vgo"
		for _, ref := range cc.Modules {
			node.Modules = append(node.Modules, ref.Name)
		}
		node.Roles = roleRefNames(cc.Roles)
	}

	// Add leaf entries.
	for _, entry := range cs.envoyEntries {
		dirPath := entry.DirPath
		parent := getOrCreateNode(dirNodes, root, dirPath)

		leaf := &TreeNode{
			Dir:      dirPath,
			Match:    entry.Match,
			Excluded: entry.ExcludeModules,
		}

		// Own modules.
		for _, ref := range entry.Modules {
			leaf.Modules = append(leaf.Modules, ref.Name)
		}
		leaf.Roles = roleRefNames(entry.Roles)

		// Compute total accumulated modules from resolved config.
		for i := range cs.entries {
			if cs.entries[i].match == entry.Match {
				for _, mod := range cs.entries[i].config.Modules {
					leaf.Total = append(leaf.Total, mod.Name)
				}
				break
			}
		}

		parent.Children = append(parent.Children, leaf)
	}

	return root
}

// SearchResult holds results for the config search command.
type SearchResult struct {
	Entries []SearchEntry `json:"entries"`
}

// SearchEntry is a single search result showing a envoy entry and its context.
type SearchEntry struct {
	Match       string             `json:"match"`
	Dir         string             `json:"dir"`
	Inheritance []InheritanceLevel `json:"inheritance,omitempty"`
	OwnModules  []string           `json:"own_modules"`
	OwnRoles    []string           `json:"own_roles,omitempty"`
	Total       []string           `json:"total"`
	Excluded    []string           `json:"excluded,omitempty"`
	Vars        map[string]any     `json:"vars,omitempty"`
}

// Search finds envoy entries matching the given criteria.
// query is matched against match: patterns using glob-style matching.
// moduleName filters to entries that use a specific module (inherited or direct).
// varName filters to entries that define a specific var.
func (cs *ConfigStore) Search(query, moduleName, varName string) *SearchResult {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result := &SearchResult{Entries: []SearchEntry{}}

	for _, entry := range cs.envoyEntries {
		// Filter by match pattern.
		if query != "" && !globContains(entry.Match, query) {
			continue
		}

		// Build the entry.
		se := SearchEntry{
			Match:    entry.Match,
			Dir:      entry.DirPath,
			Excluded: entry.ExcludeModules,
		}

		// Own modules/roles (before inheritance).
		for _, ref := range entry.Modules {
			se.OwnModules = append(se.OwnModules, ref.Name)
		}
		se.OwnRoles = roleRefNames(entry.Roles)

		// Inheritance chain.
		if entry.DirPath != "" && len(cs.commonConfigs) > 0 {
			chain := inheritanceChain(entry.DirPath, cs.commonConfigs)
			for _, cc := range chain {
				level := InheritanceLevel{
					Dir:  cc.DirPath,
					File: "common.vgo",
				}
				for _, ref := range cc.Modules {
					level.Modules = append(level.Modules, ref.Name)
				}
				level.Roles = roleRefNames(cc.Roles)
				se.Inheritance = append(se.Inheritance, level)
			}
		}

		// Total modules from resolved config.
		for i := range cs.entries {
			if cs.entries[i].match == entry.Match {
				cfg := cs.entries[i].config
				for _, mod := range cfg.Modules {
					se.Total = append(se.Total, mod.Name)
				}
				se.Vars = cfg.Vars
				break
			}
		}

		// Filter by module name (must appear in total).
		if moduleName != "" {
			found := false
			for _, m := range se.Total {
				if m == moduleName {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter by var name.
		if varName != "" {
			if _, ok := se.Vars[varName]; !ok {
				continue
			}
		}

		result.Entries = append(result.Entries, se)
	}

	return result
}

// getOrCreateNode ensures a TreeNode exists for the given directory path,
// creating intermediate nodes as needed.
func getOrCreateNode(nodes map[string]*TreeNode, root *TreeNode, dirPath string) *TreeNode {
	if dirPath == "" {
		return root
	}
	if node, ok := nodes[dirPath]; ok {
		return node
	}

	parts := strings.Split(dirPath, string(filepath.Separator))
	current := root
	for i := range parts {
		seg := filepath.Join(parts[:i+1]...)
		if node, ok := nodes[seg]; ok {
			current = node
			continue
		}
		node := &TreeNode{Dir: seg}
		current.Children = append(current.Children, node)
		nodes[seg] = node
		current = node
	}
	return current
}

// globContains checks if a pattern matches a query using simple substring
// and glob matching. The query can contain * wildcards.
func globContains(pattern, query string) bool {
	// Exact substring match.
	if strings.Contains(pattern, query) {
		return true
	}
	// Try glob: query is a pattern, pattern is the "filename".
	if matched, _ := filepath.Match(query, pattern); matched {
		return true
	}
	// Try reverse: pattern is a pattern, query is the "filename".
	if matched, _ := filepath.Match(pattern, query); matched {
		return true
	}
	return false
}

// modulesDir returns the path to the modules subdirectory.
func (cs *ConfigStore) modulesDir() string { return filepath.Join(cs.dir, "modules") }

// templatesDir returns the path to the templates subdirectory.
func (cs *ConfigStore) templatesDir() string { return filepath.Join(cs.dir, "templates") }

// loadAll reads module defs, role defs, and envoy YAML files, resolves
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

	// Load roles: try roles.vgo at root first, fall back to roles/ directory.
	roleDefs, err := loadRolesFile(cs.dir)
	if err != nil {
		return fmt.Errorf("loading roles: %w", err)
	}

	envoyEntries, commonConfigs, err := loadEnvoysDir(cs.dir)
	if err != nil {
		return fmt.Errorf("loading envoys: %w", err)
	}

	waiverMap, waiverFiles, err := loadWaiverConfigs(cs.dir)
	if err != nil {
		return fmt.Errorf("loading waivers: %w", err)
	}

	perEnvOverrides, err := loadPerEnvOverrides(cs.dir)
	if err != nil {
		return fmt.Errorf("loading per_env_overrides: %w", err)
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
	if err := validateDeviceEntries(envoyEntries); err != nil {
		return err
	}

	// Apply directory inheritance: prepend inherited modules/roles/vars
	// from common.vgo files in ancestor directories.
	for i := range envoyEntries {
		applyInheritance(&envoyEntries[i], commonConfigs, perEnvOverrides)
	}

	var entries []resolvedEntry
	var allSecretErrs []*SecretError
	for i, entry := range envoyEntries {
		cfg, err := cs.resolveEntry(entry, moduleDefs, roleDefs)
		if err != nil {
			// Secret errors are collected across all entries so the
			// operator can fix everything in one restart cycle.
			var se *SecretErrors
			if errors.As(err, &se) {
				allSecretErrs = append(allSecretErrs, se.Errors...)
				continue
			}
			return fmt.Errorf("resolving envoy entry %d (match=%q): %w", i, entry.Match, err)
		}
		entries = append(entries, resolvedEntry{match: entry.Match, config: cfg})
	}
	if len(allSecretErrs) > 0 {
		return &SecretErrors{Errors: allSecretErrs, Backend: cs.backend}
	}

	// Pre-compute module content hashes and Merkle roots for delta transfer.
	for i := range entries {
		cfg := entries[i].config
		for j := range cfg.Modules {
			cfg.Modules[j].ContentHash = HashModuleContent(cfg.Modules[j])
		}
		cfg.MerkleRoot = MerkleRoot(cfg.Modules, cfg.Vars, cfg.VarOrder)
	}

	// Build pattern index for fast hostname lookup.
	idx := buildPatternIndex(entries)

	cs.mu.Lock()
	cs.entries = entries
	cs.index = idx
	cs.rawModules = moduleDefs
	cs.envoyEntries = envoyEntries
	cs.roleDefs = roleDefs
	cs.moduleDefs = moduleDefs
	cs.complianceIndex = buildComplianceIndex(moduleDefs)
	cs.commonConfigs = commonConfigs
	cs.perEnvOverrides = perEnvOverrides
	cs.waivers = waiverMap
	cs.waiversByFile = waiverFiles
	cs.mu.Unlock()

	// Count total waivers across all standards.
	totalWaivers := 0
	for _, controls := range waiverMap {
		totalWaivers += len(controls)
	}

	v := cs.version.Add(1)

	slog.Info("config loaded",
		"envoys", len(entries),
		"modules", len(moduleDefs),
		"roles", len(roleDefs),
		"waivers", totalWaivers,
		"version", v)

	return nil
}

// loadVarsFile reads a YAML file containing a flat key-value map of vars.
// The path is relative to the examples directory. Returns the vars map
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
	data = sanitizeYAML(data)
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

// roleRefNames extracts the Name fields from a slice of RoleRefs.
func roleRefNames(refs []RoleRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names
}

// resolveEntry converts a EnvoyEntry into a fully resolved EnvoyConfig.
func (cs *ConfigStore) resolveEntry(entry EnvoyEntry, moduleDefs map[string]*ModuleDefinition, roleDefs map[string]*RoleDefinition) (*EnvoyConfig, error) {
	// Collect module refs: roles first (with includes), then direct modules.
	// First ref wins for conditional fields (matching existing var merge behavior).
	var moduleRefs []ModuleRef
	seen := make(map[string]bool)
	for _, roleRef := range entry.Roles {
		role, ok := roleDefs[roleRef.Name]
		if !ok {
			return nil, fmt.Errorf("role %q does not exist — add a role definition to roles/", roleRef.Name)
		}
		// Expand single-level includes before the role's own modules.
		for _, incName := range role.Includes {
			inc, ok := roleDefs[incName]
			if !ok {
				return nil, fmt.Errorf("role %q includes %q which does not exist", roleRef.Name, incName)
			}
			for _, ref := range inc.Modules {
				if !seen[ref.Name] {
					seen[ref.Name] = true
					mref := ref
					// When inheritance: module's own when > included role when >
					// parent role when > role ref when. Most specific wins.
					if mref.When == "" {
						if inc.When != "" {
							mref.When = inc.When
						} else if role.When != "" {
							mref.When = role.When
						} else if roleRef.When != "" {
							mref.When = roleRef.When
						}
					}
					moduleRefs = append(moduleRefs, mref)
				}
			}
		}
		for _, ref := range role.Modules {
			if !seen[ref.Name] {
				seen[ref.Name] = true
				mref := ref
				if mref.When == "" {
					if role.When != "" {
						mref.When = role.When
					} else if roleRef.When != "" {
						mref.When = roleRef.When
					}
				}
				moduleRefs = append(moduleRefs, mref)
			}
		}
		// Expand conditional cases. Each case's when: is applied to
		// all its modules that don't have their own when: condition.
		for _, c := range role.Case {
			for _, ref := range c.Modules {
				if !seen[ref.Name] {
					seen[ref.Name] = true
					mref := ref
					if mref.When == "" {
						mref.When = c.When
					}
					moduleRefs = append(moduleRefs, mref)
				}
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

	// Strip excluded resources from all modules.
	if len(entry.ExcludeResources) > 0 {
		excludeRes := make(map[string]bool, len(entry.ExcludeResources))
		for _, name := range entry.ExcludeResources {
			excludeRes[name] = true
		}
		for i := range modules {
			filtered := modules[i].Resources[:0]
			for _, res := range modules[i].Resources {
				if !excludeRes[res.Name] {
					filtered = append(filtered, res)
				}
			}
			modules[i].Resources = filtered
		}
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
				return nil, fmt.Errorf("module %q has before: [%s] but module %q is not assigned to this envoy", mod.Name, target, target)
			}
			modules[idx].DependsOn = appendUnique(modules[idx].DependsOn, mod.Name)
		}
	}

	// Extract module names for var collection below.
	moduleNames := make([]string, len(moduleRefs))
	for i, ref := range moduleRefs {
		moduleNames[i] = ref.Name
	}

	// Collect conditional vars and match vars from modules. Deduplicate by name
	// (first definition wins, matching var merge behavior).
	var condVars []conditionals.ConditionalVar
	condVarNames := make(map[string]bool)
	allMatchVars := make(map[string][]conditionals.MatchBranch)
	allMatchVarDefaults := make(map[string]string)
	for _, name := range moduleNames {
		for _, cv := range moduleDefs[name].ConditionalVars {
			if !condVarNames[cv.Name] {
				condVarNames[cv.Name] = true
				condVars = append(condVars, cv)
			}
		}
		for k, branches := range moduleDefs[name].MatchVars {
			if _, exists := allMatchVars[k]; !exists {
				allMatchVars[k] = branches
				if def, ok := moduleDefs[name].MatchVarDefaults[k]; ok {
					allMatchVarDefaults[k] = def
				}
			}
		}
	}

	// Build vars: module defaults → vars_from files → envoy-level overrides.
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
		// Skip envoy-level vars that would conflict with conditional var names.
		// Conditional vars take precedence -- they are evaluated at check-in time.
		if condVarNames[k] {
			continue
		}
		if _, exists := vars[k]; !exists {
			varOrder = append(varOrder, k)
		}
		vars[k] = v
	}

	cfg := &EnvoyConfig{
		Envoy:          entry.Match,
		MatchPattern:    entry.Match,
		Environment:     entry.Environment,
		Roles:           roleRefNames(entry.Roles),
		Modules:         modules,
		Vars:            vars,
		VarOrder:        varOrder,
		EnvironmentOverrides: entry.EnvironmentOverrides,
		ConditionalVars:      condVars,
		MatchVars:            allMatchVars,
		MatchVarDefaults:     allMatchVarDefaults,
		ObserveOnly:     entry.ObserveOnly,
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
// - gateway must reference an existing envoy entry without a device_type
// - credential must use the secret: prefix
// - gateway entries must not themselves have a device_type
func validateDeviceEntries(entries []EnvoyEntry) error {
	// Build lookup: match pattern → entry for gateway reference validation.
	byMatch := make(map[string]*EnvoyEntry, len(entries))
	for i := range entries {
		byMatch[entries[i].Match] = &entries[i]
	}

	for _, entry := range entries {
		if entry.DeviceType == "" && entry.Gateway == "" {
			continue // normal envoy
		}

		// device_type and gateway must be set together.
		if entry.DeviceType != "" && entry.Gateway == "" {
			return fmt.Errorf("envoy entry %q has device_type %q but no gateway — network devices require a gateway", entry.Match, entry.DeviceType)
		}
		if entry.Gateway != "" && entry.DeviceType == "" {
			return fmt.Errorf("envoy entry %q has gateway %q but no device_type — gateway is only for network devices", entry.Match, entry.Gateway)
		}

		// Validate device_type.
		if !ValidDeviceTypes[entry.DeviceType] {
			valid := make([]string, 0, len(ValidDeviceTypes))
			for k := range ValidDeviceTypes {
				valid = append(valid, k)
			}
			sort.Strings(valid)
			return fmt.Errorf("envoy entry %q has unsupported device_type %q (valid: %s)", entry.Match, entry.DeviceType, strings.Join(valid, ", "))
		}

		// Validate credential uses secret: prefix.
		if entry.Credential == "" {
			return fmt.Errorf("envoy entry %q (device_type=%s) requires a credential field", entry.Match, entry.DeviceType)
		}
		if !strings.HasPrefix(entry.Credential, secretPrefix) {
			return fmt.Errorf("envoy entry %q credential must use \"secret:\" prefix", entry.Match)
		}

		// Validate gateway references an existing entry.
		gw, ok := byMatch[entry.Gateway]
		if !ok {
			return fmt.Errorf("envoy entry %q references gateway %q which does not exist", entry.Match, entry.Gateway)
		}

		// Gateway must be a normal envoy, not itself a device.
		if gw.DeviceType != "" {
			return fmt.Errorf("envoy entry %q references gateway %q which is itself a network device", entry.Match, entry.Gateway)
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
	Config     *EnvoyConfig // resolved config (modules, vars, etc.)
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
			Hostname:   cfg.Envoy,
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
				Hostname:   e.config.Envoy,
				Gateway:    e.config.Gateway,
				DeviceType: e.config.DeviceType,
			})
		}
	}
	return entries
}

// EnvoysReferencingSecret returns the match patterns of all envoy entries
// whose configuration contains a "secret:<secretPath>" reference. Used by
// the rotation watcher to identify which envoys need force-push.
func (cs *ConfigStore) EnvoysReferencingSecret(secretPath string) []string {
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
	redacted := cloneEnvoyConfig(cfg)
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
	var idempErrs []IdempotencyModuleError
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

		// Separate conditional vars and match vars from regular vars.
		var condVars []conditionals.ConditionalVar
		matchVars := make(map[string][]conditionals.MatchBranch)
		matchVarDefaults := make(map[string]string)
		for name, v := range def.Vars {
			// Match vars: {match: [{when: "...", value: "..."}, ...]}
			if conditionals.IsMatchVar(v) {
				rawMap, _ := v.(map[string]any)
				branches, defaultVal, err := conditionals.ParseMatchVar(name, rawMap)
				if err != nil {
					return nil, fmt.Errorf("module %q: %w", def.Name, err)
				}
				matchVars[name] = branches
				if defaultVal != "" {
					matchVarDefaults[name] = defaultVal
				}
				delete(def.Vars, name)
				continue
			}
			// Conditional boolean vars: {trait: "...", in: [...]}
			if conditionals.IsConditionalVar(v) || conditionals.HasOperatorKey(v) {
				rawMap, _ := v.(map[string]any)
				cv, err := conditionals.ParseConditionalVar(name, rawMap)
				if err != nil {
					return nil, fmt.Errorf("module %q: %w", def.Name, err)
				}
				condVars = append(condVars, cv)
				delete(def.Vars, name)
			}
		}
		def.ConditionalVars = condVars
		def.MatchVars = matchVars
		def.MatchVarDefaults = matchVarDefaults

		// Warn about match vars without a default — envoys that don't match
		// any branch will get an empty var, likely causing resource failures.
		for name := range matchVars {
			if _, hasDefault := matchVarDefaults[name]; !hasDefault {
				slog.Warn("match var has no default — envoys not matching any branch will get an empty value",
					"module", def.Name, "var", name)
			}
		}

		// Idempotency validation — collect errors across all modules.
		findings := validate.ValidateModule(moduleToValidate(def))
		for _, f := range findings {
			if f.Severity == validate.SeverityWarning {
				slog.Warn("idempotency warning", "module", def.Name, "finding", f.String())
			}
		}
		if !dangerous && validate.HasErrors(findings) {
			idempErrs = append(idempErrs, IdempotencyModuleError{
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
			idempErrs = append(idempErrs, IdempotencyModuleError{
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

// loadRolesFile loads roles from a single roles.vgo file at the base dir.
// The file must contain a top-level "roles:" list.
func loadRolesFile(baseDir string) (map[string]*RoleDefinition, error) {
	for _, name := range []string{"roles.vgo"} {
		path := filepath.Join(baseDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var container struct {
			Roles []RoleDefinition `yaml:"roles"`
		}
		data = sanitizeYAML(data)
		if err := yaml.Unmarshal(data, &container); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, err))
		}

		defs := make(map[string]*RoleDefinition, len(container.Roles))
		for i := range container.Roles {
			def := &container.Roles[i]
			if def.Name == "" {
				return nil, fmt.Errorf("%s: role %d missing required 'name' field", path, i)
			}
			if _, exists := defs[def.Name]; exists {
				return nil, fmt.Errorf("%s: duplicate role name %q", path, def.Name)
			}
			defs[def.Name] = def
		}

		// Validate includes.
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

	// No roles.vgo found — return empty (roles are optional).
	return make(map[string]*RoleDefinition), nil
}

// applyInheritance prepends inherited modules, roles, and vars from
// common.vgo files in ancestor directories to a EnvoyEntry.
// Parent modules come first (foundation before specialization).
// Child vars override parent vars. exclude_modules strips specific inherited modules.
func applyInheritance(entry *EnvoyEntry, commons map[string]*CommonConfig, perEnvOverrides map[string]map[string]*PerEnvOverrideBlock) {
	// Snapshot the entry's own modules/roles/vars before merging.
	entry.OwnModules = append([]ModuleRef(nil), entry.Modules...)
	entry.OwnRoles = append([]RoleRef(nil), entry.Roles...)
	if len(entry.Vars) > 0 {
		entry.OwnVars = make(map[string]any, len(entry.Vars))
		for k, v := range entry.Vars {
			entry.OwnVars[k] = v
		}
	}

	if len(commons) == 0 {
		return
	}

	// Build the ancestor chain from root to the entry's directory.
	chain := inheritanceChain(entry.DirPath, commons)
	if len(chain) == 0 {
		return
	}

	// Collect inherited modules and roles (parent first).
	var inheritedModules []ModuleRef
	var inheritedRoles []RoleRef
	inheritedVars := make(map[string]any)

	for _, cc := range chain {
		inheritedModules = append(inheritedModules, cc.Modules...)
		inheritedRoles = append(inheritedRoles, cc.Roles...)
		for k, v := range cc.Vars {
			inheritedVars[k] = v
		}
	}

	// Apply exclude_modules — remove named modules from the inherited set.
	if len(entry.ExcludeModules) > 0 {
		exclude := make(map[string]bool, len(entry.ExcludeModules))
		for _, name := range entry.ExcludeModules {
			exclude[name] = true
		}
		filtered := inheritedModules[:0]
		for _, ref := range inheritedModules {
			if !exclude[ref.Name] {
				filtered = append(filtered, ref)
			}
		}
		inheritedModules = filtered
	}

	// Prepend inherited modules before the entry's own.
	entry.Modules = append(inheritedModules, entry.Modules...)

	// Prepend inherited roles before the entry's own.
	entry.Roles = append(inheritedRoles, entry.Roles...)

	// Merge vars: inherited first, entry's own override.
	if len(inheritedVars) > 0 {
		merged := inheritedVars
		for k, v := range entry.Vars {
			merged[k] = v
		}
		entry.Vars = merged
	}

	// Apply per_env_overrides.vgo blocks for the entry's environment.
	// Walk the same ancestry chain; child overrides override parent.
	if entry.Environment != "" && len(perEnvOverrides) > 0 {
		// Build the same directory chain (root → parent dirs).
		dirs := []string{""}
		if entry.DirPath != "" {
			parts := strings.Split(entry.DirPath, "/")
			for i := range parts {
				dirs = append(dirs, strings.Join(parts[:i+1], "/"))
			}
		}

		for _, dp := range dirs {
			envMap, ok := perEnvOverrides[dp]
			if !ok {
				continue
			}
			block, ok := envMap[entry.Environment]
			if !ok {
				continue
			}

			// Add modules from the env block.
			if len(block.Modules) > 0 {
				entry.Modules = append(entry.Modules, block.Modules...)
			}

			// Remove excluded modules.
			if len(block.ExcludeModules) > 0 {
				exclude := make(map[string]bool, len(block.ExcludeModules))
				for _, name := range block.ExcludeModules {
					exclude[name] = true
				}
				filtered := entry.Modules[:0]
				for _, ref := range entry.Modules {
					if !exclude[ref.Name] {
						filtered = append(filtered, ref)
					}
				}
				entry.Modules = filtered
			}

			// Merge vars — env overrides win over existing.
			if len(block.Vars) > 0 {
				if entry.Vars == nil {
					entry.Vars = make(map[string]any)
				}
				for k, v := range block.Vars {
					entry.Vars[k] = v
				}
			}
		}
	}
}

// inheritanceChain returns the ordered list of CommonConfigs from root
// down to (but not including) the entry's own directory level.
// For an entry at "production/web", it returns commons for:
// "" (root), "production" — in that order.
func inheritanceChain(dirPath string, commons map[string]*CommonConfig) []*CommonConfig {
	if dirPath == "" {
		// Root-level entries get no inheritance (common.vgo at root
		// applies only to subdirectories).
		return nil
	}

	var chain []*CommonConfig

	// Root common.
	if cc, ok := commons[""]; ok {
		chain = append(chain, cc)
	}

	// Walk path segments: "production/web" → ["production", "production/web"]
	parts := strings.Split(dirPath, string(filepath.Separator))
	for i := range parts {
		seg := filepath.Join(parts[:i+1]...)
		// The last segment is the entry's own directory — include it
		// because common.vgo there applies to entries in that dir.
		if cc, ok := commons[seg]; ok {
			chain = append(chain, cc)
		}
	}

	return chain
}

// loadWaiverConfigs discovers waivers.vgo files at each directory level
// and merges all entries into a flat standard → control_id → WaiverEntry map.
// Expired waivers (past their expires date) are excluded.
func loadWaiverConfigs(dir string) (map[string]map[string]*WaiverEntry, []WaiverFileEntry, error) {
	merged := make(map[string]map[string]*WaiverEntry)
	var perFile []WaiverFileEntry

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Base(path) != "waivers.vgo" {
			return nil
		}

		// Skip modules/, templates/, example-configs/ — same as loadEnvoysDir.
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "modules"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "templates"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "examples"+string(filepath.Separator)) {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		data = sanitizeYAML(data)

		var wc WaiverConfig
		if unmarshalErr := yaml.Unmarshal(data, &wc); unmarshalErr != nil {
			return fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, unmarshalErr))
		}

		fe := WaiverFileEntry{
			RelPath: filepath.ToSlash(rel),
			Waivers: make(map[string][]*WaiverEntry),
		}

		now := time.Now()
		for standard, entries := range wc.Waivers {
			if _, ok := merged[standard]; !ok {
				merged[standard] = make(map[string]*WaiverEntry)
			}
			for i := range entries {
				e := &entries[i]
				// Skip expired waivers.
				if e.Expires != "" {
					if exp, parseErr := time.Parse("2006-01-02", e.Expires); parseErr == nil && exp.Before(now) {
						continue
					}
				}
				merged[standard][e.Control] = e
				fe.Waivers[standard] = append(fe.Waivers[standard], e)
			}
		}

		if len(fe.Waivers) > 0 {
			perFile = append(perFile, fe)
		}

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	return merged, perFile, nil
}

// loadPerEnvOverrides loads per_env_overrides.vgo files from the config
// directory tree. Returns a map of dir path → env name → override block.
func loadPerEnvOverrides(dir string) (map[string]map[string]*PerEnvOverrideBlock, error) {
	result := make(map[string]map[string]*PerEnvOverrideBlock)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Base(path) != "per_env_overrides.vgo" {
			return nil
		}

		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "modules"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "templates"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "examples"+string(filepath.Separator)) {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		data = sanitizeYAML(data)

		var peo PerEnvOverridesConfig
		if unmarshalErr := yaml.Unmarshal(data, &peo); unmarshalErr != nil {
			return fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, unmarshalErr))
		}

		dirPath := filepath.ToSlash(filepath.Dir(rel))
		if dirPath == "." {
			dirPath = ""
		}

		if len(peo.Env) > 0 {
			result[dirPath] = peo.Env
			slog.Info("loaded per_env_overrides", "path", rel, "environments", len(peo.Env))
		}

		return nil
	})

	return result, err
}

// Waivers returns the merged compliance waivers for a given standard
// (compliance tag key, e.g., "cis-ubuntu", "pci-dss").
// Returns nil if no waivers are defined for that standard.
func (cs *ConfigStore) Waivers(standard string) map[string]*WaiverEntry {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.waivers[standard]
}

// AllWaivers returns all merged compliance waivers across all standards.
func (cs *ConfigStore) AllWaivers() map[string]map[string]*WaiverEntry {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.waivers
}

// loadEnvoysDir walks the config directory hierarchy and returns all
// leaf EnvoyEntry items (files with match: entries) plus a map of
// CommonConfig items from common.vgo files at each directory level.
//
// The directory structure defines inheritance:
//   - common.vgo at any level = modules/roles/vars inherited by all subdirs
//   - Any other .vgo file = leaf entry (must have match:)
//   - Root-level common.vgo applies only to subdirectories
//   - modules/, templates/ are skipped (not part of envoy hierarchy)
//   - roles.vgo at root is skipped (loaded separately by loadRolesFile)
func loadEnvoysDir(dir string) ([]EnvoyEntry, map[string]*CommonConfig, error) {
	envoyRoot := dir

	commons := make(map[string]*CommonConfig)
	var allEntries []EnvoyEntry

	err := filepath.WalkDir(envoyRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !isConfigFile(d.Name()) {
			return nil
		}
		// Skip modules/, templates/, example-configs/ and roles.vgo — they are not envoy entries.
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "modules"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "templates"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "examples"+string(filepath.Separator)) {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		data = sanitizeYAML(data)

		// Determine the relative dir path for this file within the hierarchy.
		relToRoot, _ := filepath.Rel(envoyRoot, path)
		dirPath := filepath.Dir(relToRoot)
		if dirPath == "." {
			dirPath = ""
		}

		// common.vgo files define inheritance — they should not have match: entries.
		baseName := filepath.Base(path)
		if baseName == "common.vgo" {
			var cc CommonConfig
			if unmarshalErr := yaml.Unmarshal(data, &cc); unmarshalErr != nil {
				return fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, unmarshalErr))
			}
			cc.DirPath = dirPath
			commons[dirPath] = &cc
			return nil
		}

		// roles.vgo at root level is handled separately — skip here.
		if dirPath == "" && baseName == "roles.vgo" {
			return nil
		}

		// Regular envoy entry file.
		var cfg EnvoysConfig
		if unmarshalErr := yaml.Unmarshal(data, &cfg); unmarshalErr != nil {
			return fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, unmarshalErr))
		}
		for i, entry := range cfg.Envoys {
			if entry.Match == "" {
				return fmt.Errorf("%s entry %d: missing required 'match' field", path, i)
			}
			cfg.Envoys[i].DirPath = dirPath
			cfg.Envoys[i].SourceFile = baseName
		}
		allEntries = append(allEntries, cfg.Envoys...)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// Reject duplicate match patterns — a hostname should only match one entry.
	seen := make(map[string]string, len(allEntries))
	for _, entry := range allEntries {
		if prev, exists := seen[entry.Match]; exists {
			return nil, nil, fmt.Errorf("duplicate envoy match pattern %q (also defined in %s)", entry.Match, prev)
		}
		seen[entry.Match] = entry.Match
	}

	return allEntries, commons, nil
}

// mergeEnvironmentOverrides merges base vars with environment-specific overrides.
func mergeEnvironmentOverrides(cfg *EnvoyConfig) {
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
// attribute on file resources. The source path is relative to the examples
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

// entryReferencesSecret checks if a envoy entry uses any affected module
// or has the secret in its environment_overrides (non-active environment overrides retain
// raw secret: prefixes since only the active environment is resolved).
func entryReferencesSecret(cfg *EnvoyConfig, affectedModules map[string]bool, needle string) bool {
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

// ComputeEnvDiff compares environment_overrides between two environments across all envoy
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

// cloneEnvoyConfig returns a shallow copy of the EnvoyConfig with a
// fresh Modules slice and Vars map, so modifications don't affect the original.
func cloneEnvoyConfig(cfg *EnvoyConfig) *EnvoyConfig {
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
		if d.IsDir() && d.Name() == "examples" {
			return filepath.SkipDir
		}
		if !d.IsDir() && isConfigFile(d.Name()) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// isConfigFile checks if a filename has a recognized config extension.
// Accepts .vgo files only. Ignores .example files.
// Legacy .yaml/.yml support has been removed — rename config files to .vgo.
func isConfigFile(name string) bool {
	if strings.HasSuffix(name, ".example") {
		return false
	}
	return filepath.Ext(name) == ".vgo"
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
			When:             r.When,
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

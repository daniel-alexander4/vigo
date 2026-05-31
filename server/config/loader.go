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

	"git.alexander4.io/vigo/server/compliance"
	"git.alexander4.io/vigo/server/conditionals"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/validate"
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

// Secret placeholder markers — substituted in for resolved secret values
// during loader resolution so the resolved config (and the bundle it
// produces) never carries plaintext secret bytes. The agent recognizes
// these markers post-template-render and fetches the real values via
// VigoAgent.ResolveSecrets. Markers are unambiguous in any text context
// (the literal byte sequence is implausibly rare in operator config).
const (
	secretPlaceholderBegin = "__VIGO_SECRET__"
	secretPlaceholderEnd   = "__END_VIGO_SECRET__"
)

// formatSecretPlaceholder produces the marker form the agent looks for.
// Wrapping the key with explicit begin/end sentinels makes mid-string
// substitution unambiguous (compound `PW=secret:foo,DB=app` → `PW=__VIGO_SECRET__foo__END_VIGO_SECRET__,DB=app`).
func formatSecretPlaceholder(key string) string {
	return secretPlaceholderBegin + key + secretPlaceholderEnd
}

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
	mu              sync.RWMutex
	entries         []resolvedEntry
	index           *patternIndex                  // accelerated hostname lookup
	rawModules      map[string]*ModuleDefinition   // unresolved, for secret ref scanning
	envoyEntries    []EnvoyEntry                   // original entries for tracing
	roleDefs        map[string]*RoleDefinition     // loaded roles for tracing
	moduleDefs      map[string]*ModuleDefinition   // loaded modules for tracing
	complianceIndex map[string]map[string][]string // standard → control_id → []module_name
	// coverageCache memoizes EnvoyStandardCoverage results at publish
	// time keyed by (hostcrate entry index, standard). Each entry's
	// module set is fixed by the publish-time hostcrate resolution, so
	// per-envoy coverage is purely a function of (entry, standard) and
	// can be precomputed once. Eliminates the per-call walk previously
	// done by EnvoyStandardCoverage. Built alongside complianceIndex.
	coverageCache           map[int]map[string]*StandardCoverage
	commonConfigs           map[string]*CommonConfig                   // dir path → common.vgo inheritance config
	perEnvOverrides         map[string]map[string]*PerEnvOverrideBlock // dir path → env name → override block
	waivers                 map[string]map[string]*WaiverEntry         // standard → control_id → waiver
	waiversByFile           []EnvoyWaiverFileEntry                     // per-file waiver data for trace display
	moduleComplianceSources []ModuleComplianceSource                   // per-file modules-tree compliance.vgo entries for trace display
	dir                     string                                     // examples directory
	provider                secrets.Provider
	backend                 string        // secrets backend name for diagnostic hints
	dangerous               bool          // allow non-idempotent resources
	version                 atomic.Uint64 // incremented on each successful reload
	secretsEpoch            atomic.Int64  // bumped on each successful reload AND on watcher-detected rotation; lets agents invalidate cached resolved secret values when the marker shifts
	// versionMintAt stamps the wall-clock time each config version became
	// current, retained for versionMintHorizon so the gRPC delta path can
	// measure publish→converge latency (vigo_publish_convergence_seconds)
	// when an envoy reports applying that version. Bounded: pruned on every
	// write, and a convergence onto a version older than the horizon is
	// catch-up to stale state, not a fresh publish wave, so it's not measured.
	versionMintMu    sync.Mutex
	versionMintAt    map[uint64]time.Time
	onReloadFailure  func(err error) // optional callback on reload failure
	onReloadSuccess  func()          // optional callback after successful reload
	lastReloadErr    error           // last reload error (nil if last reload succeeded)
	lastReloadAt     time.Time       // wall-clock time of the last loadAll attempt (success or failure)
	enabledStandards map[string]bool // nil = all enabled; non-nil = only these standard keys
	approvedSources  []string        // nil/empty = no annotation; non-empty = URL prefixes for package_source_audit trait
	serverYamlPath   string          // path to server.yaml for re-reading compliance on reload

	// fleet: lookup memoization. fleetLookupTTL is operator-set
	// (cs.SetFleetLookupTTL); 0 means use defaultFleetLookupTTL. The memo
	// caches the resolved {string | []any} for each (where, select, join,
	// include_stale) key for one TTL window so a publish doesn't cause N
	// fleet walks per check-in. Per-instance so two ConfigStores in one
	// process (test harness, multi-vigosrv lab) don't cross-pollute.
	fleetLookupTTL atomic.Int64 // nanoseconds
	fleetMemoMu    sync.Mutex
	fleetMemo      map[string]fleetMemoEntry

	// metrics is the per-instance prometheus collector set; nil-safe.
	// ConfigStores constructed without a *Metrics still load and reload —
	// the metric calls are guarded.
	metrics *metrics.Metrics
}

// SetMetrics attaches the per-instance prometheus collector set so
// config-reload duration/error counters update. Call once at server
// startup before the first reload; subsequent reloads pick up the
// pointer atomically because reloads serialize on cs.mu.
func (cs *ConfigStore) SetMetrics(m *metrics.Metrics) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.metrics = m
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

// SetApprovedSources configures the URL prefix allowlist used to annotate
// the `package_source_audit` trait. Pass nil or empty to disable the
// annotation (no `approved` field added to source rows).
func (cs *ConfigStore) SetApprovedSources(prefixes []string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(prefixes) == 0 {
		cs.approvedSources = nil
		return
	}
	cs.approvedSources = append([]string(nil), prefixes...)
}

// ApprovedSources returns a defensive copy of the configured allowlist.
// Returns nil when no allowlist is configured.
func (cs *ConfigStore) ApprovedSources() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if len(cs.approvedSources) == 0 {
		return nil
	}
	out := make([]string, len(cs.approvedSources))
	copy(out, cs.approvedSources)
	return out
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
		fleetMemo:       make(map[string]fleetMemoEntry),
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

// reloadComplianceStandards re-reads the compliance section from
// server.yaml and applies both the enabled-standards filter and the
// package-source allowlist. Name kept for the sake of existing call
// sites; the function now covers the whole section.
func (cs *ConfigStore) reloadComplianceStandards() {
	data, err := os.ReadFile(cs.serverYamlPath)
	if err != nil {
		slog.Warn("failed to re-read server.yaml for compliance section", "error", err)
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
	cs.SetApprovedSources(raw.Compliance.ApprovedSources)
}

// Version returns the current config version. Incremented on each successful
// reload, used by the response cache to detect config changes.
func (cs *ConfigStore) Version() uint64 {
	return cs.version.Load()
}

// versionMintHorizon bounds how long a config version's mint time is
// retained for publish→converge measurement. A convergence onto a version
// older than this is treated as catch-up to stale state, not a fresh
// publish wave, and is not measured. Comfortably exceeds the convergence
// histogram's top bucket (30m) so slow but legitimate waves are captured.
const versionMintHorizon = time.Hour

// recordVersionMint stamps the time a config version became current and
// prunes entries older than versionMintHorizon so the map stays bounded.
// Called once per successful reload.
func (cs *ConfigStore) recordVersionMint(v uint64, now time.Time) {
	cs.versionMintMu.Lock()
	defer cs.versionMintMu.Unlock()
	if cs.versionMintAt == nil {
		cs.versionMintAt = make(map[uint64]time.Time)
	}
	cs.versionMintAt[v] = now
	for ver, t := range cs.versionMintAt {
		if now.Sub(t) > versionMintHorizon {
			delete(cs.versionMintAt, ver)
		}
	}
}

// VersionMintedAt returns the time the given config version became current,
// if still within the retention horizon. The gRPC delta path uses this to
// measure publish→converge latency when an envoy reports it has applied that
// version. Returns false for versions older than the horizon or minted
// before this process started — both are catch-up, not a fresh publish wave.
func (cs *ConfigStore) VersionMintedAt(v uint64) (time.Time, bool) {
	cs.versionMintMu.Lock()
	defer cs.versionMintMu.Unlock()
	t, ok := cs.versionMintAt[v]
	return t, ok
}

// ResolveSecret fetches the plaintext value of the given secret key from
// the configured provider. Used by the ResolveSecrets RPC after the
// caller's entitlement is checked — never call this from a path that
// hasn't already verified the requester is allowed to see the value.
func (cs *ConfigStore) ResolveSecret(ctx context.Context, key string) (string, error) {
	return cs.provider.Get(ctx, key)
}

// SecretsEpoch returns the current secrets-epoch counter. Bumped on
// every successful Reload and on watcher-detected rotation. The
// CheckIn response embeds this so agents invalidate cached resolved-
// secret values when the value advances.
func (cs *ConfigStore) SecretsEpoch() int64 {
	return cs.secretsEpoch.Load()
}

// BumpSecretsEpoch advances the secrets-epoch by one. Called by the
// rotation watcher when isopass (or another backend's polling layer)
// reports a secret value has changed. Reload also bumps the epoch on
// its own — this is for rotation events that happen between reloads.
func (cs *ConfigStore) BumpSecretsEpoch() int64 {
	return cs.secretsEpoch.Add(1)
}

// LastReloadError returns the error from the most recent config reload,
// or nil if the last reload succeeded. Surfaced to agents as a check-in warning.
func (cs *ConfigStore) LastReloadError() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.lastReloadErr
}

// LastReloadAt returns the wall-clock time of the most recent loadAll
// attempt — success or failure. Zero value means loadAll has never run
// (the store was constructed but New errored out before calling it).
func (cs *ConfigStore) LastReloadAt() time.Time {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.lastReloadAt
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
		Envoy:   hostname,
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
	Hostname      string             `json:"hostname"`
	Matched       bool               `json:"matched"`
	MatchPattern  string             `json:"match_pattern,omitempty"`
	PatternType   string             `json:"pattern_type,omitempty"` // "exact" or "glob"
	Environment   string             `json:"environment,omitempty"`
	Roles         []string           `json:"roles,omitempty"`
	RoleExpansion []RoleTraceEntry   `json:"role_expansion,omitempty"`
	Inheritance   []InheritanceLevel `json:"inheritance,omitempty"`
	Modules       []string           `json:"modules"`
	ModuleSources map[string]string  `json:"module_sources,omitempty"` // module name → source file/role
	VarSources    []VarSourceEntry   `json:"var_sources"`
	FinalVars     map[string]any     `json:"final_vars"`
	ResourceCount int                `json:"resource_count"`

	// Password Inheritance
	SecretRefs      []SecretTraceEntry `json:"secret_refs,omitempty"`
	SecretsProvider string             `json:"secrets_provider,omitempty"`

	// Dependency Inheritance
	Dependencies []DependencyTraceEntry `json:"dependencies,omitempty"`

	// Compliance Inheritance
	ComplianceTags []ComplianceTraceEntry `json:"compliance_tags,omitempty"`

	// Modules-tree compliance.vgo provenance — directory-level claim files
	// that attached compliance tags to the modules this envoy runs.
	ModuleComplianceFiles []ModuleComplianceFileEntry `json:"module_compliance_files,omitempty"`

	// Compliance Waivers — mirrors the ModuleCompliance* fields above
	// (per-file paths + per-entry detail) but for the waivers half of
	// compliance.vgo. "Envoy" prefix signals the envoy's-view-of-the-
	// inheritance-chain semantic, parallel to "Module" on the claims
	// side.
	EnvoyWaiverFiles []string                `json:"envoy_waiver_files,omitempty"` // compliance.vgo paths in the envoy's inheritance chain
	EnvoyWaivers     []EnvoyWaiverTraceEntry `json:"envoy_waivers,omitempty"`

	// Per-Environment Overrides
	EnvOverrideFiles []string `json:"env_override_files,omitempty"` // environments.vgo paths in ancestry
	EnvOverrideEnv   string   `json:"env_override_env,omitempty"`   // which env block was applied

	// Conditional Inheritance
	Conditionals []ConditionalTraceEntry `json:"conditionals,omitempty"`
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

	// Filter modules-tree compliance.vgo files to those that cover
	// the filtered module.
	out.ModuleComplianceFiles = nil
	for _, mcf := range t.ModuleComplianceFiles {
		for _, covered := range mcf.CoveredModules {
			if covered == module {
				out.ModuleComplianceFiles = append(out.ModuleComplianceFiles, mcf)
				break
			}
		}
	}

	// Filter waivers to frameworks relevant to the filtered module.
	out.EnvoyWaivers = nil
	for _, w := range t.EnvoyWaivers {
		if filteredFrameworks[w.Framework] {
			out.EnvoyWaivers = append(out.EnvoyWaivers, w)
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
// Modules and Usercrates are listed separately so the operator can see which
// inclusion site activated each kind (parallel to the carrier's `modules:`
// and `usercrates:` YAML fields).
type InheritanceLevel struct {
	Dir        string         `json:"dir"`                  // relative dir path (e.g., "production/web")
	File       string         `json:"file"`                 // filename (e.g., "common.vgo")
	Modules    []string       `json:"modules,omitempty"`    // modules contributed at this level
	Usercrates []string       `json:"usercrates,omitempty"` // usercrates contributed at this level
	Roles      []string       `json:"roles,omitempty"`      // roles contributed at this level
	Vars       map[string]any `json:"vars,omitempty"`       // vars contributed at this level
	Overrides  []string       `json:"overrides,omitempty"`  // var keys that override a parent value
}

// RoleTraceEntry shows which modules and usercrates came from which role (and includes).
type RoleTraceEntry struct {
	Role       string   `json:"role"`
	Includes   []string `json:"includes,omitempty"`
	Modules    []string `json:"modules"`
	Usercrates []string `json:"usercrates,omitempty"`
}

// VarSourceEntry tracks where a var's value came from.
type VarSourceEntry struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source string `json:"source"` // "module:<name>", "inline"
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

// ComplianceTraceEntry tracks compliance framework tags on a resolved
// module. Frameworks carries the union of explicit `compliance:`
// entries, controls resolved through each `provides:` tag, and
// controls resolved through each `bundle:` tag. ControlSources, when
// populated, attributes each (framework, control) pair to its origin
// — "provides:<id>", "bundle:<id>", or "explicit" — so
// `vigocli config trace` can answer "which control came from where"
// without a second walk. ControlSources is sparse: present only when
// the module has a provides: or bundle: tag (otherwise every source
// would be "explicit" — emit nothing and keep the renderer's legacy
// compact form).
type ComplianceTraceEntry struct {
	Module         string                       `json:"module"`
	Provides       []string                     `json:"provides,omitempty"`        // the module's raw provides: list (kept for renderer headers)
	Bundle         []string                     `json:"bundle,omitempty"`          // the module's raw bundle: list
	Frameworks     map[string][]string          `json:"frameworks"`                // framework → control IDs (union of explicit + provides + bundle)
	ControlSources map[string]map[string]string `json:"control_sources,omitempty"` // framework → control_id → "provides:<id>" | "bundle:<id>" | "explicit"
}

// EnvoyWaiverFileEntry stores per-file waiver data for trace display.
// Named to mirror ModuleComplianceFileEntry — the "Envoy" prefix
// signals the envoy's-view-of-the-inheritance-chain semantic, parallel
// to "Module" meaning modules-tree on the claims side.
type EnvoyWaiverFileEntry struct {
	RelPath string                    // e.g., "compliance.vgo" or "production/compliance.vgo"
	Waivers map[string][]*WaiverEntry // standard → entries
}

// EnvoyWaiverTraceEntry tracks a compliance waiver applied to an envoy
// via a compliance.vgo file in the envoy's inheritance chain.
type EnvoyWaiverTraceEntry struct {
	SourceFile string `json:"source_file"`
	Framework  string `json:"framework"`
	Control    string `json:"control"`
	Reason     string `json:"reason"`
	ApprovedBy string `json:"approved_by"`
	Expires    string `json:"expires,omitempty"`
}

// ModuleComplianceFileEntry surfaces one modules-tree compliance.vgo file in
// the trace: the stockpile-relative path, the compliance claims it
// contributes, and the envoy's effective modules whose SourceFile falls
// under the file's directory. The trace only lists files that cover at
// least one of the envoy's modules.
type ModuleComplianceFileEntry struct {
	SourceFile     string              `json:"source_file"`
	Compliance     map[string][]string `json:"compliance"`
	CoveredModules []string            `json:"covered_modules,omitempty"`
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
			for _, ref := range cc.Usercrates {
				level.Usercrates = append(level.Usercrates, ref.Name)
				if _, exists := moduleSources[ref.Name]; !exists {
					moduleSources[ref.Name] = cs.usercrateSource(ref.Name)
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

	// (Usercrate-included modules are surfaced in trace.Inheritance via
	// the normal include path — see step 7's Kind plumbing.)

	// Collect compliance.vgo files along the inheritance path from per-file data.
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
				trace.EnvoyWaiverFiles = append(trace.EnvoyWaiverFiles, wf.RelPath)
			}
		}
	}

	// Collect environments.vgo files along the inheritance path.
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
					path := "environments.vgo"
					if dp != "" {
						path = dp + "/environments.vgo"
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
		for _, ref := range envoyEntry.OwnUsercrates {
			leafLevel.Usercrates = append(leafLevel.Usercrates, ref.Name)
			if _, exists := moduleSources[ref.Name]; !exists {
				moduleSources[ref.Name] = cs.usercrateSource(ref.Name)
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
				for _, ref := range inc.Usercrates {
					rte.Usercrates = append(rte.Usercrates, ref.Name)
					if _, exists := moduleSources[ref.Name]; !exists {
						moduleSources[ref.Name] = cs.usercrateSource(ref.Name)
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
		for _, ref := range role.Usercrates {
			rte.Usercrates = append(rte.Usercrates, ref.Name)
			if _, exists := moduleSources[ref.Name]; !exists {
				moduleSources[ref.Name] = cs.usercrateSource(ref.Name)
			}
		}
		trace.RoleExpansion = append(trace.RoleExpansion, rte)
	}

	// Collect module names from resolved config.
	cfg := entry.config
	for _, mod := range cfg.Modules {
		trace.Modules = append(trace.Modules, mod.Name)
		if _, exists := moduleSources[mod.Name]; exists {
			continue
		}
		if mod.BaseName != "" {
			// A foreach: instance — attribute it to the base module's source.
			if src, ok := moduleSources[mod.BaseName]; ok {
				moduleSources[mod.Name] = "foreach: " + mod.BaseName + " (" + src + ")"
			} else {
				moduleSources[mod.Name] = "foreach: " + mod.BaseName
			}
			continue
		}
		moduleSources[mod.Name] = "direct"
	}
	trace.ModuleSources = moduleSources

	// Modules-tree compliance.vgo provenance. A compliance.vgo at
	// directory D attaches claims to every module whose SourceFile lives
	// under D/. Include a source only when it covers at least one of
	// this envoy's effective modules, and record which modules it
	// covered so filter-by-module can narrow the view cleanly.
	for _, src := range cs.moduleComplianceSources {
		srcDir := filepath.ToSlash(filepath.Dir(src.RelPath))
		if srcDir == "." {
			srcDir = ""
		}
		var covered []string
		for _, modName := range trace.Modules {
			def, ok := cs.moduleDefs[modName]
			if !ok || def.SourceFile == "" {
				continue
			}
			modFile := filepath.ToSlash(def.SourceFile)
			if srcDir == "" || strings.HasPrefix(modFile, srcDir+"/") {
				covered = append(covered, modName)
			}
		}
		if len(covered) > 0 {
			trace.ModuleComplianceFiles = append(trace.ModuleComplianceFiles, ModuleComplianceFileEntry{
				SourceFile:     src.RelPath,
				Compliance:     src.Compliance,
				CoveredModules: covered,
			})
		}
	}

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

	// 2. Inline vars
	for k := range envoyEntry.Vars {
		varSource[k] = "inline"
	}

	// Build ordered var sources: trunk-to-leaf by source priority.
	// module defaults → inline.
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
	// Build the union of explicit `compliance:` entries and
	// controls implied by each `provides:` tag, tagging each
	// (framework, control) with its source so the trace renderer
	// can attribute every row. A control declared by both a
	// provision and an explicit entry resolves to "explicit" —
	// the explicit author intent wins for attribution.
	for _, mod := range cfg.Modules {
		def, ok := cs.moduleDefs[mod.Name]
		if !ok || (len(def.InheritedCompliance) == 0 && len(def.Compliance.Provides) == 0 && len(def.Compliance.Bundle) == 0) {
			continue
		}
		entry := ComplianceTraceEntry{Module: mod.Name}
		if len(def.Compliance.Provides) > 0 {
			entry.Provides = append([]string(nil), def.Compliance.Provides...)
		}
		if len(def.Compliance.Bundle) > 0 {
			entry.Bundle = append([]string(nil), def.Compliance.Bundle...)
		}
		fw := make(map[string][]string)
		src := make(map[string]map[string]string)
		seen := make(map[string]map[string]bool)
		addControl := func(standard, cid, source string) {
			if seen[standard] == nil {
				seen[standard] = make(map[string]bool)
			}
			if !seen[standard][cid] {
				seen[standard][cid] = true
				fw[standard] = append(fw[standard], cid)
				if src[standard] == nil {
					src[standard] = make(map[string]string)
				}
				src[standard][cid] = source
			}
			// Explicit overrides a provides:/bundle: tag for
			// attribution purposes — operators reading the trace
			// want the "operator wrote this" signal preserved.
			if source == "explicit" && src[standard] != nil {
				src[standard][cid] = "explicit"
			}
		}
		for _, prov := range def.Compliance.Provides {
			for standard, controlIDs := range compliance.ResolveProvision(prov) {
				for _, cid := range controlIDs {
					addControl(standard, cid, "provides:"+prov)
				}
			}
		}
		for _, b := range def.Compliance.Bundle {
			for standard, controlIDs := range compliance.ResolveBundle(b) {
				for _, cid := range controlIDs {
					addControl(standard, cid, "bundle:"+b)
				}
			}
		}
		for standard, controlIDs := range def.InheritedCompliance {
			for _, cid := range controlIDs {
				addControl(standard, cid, "explicit")
			}
		}
		entry.Frameworks = fw
		if len(def.Compliance.Provides) > 0 || len(def.Compliance.Bundle) > 0 {
			entry.ControlSources = src
		}
		trace.ComplianceTags = append(trace.ComplianceTags, entry)
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
					trace.EnvoyWaivers = append(trace.EnvoyWaivers, EnvoyWaiverTraceEntry{
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
		sort.Slice(trace.EnvoyWaivers, func(i, j int) bool {
			di := strings.Count(trace.EnvoyWaivers[i].SourceFile, "/")
			dj := strings.Count(trace.EnvoyWaivers[j].SourceFile, "/")
			if di != dj {
				return di < dj
			}
			return trace.EnvoyWaivers[i].SourceFile < trace.EnvoyWaivers[j].SourceFile
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
// Lower values come first: module defaults (trunk) → inline (leaf).
func varSourcePriority(source string) int {
	switch {
	case strings.HasPrefix(source, "module:"):
		return 0
	case source == "inline":
		return 1
	default:
		return 2
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
	"ccss":            "ccss",
	"fda21cfr11":      "fda-21cfr11",
	"gaming":          "gaming",
	"ciswindows":      "cis-windows",
	"cisubuntu":       "cis-ubuntu",
	"cisrhel":         "cis-rhel",
	"abacyber":        "aba-cyber",
	"nydfscyber":      "ny-dfs-cyber",
}

// SlugForStandardKey is the inverse of StandardKeyForSlug.
var SlugForStandardKey = func() map[string]string {
	m := make(map[string]string, len(StandardKeyForSlug))
	for slug, key := range StandardKeyForSlug {
		m[key] = slug
	}
	return m
}()

// buildComplianceIndex creates a reverse index from module compliance
// tags. Each module's controls are the UNION of three sources: the
// explicit `compliance:` block, the controls implied by each
// `provides:` tag (resolved through compliance.ModuleProvisions), and
// the controls implied by each `bundle:` tag (resolved through
// compliance.ModuleBundles). The per-control []module_names slice is
// deduped — without that, a module declaring the same
// (framework, control) via two paths would appear twice in coverage
// drilldowns.
func buildComplianceIndex(moduleDefs map[string]*ModuleDefinition) map[string]map[string][]string {
	idx := make(map[string]map[string][]string)
	// Track which (standard, control, module) tuples we've already
	// recorded so the per-control owner list stays a set even when a
	// module declares the same coverage via two paths.
	seen := make(map[string]map[string]map[string]bool)
	add := func(standard, cid, modName string) {
		if seen[standard] == nil {
			seen[standard] = make(map[string]map[string]bool)
		}
		if seen[standard][cid] == nil {
			seen[standard][cid] = make(map[string]bool)
		}
		if seen[standard][cid][modName] {
			return
		}
		seen[standard][cid][modName] = true
		if idx[standard] == nil {
			idx[standard] = make(map[string][]string)
		}
		idx[standard][cid] = append(idx[standard][cid], modName)
	}
	for _, mod := range moduleDefs {
		for _, prov := range mod.Compliance.Provides {
			for standard, controlIDs := range compliance.ResolveProvision(prov) {
				for _, cid := range controlIDs {
					add(standard, cid, mod.Name)
				}
			}
		}
		for _, b := range mod.Compliance.Bundle {
			for standard, controlIDs := range compliance.ResolveBundle(b) {
				for _, cid := range controlIDs {
					add(standard, cid, mod.Name)
				}
			}
		}
		for standard, controlIDs := range mod.InheritedCompliance {
			for _, cid := range controlIDs {
				add(standard, cid, mod.Name)
			}
		}
	}
	return idx
}

// EnvoyStandardCoverage resolves which controls of a given compliance
// standard an envoy covers through its assigned modules. The standard
// parameter is a compliance tag key (e.g. "pci-dss"), not a URL slug.
//
// The result is precomputed at publish time keyed by (hostcrate entry,
// standard); this method is now an O(1) cache lookup. An envoy whose
// hostname matches no entry returns nil — same as before. A standard
// with no entries in complianceIndex also returns nil.
func (cs *ConfigStore) EnvoyStandardCoverage(hostname, standard string) *StandardCoverage {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	controlIndex, ok := cs.complianceIndex[standard]
	if !ok {
		return nil
	}
	i := cs.index.Lookup(hostname)
	if i < 0 {
		// No hostcrate entry — return an empty-modules coverage so the
		// caller sees every control as missing rather than nil. Matches
		// pre-cache behavior used by surfaces that need to render
		// drilldowns for envoys that haven't been mapped yet.
		return computeStandardCoverage(nil, standard, controlIndex)
	}
	if perStandard, ok := cs.coverageCache[i]; ok {
		return perStandard[standard]
	}
	return nil
}

// EnvoyFrameworks returns the sorted list of standard keys this envoy
// participates in — every standard whose complianceIndex has at least
// one control covered by the envoy's resolved modules. Used by the
// envoys-page filter chip (`?compliance=hipaa,soc2`) and by surfaces
// that need to display the per-envoy compliance-framework chip set.
//
// O(N×M) where N=standards (~10) and M=envoy's covered controls per
// standard. Bounded; no cache layer in v1 — add one alongside the
// existing coverageCache if profiling shows it hot.
func (cs *ConfigStore) EnvoyFrameworks(hostname string) []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	i := cs.index.Lookup(hostname)
	if i < 0 {
		return nil
	}
	perStandard, ok := cs.coverageCache[i]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(perStandard))
	for standard, sc := range perStandard {
		if sc == nil {
			continue
		}
		// A non-nil StandardCoverage with at least one covered control
		// means the envoy participates in this framework.
		if len(sc.CoveredControls) > 0 {
			out = append(out, standard)
		}
	}
	sort.Strings(out)
	return out
}

// computeStandardCoverage walks (entryModules × controlIndex) once to
// produce a single StandardCoverage. Pure function — used by
// buildCoverageCache at publish time. Kept exported-ish (lowercase but
// in the same package) so tests can re-derive coverage independently
// and assert that the cache matches.
func computeStandardCoverage(entryModules map[string]bool, standard string, controlIndex map[string][]string) *StandardCoverage {
	if len(controlIndex) == 0 {
		return nil
	}
	sc := &StandardCoverage{Standard: standard}

	controlIDs := make([]string, 0, len(controlIndex))
	for cid := range controlIndex {
		controlIDs = append(controlIDs, cid)
	}
	sort.Strings(controlIDs)

	for _, cid := range controlIDs {
		var assigned []string
		for _, mod := range controlIndex[cid] {
			if entryModules[mod] {
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

// buildCoverageCache precomputes per-entry coverage for every standard
// in complianceIndex. Called at publish time alongside
// buildComplianceIndex; the result is keyed by entry index so per-call
// EnvoyStandardCoverage lookups are O(1).
func buildCoverageCache(entries []resolvedEntry, complianceIndex map[string]map[string][]string) map[int]map[string]*StandardCoverage {
	if len(entries) == 0 || len(complianceIndex) == 0 {
		return nil
	}
	out := make(map[int]map[string]*StandardCoverage, len(entries))
	for i := range entries {
		cfg := entries[i].config
		if cfg == nil {
			continue
		}
		entryModules := make(map[string]bool, len(cfg.Modules))
		for _, mod := range cfg.Modules {
			entryModules[mod.Name] = true
		}
		perStandard := make(map[string]*StandardCoverage, len(complianceIndex))
		for standard, controlIndex := range complianceIndex {
			perStandard[standard] = computeStandardCoverage(entryModules, standard, controlIndex)
		}
		out[i] = perStandard
	}
	return out
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

// InverseTraceResult is the response shape for inverse-direction config trace
// queries — "where is module/usercrate X applied?" — the dual of the
// hostname-rooted Trace. Envoys lists enrolled hosts whose resolved config
// includes the subject; TreeRefs (only populated when IncludeTree is true)
// lists config-tree patterns that reference the subject but match no
// currently-enrolled envoy.
type InverseTraceResult struct {
	Subject     string              `json:"subject"` // "module" or "usercrate"
	Value       string              `json:"value"`   // the subject name being looked up
	IncludeTree bool                `json:"include_tree"`
	Envoys      []InverseEnvoyEntry `json:"envoys"`              // enrolled envoys whose resolved config includes the subject
	TreeRefs    []InverseTreeRef    `json:"tree_refs,omitempty"` // populated only when IncludeTree
}

// InverseEnvoyEntry is one enrolled envoy whose resolved config includes the
// subject. Carrier mirrors the existing ModuleSources attribution shape
// produced by forward-direction Trace.
type InverseEnvoyEntry struct {
	EnvoyID     string `json:"envoy_id"`
	Hostname    string `json:"hostname"`
	Match       string `json:"match"`
	Environment string `json:"environment,omitempty"`
	Carrier     string `json:"carrier"`
}

// InverseTreeRef is one envoyEntry pattern in the config tree that references
// the subject but matches no enrolled envoy. Surfaces via --include-tree so
// operators can spot wiring that hasn't landed on a real host yet.
type InverseTreeRef struct {
	Match       string `json:"match"`
	Environment string `json:"environment,omitempty"`
	Carrier     string `json:"carrier"`
}

// InverseEnvoyRef is the minimal envoy projection TraceInverse needs from the
// FleetIndex — kept here so server/config does not import server/fleet and
// create a cycle. The api/configtrace handler does the small projection.
type InverseEnvoyRef struct {
	ID       string
	Hostname string
}

// TraceInverse answers "which enrolled envoys resolve to module/usercrate X?"
// by walking each envoy's Trace and checking for the subject — the
// ConfigStore stays the single source of truth for resolution. When
// includeTree is true, also lists envoyEntries patterns that reference the
// subject but match no enrolled envoy.
//
// subject must be "module" or "usercrate"; any other value returns an empty
// result.
func (cs *ConfigStore) TraceInverse(subject, value string, includeTree bool, envoys []InverseEnvoyRef) *InverseTraceResult {
	result := &InverseTraceResult{
		Subject:     subject,
		Value:       value,
		IncludeTree: includeTree,
		Envoys:      []InverseEnvoyEntry{},
	}
	if value == "" || (subject != "module" && subject != "usercrate") {
		return result
	}

	matchedPatterns := make(map[string]bool)

	for _, e := range envoys {
		trace := cs.Trace(e.Hostname)
		if !trace.Matched {
			continue
		}
		carrier, ok := traceCarrierFor(trace, subject, value)
		if !ok {
			continue
		}
		result.Envoys = append(result.Envoys, InverseEnvoyEntry{
			EnvoyID:     e.ID,
			Hostname:    e.Hostname,
			Match:       trace.MatchPattern,
			Environment: trace.Environment,
			Carrier:     carrier,
		})
		matchedPatterns[trace.MatchPattern] = true
	}

	if includeTree {
		cs.mu.RLock()
		for i := range cs.envoyEntries {
			ee := &cs.envoyEntries[i]
			if matchedPatterns[ee.Match] {
				continue
			}
			carrier := cs.inverseTreeCarrier(ee, subject, value)
			if carrier == "" {
				continue
			}
			result.TreeRefs = append(result.TreeRefs, InverseTreeRef{
				Match:       ee.Match,
				Environment: ee.Environment,
				Carrier:     carrier,
			})
		}
		cs.mu.RUnlock()
	}

	sort.Slice(result.Envoys, func(i, j int) bool {
		return result.Envoys[i].Hostname < result.Envoys[j].Hostname
	})
	sort.Slice(result.TreeRefs, func(i, j int) bool {
		return result.TreeRefs[i].Match < result.TreeRefs[j].Match
	})

	return result
}

// traceCarrierFor returns the carrier (file path / role / foreach attribution)
// for the given subject within a Trace result, and whether the trace
// contains it at all. Modules consult trace.ModuleSources directly;
// usercrates walk inheritance and role expansion since the forward trace
// tags those at the InheritanceLevel shape rather than the resolved-module
// name shape.
func traceCarrierFor(trace *ConfigTrace, subject, value string) (string, bool) {
	switch subject {
	case "module":
		for _, m := range trace.Modules {
			if m == value {
				if src, ok := trace.ModuleSources[value]; ok {
					return src, true
				}
				return "direct", true
			}
		}
	case "usercrate":
		for _, lvl := range trace.Inheritance {
			for _, u := range lvl.Usercrates {
				if u == value {
					src := lvl.File
					if lvl.Dir != "" {
						src = lvl.Dir + "/" + lvl.File
					}
					return src, true
				}
			}
		}
		for _, rte := range trace.RoleExpansion {
			for _, u := range rte.Usercrates {
				if u == value {
					return "role:" + rte.Role, true
				}
			}
		}
	}
	return "", false
}

// inverseTreeCarrier returns the carrier for a subject within a raw
// EnvoyEntry — used for the --include-tree path where the pattern matches no
// enrolled envoy, so cs.Trace cannot be re-used. Mirrors the carrier
// attribution priority in Trace: leaf entry, then inherited common.vgo
// (parent→child), then roles (own + inherited), then per-environment
// overrides. Caller must hold cs.mu (read).
func (cs *ConfigStore) inverseTreeCarrier(entry *EnvoyEntry, subject, value string) string {
	leafFile := entry.SourceFile
	if leafFile == "" {
		leafFile = "envoys.vgo"
	}
	leafSrc := leafFile
	if entry.DirPath != "" {
		leafSrc = entry.DirPath + "/" + leafFile
	}

	switch subject {
	case "module":
		for _, ref := range entry.OwnModules {
			if ref.Name == value {
				return leafSrc
			}
		}
	case "usercrate":
		for _, ref := range entry.OwnUsercrates {
			if ref.Name == value {
				return leafSrc
			}
		}
	}

	if entry.DirPath != "" && len(cs.commonConfigs) > 0 {
		chain := inheritanceChain(entry.DirPath, cs.commonConfigs)
		for _, cc := range chain {
			refs := cc.Modules
			if subject == "usercrate" {
				refs = cc.Usercrates
			}
			for _, ref := range refs {
				if ref.Name == value {
					src := "common.vgo"
					if cc.DirPath != "" {
						src = cc.DirPath + "/common.vgo"
					}
					return src
				}
			}
		}
	}

	allRoles := append([]RoleRef{}, entry.OwnRoles...)
	if entry.DirPath != "" && len(cs.commonConfigs) > 0 {
		chain := inheritanceChain(entry.DirPath, cs.commonConfigs)
		for _, cc := range chain {
			allRoles = append(allRoles, cc.Roles...)
		}
	}
	for _, roleRef := range allRoles {
		role, ok := cs.roleDefs[roleRef.Name]
		if !ok {
			continue
		}
		refs := role.Modules
		if subject == "usercrate" {
			refs = role.Usercrates
		}
		for _, ref := range refs {
			if ref.Name == value {
				return "role:" + roleRef.Name
			}
		}
		for _, incName := range role.Includes {
			inc, ok := cs.roleDefs[incName]
			if !ok {
				continue
			}
			incRefs := inc.Modules
			if subject == "usercrate" {
				incRefs = inc.Usercrates
			}
			for _, ref := range incRefs {
				if ref.Name == value {
					return "role:" + roleRef.Name
				}
			}
		}
	}

	if entry.Environment != "" && len(cs.perEnvOverrides) > 0 {
		pathDirs := []string{""}
		if entry.DirPath != "" {
			parts := strings.Split(entry.DirPath, "/")
			for i := range parts {
				pathDirs = append(pathDirs, strings.Join(parts[:i+1], "/"))
			}
		}
		for _, dp := range pathDirs {
			envMap, ok := cs.perEnvOverrides[dp]
			if !ok {
				continue
			}
			block, ok := envMap[entry.Environment]
			if !ok {
				continue
			}
			refs := block.Modules
			if subject == "usercrate" {
				refs = block.Usercrates
			}
			for _, ref := range refs {
				if ref.Name == value {
					src := "environments.vgo"
					if dp != "" {
						src = dp + "/environments.vgo"
					}
					return src + " (env: " + entry.Environment + ")"
				}
			}
		}
	}

	return ""
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

// contentDir returns the directory the loader treats as the content root.
// Holds every config primitive directly at the top level: modules/,
// usercrates/, templates/, tasks/, workflows/, plus the assignment-tree
// files (roles.vgo, envoys.vgo hostcrates, common.vgo, waivers.vgo,
// environments.vgo) and directory-inherited compliance.vgo claim files.
// For the editable side cs.dir points at /srv/vigo/stacks/stockpile/;
// for the published side it points at /srv/vigo/stacks/.live/. The
// loader is indifferent — both layouts are flat.
func (cs *ConfigStore) contentDir() string { return cs.dir }

// modulesDir returns the path to the modules directory (named "modules" on
// disk; YAML keys and Go identifiers retain "module" for industry-standard
// recognition).
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
	cs.lastReloadAt = time.Now()
	cs.mu.Unlock()

	if cs.metrics != nil {
		if err != nil {
			cs.metrics.ConfigReloadErrors.Inc()
		} else {
			cs.metrics.ConfigReloadDuration.Observe(elapsed.Seconds())
		}
	}
	if err != nil {
		return err
	}
	return nil
}

func (cs *ConfigStore) loadAllInner() error {
	// Enforce filename-per-tree rule even if someone hand-edits the
	// published .live/ directory. Publish also runs this check, but
	// the loader re-checks defensively: a violation here means we refuse
	// the reload and keep the previous good config live.
	if err := validateStockpileLayout(cs.contentDir()); err != nil {
		return fmt.Errorf("config layout: %w", err)
	}

	moduleDefs, err := loadModuleDefs(cs.modulesDir(), cs.dangerous)
	if err != nil {
		return fmt.Errorf("loading modules: %w", err)
	}

	if err := resolveSourceFiles(moduleDefs, cs.contentDir()); err != nil {
		return fmt.Errorf("resolving source files: %w", err)
	}

	// Discover usercrates — structurally identical to modules but living
	// under stockpile/**/usercrates/ directories. Merged into moduleDefs so
	// resolveEntry looks them up the same way. Name collisions with
	// regular modules are fatal: usercrates share the same name namespace
	// because they resolve through the same module-ref path.
	usercrateDefs, err := loadUsercrateDefs(cs.contentDir())
	if err != nil {
		return fmt.Errorf("loading usercrates: %w", err)
	}
	for name, def := range usercrateDefs {
		if existing, clash := moduleDefs[name]; clash {
			return fmt.Errorf("name collision between module and usercrate %q: module at %s, usercrate at %s — module and usercrate names share one namespace; rename one",
				name, existing.SourceFile, def.SourceFile)
		}
		moduleDefs[name] = def
	}

	// Walk the stockpile subtree for compliance.vgo files and union their
	// claims into each module's effective Compliance block. One walk covers
	// the whole tree — modules/, usercrates/, and the rest — so usercrate
	// defs receive claim inheritance through the same byDir the module defs
	// use: their SourceFile dir (e.g. "usercrates" or "customerA/usercrates")
	// is just another key.
	moduleComplianceByDir, moduleComplianceSources, err := loadComplianceConfigs(cs.contentDir())
	if err != nil {
		return fmt.Errorf("loading compliance configs: %w", err)
	}
	applyModuleComplianceInheritance(moduleDefs, moduleComplianceByDir)
	applyModuleComplianceInheritance(usercrateDefs, moduleComplianceByDir)

	// Load roles from stockpile/roles.vgo (single fleet-wide file).
	roleDefs, err := loadRolesFile(cs.contentDir())
	if err != nil {
		return fmt.Errorf("loading roles: %w", err)
	}

	envoyEntries, commonConfigs, err := loadEnvoysDir(cs.contentDir())
	if err != nil {
		return fmt.Errorf("loading envoys: %w", err)
	}

	// Waivers live in stockpile/**/waivers.vgo — distinct filename from
	// stockpile's compliance.vgo claims (mixed-keys check enforces this).
	waiverMap, waiverFiles, err := loadStockpileWaivers(cs.contentDir())
	if err != nil {
		return fmt.Errorf("loading waivers: %w", err)
	}

	perEnvOverrides, err := loadPerEnvOverrides(cs.contentDir())
	if err != nil {
		return fmt.Errorf("loading environments.vgo: %w", err)
	}

	// Validate role references.
	for _, role := range roleDefs {
		for _, ref := range role.Modules {
			if _, ok := moduleDefs[ref.Name]; !ok {
				return fmt.Errorf("role %q references module %q which does not exist%s",
					role.Name, ref.Name,
					notFoundHint(ref.Name, moduleCandidates(moduleDefs), "add a module definition to modules/"))
			}
		}
	}

	// Validate network device entries: gateway references, device types, credentials.
	if err := validateDeviceEntries(envoyEntries); err != nil {
		return err
	}

	// Apply directory inheritance: prepend inherited modules/roles/vars
	// from common.vgo files in ancestor directories. Usercrates auto-apply
	// from any usercrates/ subdir in the entry's ancestor chain.
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
	cs.coverageCache = buildCoverageCache(entries, cs.complianceIndex)
	cs.commonConfigs = commonConfigs
	cs.perEnvOverrides = perEnvOverrides
	cs.waivers = waiverMap
	cs.waiversByFile = waiverFiles
	cs.moduleComplianceSources = moduleComplianceSources
	cs.mu.Unlock()

	// Count total waivers across all standards.
	totalWaivers := 0
	for _, controls := range waiverMap {
		totalWaivers += len(controls)
	}

	// Warn on usercrates that exist as defs but are never included by any
	// envoy's effective config. Operators can act on the hint by either
	// adding the usercrate to a carrier's `usercrates:` field or moving
	// the def out of usercrates/ (it became dead drop-in code on the
	// 0.54.0 inert-until-included flip).
	if unref := unreferencedUsercrates(moduleDefs, entries); len(unref) > 0 {
		slog.Warn("unreferenced usercrates — defined under stockpile/**/usercrates/ but not included by any envoy",
			"count", len(unref),
			"names", unref)
	}

	v := cs.version.Add(1)
	cs.recordVersionMint(v, time.Now())
	// Bump the secrets epoch on every successful reload. A reload may
	// have introduced new secret references, retired old ones, or
	// resolved the same refs against rotated provider values — agents
	// invalidate any cached resolved-secret values when this advances.
	se := cs.secretsEpoch.Add(1)

	slog.Info("config loaded",
		"envoys", len(entries),
		"modules", len(moduleDefs),
		"roles", len(roleDefs),
		"waivers", totalWaivers,
		"version", v,
		"secrets_epoch", se)

	return nil
}

// unreferencedUsercrates returns the sorted names of Usercrate-kind defs
// in moduleDefs whose names do not appear in any envoy's resolved module
// list. Surfaced as a publish-time warning so operators notice dead
// drop-ins (a usercrate file the loader picked up but no envoy actually
// includes).
func unreferencedUsercrates(moduleDefs map[string]*ModuleDefinition, entries []resolvedEntry) []string {
	referenced := make(map[string]bool)
	for _, e := range entries {
		if e.config == nil {
			continue
		}
		for _, m := range e.config.Modules {
			referenced[m.Name] = true
			if m.BaseName != "" {
				referenced[m.BaseName] = true
			}
		}
	}
	var unref []string
	for name, def := range moduleDefs {
		if def.Kind != ModuleKindUsercrate {
			continue
		}
		if !referenced[name] {
			unref = append(unref, name)
		}
	}
	sort.Strings(unref)
	return unref
}

// usercrateSource returns the def's SourceFile (stockpile-relative
// usercrates/<file>.vgo path) for a usercrate name, falling back to a
// synthesized "usercrates/<name>.vgo" if the def isn't registered. Used
// by the trace builder to attribute ModuleSources to the def file rather
// than the inclusion site, so operators can jump straight to the source.
func (cs *ConfigStore) usercrateSource(name string) string {
	if def, ok := cs.moduleDefs[name]; ok && def.SourceFile != "" {
		return def.SourceFile
	}
	return "usercrates/" + name + ".vgo"
}

// roleRefNames extracts the Name fields from a slice of RoleRefs.
func roleRefNames(refs []RoleRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names
}

// checkCrossKindRefs enforces that every ref in a `modules:` field points at
// a Module-kind def, and every ref in a `usercrates:` field at a Usercrate.
// Refs to unknown defs are skipped — the "module not found" check fires
// later. Accumulates all mismatches across the carriers (envoy own modules
// and usercrates after inheritance, plus each cited role's modules,
// usercrates, includes, and case branches) so an operator sees the full
// picture in one publish attempt.
func checkCrossKindRefs(entry EnvoyEntry, moduleDefs map[string]*ModuleDefinition, roleDefs map[string]*RoleDefinition) error {
	type mismatch struct {
		Source  string
		Name    string
		Want    ModuleKind
		Got     ModuleKind
		DefFile string
	}
	var mismatches []mismatch
	check := func(refs []ModuleRef, want ModuleKind, source string) {
		for _, r := range refs {
			def, ok := moduleDefs[r.Name]
			if !ok {
				continue
			}
			if def.Kind != want {
				mismatches = append(mismatches, mismatch{source, r.Name, want, def.Kind, def.SourceFile})
			}
		}
	}
	check(entry.Modules, ModuleKindModule, fmt.Sprintf("envoy %q modules:", entry.Match))
	check(entry.Usercrates, ModuleKindUsercrate, fmt.Sprintf("envoy %q usercrates:", entry.Match))
	for _, roleRef := range entry.Roles {
		role, ok := roleDefs[roleRef.Name]
		if !ok {
			continue
		}
		check(role.Modules, ModuleKindModule, fmt.Sprintf("role %q modules:", role.Name))
		check(role.Usercrates, ModuleKindUsercrate, fmt.Sprintf("role %q usercrates:", role.Name))
		for _, incName := range role.Includes {
			inc, ok := roleDefs[incName]
			if !ok {
				continue
			}
			check(inc.Modules, ModuleKindModule, fmt.Sprintf("role %q (included by %q) modules:", inc.Name, role.Name))
			check(inc.Usercrates, ModuleKindUsercrate, fmt.Sprintf("role %q (included by %q) usercrates:", inc.Name, role.Name))
		}
		for ci, c := range role.Case {
			check(c.Modules, ModuleKindModule, fmt.Sprintf("role %q case[%d] modules:", role.Name, ci))
		}
	}
	if len(mismatches) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("usercrate/module field mismatch — each reference must use the field that matches the def's kind:")
	for _, m := range mismatches {
		gotLabel := "module"
		fix := "use `usercrates:` instead of `modules:`"
		if m.Got == ModuleKindUsercrate {
			gotLabel = "usercrate"
			if m.Want == ModuleKindUsercrate {
				// can't happen — Want != Got but Got is usercrate, so Want is Module
				continue
			}
		} else {
			fix = "use `modules:` instead of `usercrates:`"
		}
		fmt.Fprintf(&b, "\n  %s references %q — that name is a %s defined at %s; %s",
			m.Source, m.Name, gotLabel, m.DefFile, fix)
	}
	return fmt.Errorf("%s", b.String())
}

// resolveEntry converts a EnvoyEntry into a fully resolved EnvoyConfig.
func (cs *ConfigStore) resolveEntry(entry EnvoyEntry, moduleDefs map[string]*ModuleDefinition, roleDefs map[string]*RoleDefinition) (*EnvoyConfig, error) {
	if err := checkCrossKindRefs(entry, moduleDefs, roleDefs); err != nil {
		return nil, err
	}

	// Collect module refs: roles first (with includes), then direct modules.
	// First ref wins for conditional fields (matching existing var merge behavior).
	var moduleRefs []ModuleRef
	seen := make(map[string]bool)
	for _, roleRef := range entry.Roles {
		role, ok := roleDefs[roleRef.Name]
		if !ok {
			return nil, fmt.Errorf("role %q does not exist%s",
				roleRef.Name,
				notFoundHint(roleRef.Name, roleCandidates(roleDefs), "add a role definition to roles/"))
		}
		// Expand single-level includes before the role's own modules.
		for _, incName := range role.Includes {
			inc, ok := roleDefs[incName]
			if !ok {
				return nil, fmt.Errorf("role %q includes %q which does not exist%s",
					roleRef.Name, incName,
					notFoundHint(incName, roleCandidates(roleDefs), ""))
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
			for _, ref := range inc.Usercrates {
				if !seen[ref.Name] {
					seen[ref.Name] = true
					mref := ref
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
		for _, ref := range role.Usercrates {
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
	for _, ref := range entry.Usercrates {
		if !seen[ref.Name] {
			seen[ref.Name] = true
			moduleRefs = append(moduleRefs, ref)
		}
	}

	// Expand module refs carrying foreach: into one synthetic ref per list item
	// ("<module>[<key>]"). Each item's fields become {{ .Each.<field> }} (maps)
	// or {{ .Item }} (scalars) inside the instance's resources.
	moduleRefs, err := expandForeachModuleRefs(moduleRefs, moduleDefs, entry.Vars)
	if err != nil {
		return nil, err
	}

	// Resolve module definitions into ModuleConfigs, carrying conditional fields.
	var modules []ModuleConfig
	for _, ref := range moduleRefs {
		defName := ref.Name
		if ref.BaseName != "" {
			defName = ref.BaseName
		}
		def, ok := moduleDefs[defName]
		if !ok {
			via := ""
			if ref.BaseName != "" {
				via = " (referenced via foreach:)"
			}
			return nil, fmt.Errorf("module %q%s does not exist%s",
				defName, via,
				notFoundHint(defName, moduleCandidates(moduleDefs), "add a module definition to modules/"))
		}
		// Shallow-clone the inherited-compliance map so later mutation
		// of one doesn't leak into the other. InheritedCompliance carries
		// any unioned ancestor compliance.vgo claims from
		// applyModuleComplianceInheritance; the operator-facing per-module
		// compliance attribution (Compliance.Provides + Compliance.Bundle)
		// is unioned separately by the trace builder.
		var compliance map[string][]string
		if len(def.InheritedCompliance) > 0 {
			compliance = make(map[string][]string, len(def.InheritedCompliance))
			for k, v := range def.InheritedCompliance {
				compliance[k] = append([]string(nil), v...)
			}
		}
		resources := cloneResources(def.Resources)
		modName := def.Name
		if ref.BaseName != "" {
			modName = ref.Name
			expanded, rerr := expandModuleInstanceResources(resources, ref.EachItem, ref.InstanceKey)
			if rerr != nil {
				return nil, fmt.Errorf("module %q instance [%s]: %w", ref.BaseName, ref.InstanceKey, rerr)
			}
			resources = expanded
		}
		modules = append(modules, ModuleConfig{
			Name:       modName,
			DependsOn:  append([]string(nil), def.DependsOn...),
			When:       ref.When,
			Resources:  resources,
			Compliance: compliance,
			BaseName:   ref.BaseName,
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
	// means B depends on A — so we add A to B's DependsOn list. before: targets
	// name a module by its definition name; if that module was foreach-expanded,
	// the relation fans out to every instance.
	modulesByBase := make(map[string][]int, len(modules))
	for i, mod := range modules {
		base := mod.Name
		if mod.BaseName != "" {
			base = mod.BaseName
		}
		modulesByBase[base] = append(modulesByBase[base], i)
	}
	for _, mod := range modules {
		defName := mod.Name
		if mod.BaseName != "" {
			defName = mod.BaseName
		}
		for _, target := range moduleDefs[defName].Before {
			idxs, ok := modulesByBase[target]
			if !ok {
				// Distinguish three cases for a more actionable error:
				//  1. Target module does not exist anywhere in modules/
				//  2. Target exists but is explicitly excluded via exclude_modules:
				//  3. Target exists but is simply not in this envoy's modules: list
				if _, defined := moduleDefs[target]; !defined {
					return nil, fmt.Errorf("module %q has before: [%s] but module %q does not exist%s",
						mod.Name, target, target,
						notFoundHint(target, moduleCandidates(moduleDefs), "add a module definition to modules/"))
				}
				for _, ex := range entry.ExcludeModules {
					if ex == target {
						return nil, fmt.Errorf("module %q has before: [%s] but module %q is listed in exclude_modules: for this envoy — remove the exclusion or drop the before: reference",
							mod.Name, target, target)
					}
				}
				return nil, fmt.Errorf("module %q has before: [%s] but module %q is not assigned to this envoy — add it to the envoy's modules: list (directly or via a role) or drop the before: reference",
					mod.Name, target, target)
			}
			for _, idx := range idxs {
				modules[idx].DependsOn = appendUnique(modules[idx].DependsOn, mod.Name)
			}
		}
	}

	// Extract module definition names for var collection below. A foreach
	// instance's var defaults come from the base module definition.
	moduleNames := make([]string, len(moduleRefs))
	for i, ref := range moduleRefs {
		if ref.BaseName != "" {
			moduleNames[i] = ref.BaseName
		} else {
			moduleNames[i] = ref.Name
		}
	}

	// Build vars: module defaults → envoy-level overrides.
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
	for k, v := range entry.Vars {
		if _, exists := vars[k]; !exists {
			varOrder = append(varOrder, k)
		}
		vars[k] = v
	}

	cfg := &EnvoyConfig{
		Envoy:        entry.Match,
		MatchPattern: entry.Match,
		Environment:  entry.Environment,
		Roles:        roleRefNames(entry.Roles),
		Modules:      modules,
		Vars:         vars,
		VarOrder:     varOrder,
		Tags:         append([]string(nil), entry.Tags...),
		ObserveOnly:  entry.ObserveOnly,
		Gateway:      entry.Gateway,
		DeviceType:   entry.DeviceType,
		Credential:   entry.Credential,
		Username:     entry.Username,
		Port:         entry.Port,
		IPAddress:    entry.IPAddress,
	}

	// Detect fleet: lookups in top-level vars. Parsing/validating here surfaces
	// a malformed construct at config publish; resolution happens per-envoy at
	// check-in (it needs the live fleet snapshot).
	for name, v := range cfg.Vars {
		inner, ok := asFleetLookupConstruct(v)
		if !ok {
			continue
		}
		fl, err := ParseFleetLookup(name, inner)
		if err != nil {
			return nil, err
		}
		cfg.Vars[name] = fl
		cfg.HasFleetLookups = true
	}

	// Expand foreach resources now that vars are fully merged. A foreach: over
	// an unresolved fleet: var is left for the per-envoy clone path (see
	// ExpandForEach), which re-runs this after ResolveFleetLookupsInConfig.
	for i := range cfg.Modules {
		expanded, err := ExpandForEach(cfg.Modules[i].Resources, cfg.Vars)
		if err != nil {
			return nil, fmt.Errorf("module %q: %w", cfg.Modules[i].Name, err)
		}
		cfg.Modules[i].Resources = expanded
	}

	// Detect tag/host/trait lookup tables in resource attributes. If any
	// are present, the check-in path must clone the config and resolve
	// lookups per-envoy instead of serving the shared cached bundle.
	for i := range cfg.Modules {
		for j := range cfg.Modules[i].Resources {
			for _, v := range cfg.Modules[i].Resources[j].Attributes {
				if IsLookupTable(v) {
					cfg.HasLookups = true
					break
				}
			}
			if cfg.HasLookups {
				break
			}
		}
		if cfg.HasLookups {
			break
		}
	}

	// Enforce known-secret fields and resolve secret: prefixes.
	// Collect all failures so the operator can fix everything in one pass.
	var secretErrs []*SecretError
	secretErrs = append(secretErrs, cs.resolveVars(entry.Match, cfg.Vars)...)
	for i := range cfg.Modules {
		secretErrs = append(secretErrs, cs.resolveModuleSecrets(entry.Match, &cfg.Modules[i])...)
	}

	// Validate the device credential secret if this is a network device
	// entry; on success store the placeholder marker (the resolved value
	// is fetched by the agent via ResolveSecrets when the gateway envoy
	// applies the proxied-device session).
	if cfg.Credential != "" && strings.HasPrefix(cfg.Credential, secretPrefix) {
		key := strings.TrimPrefix(cfg.Credential, secretPrefix)
		if _, err := cs.provider.Get(context.Background(), key); err != nil {
			secretErrs = append(secretErrs, &SecretError{
				Match:   entry.Match,
				Attr:    "credential",
				Secret:  key,
				Err:     err,
				Backend: cs.backend,
			})
		} else {
			cfg.Credential = formatSecretPlaceholder(key)
		}
	}

	if len(secretErrs) > 0 {
		if cs.metrics != nil {
			for range secretErrs {
				cs.metrics.SecretResolutionErrorsTotal.Inc()
			}
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
			gwCandidates := make([]string, 0, len(byMatch))
			for k := range byMatch {
				gwCandidates = append(gwCandidates, k)
			}
			sort.Strings(gwCandidates)
			return fmt.Errorf("envoy entry %q references gateway %q which does not exist%s",
				entry.Match, entry.Gateway,
				notFoundHint(entry.Gateway, gwCandidates, ""))
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
		// compliance.vgo files under modules/ are handled by
		// loadComplianceConfigs — skip them here so they aren't
		// parsed as module definitions.
		if filepath.Base(path) == "compliance.vgo" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		def, err := ParseModuleFile(data)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if def.Name == "" {
			// Scan the raw YAML for near-miss keys so the operator
			// doesn't have to guess which field they mistyped.
			var raw map[string]any
			if unmarshalErr := yaml.Unmarshal(data, &raw); unmarshalErr == nil {
				for _, candidate := range []string{"Name", "NAME", "names", "nme", "naem", "module_name", "module"} {
					if _, ok := raw[candidate]; ok {
						return nil, fmt.Errorf("%s: missing required 'name' field — found %q, did you mean 'name'?", path, candidate)
					}
				}
			}
			return nil, fmt.Errorf("%s: missing required 'name' field", path)
		}
		if existing, exists := defs[def.Name]; exists {
			newRel := path
			if rel, relErr := filepath.Rel(filepath.Dir(dir), path); relErr == nil {
				newRel = filepath.ToSlash(rel)
			}
			return nil, fmt.Errorf("duplicate module name %q: already defined in %s, redefined in %s — modules are indexed by the `name:` field (not the filename); rename one of them",
				def.Name, existing.SourceFile, newRel)
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

		// Record source file path relative to stockpile root for unused-module reporting.
		if rel, err := filepath.Rel(filepath.Dir(dir), path); err == nil {
			def.SourceFile = filepath.ToSlash(rel)
		}

		defs[def.Name] = def
	}
	if len(idempErrs) > 0 {
		return nil, &IdempotencyErrors{Modules: idempErrs}
	}
	return defs, nil
}

// loadUsercrateDefs walks the stockpile tree looking for usercrates/
// subdirectories at any depth. Every `.vgo` file inside a usercrates/
// dir is parsed as a module definition with Kind = ModuleKindUsercrate.
// Usercrates are structurally modules — same YAML shape — distinguished
// only by where they live and the resolver's positive-include rule
// (carrier-level `usercrates: [...]` is the only path to activate one).
//
// Returns defs: name → ModuleDefinition. SourceFile is the
// stockpile-relative path (e.g., "usercrates/dan.vgo" or
// "customerA/usercrates/alice.vgo").
func loadUsercrateDefs(stockpileDir string) (map[string]*ModuleDefinition, error) {
	defs := make(map[string]*ModuleDefinition)

	walkErr := filepath.WalkDir(stockpileDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip known-not-usercrates subtrees to save walks.
		if d.IsDir() {
			rel, _ := filepath.Rel(stockpileDir, path)
			switch rel {
			case "modules", "templates", "examples", ".live":
				return filepath.SkipDir
			}
			return nil
		}
		if !isConfigFile(d.Name()) {
			return nil
		}
		// compliance.vgo files inside a usercrates/ dir are directory-level
		// compliance claim declarations — handled by
		// loadComplianceConfigs, not module definitions.
		if d.Name() == "compliance.vgo" {
			return nil
		}
		// Only files inside a usercrates/ directory are considered
		// usercrate definitions. The separator checks keep substring
		// matches ("my-usercrates-notes/") from false-positiving.
		rel, relErr := filepath.Rel(stockpileDir, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		parts := strings.Split(relSlash, "/")
		usercratesIdx := -1
		for i, p := range parts {
			if p == "usercrates" {
				usercratesIdx = i
				break
			}
		}
		if usercratesIdx == -1 || usercratesIdx == len(parts)-1 {
			return nil
		}
		// Ignore nested usercrates/ under usercrates/ (not supported — a
		// usercrate file lives directly in a usercrates/ dir).
		if usercratesIdx != len(parts)-2 {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		def, err := ParseModuleFile(data)
		if err != nil {
			return fmt.Errorf("parsing usercrate %s: %w", path, err)
		}
		if def.Name == "" {
			// Default the name to the filename stem. Usercrates are
			// identified by their filesystem location (auto-apply-within-scope
			// keys off the containing dir), so the explicit `name:` is
			// boilerplate in the common case. `dan.vgo` → `dan`.
			base := d.Name()
			stem := strings.TrimSuffix(base, filepath.Ext(base))
			if stem == "" {
				return fmt.Errorf("%s: usercrate filename has no stem (cannot default 'name' field)", path)
			}
			def.Name = stem
		}
		if existing, exists := defs[def.Name]; exists {
			return fmt.Errorf("duplicate usercrate name %q: already defined in %s, redefined in %s — usercrates are indexed by the `name:` field; rename one of them",
				def.Name, existing.SourceFile, relSlash)
		}

		// Shape rule: a usercrate must declare exactly one `type: user`
		// resource. Extra non-user resources (file for authorized_keys
		// or dotfiles, exec for post-create setup, etc.) are permitted.
		userCount := 0
		for _, r := range def.Resources {
			if r.Type == "user" {
				userCount++
			}
		}
		if userCount != 1 {
			return fmt.Errorf("%s: usercrate must declare exactly one `type: user` resource — found %d. Move it to modules/ or restructure",
				relSlash, userCount)
		}

		def.SourceFile = relSlash
		def.Kind = ModuleKindUsercrate
		defs[def.Name] = def
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
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
					return nil, fmt.Errorf("role %q includes %q which does not exist%s",
						def.Name, incName,
						notFoundHint(incName, roleCandidates(defs), ""))
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
// common.vgo files in ancestor directories to an EnvoyEntry. Parent
// modules come first (foundation before specialization). Child vars
// override parent vars. exclude_modules strips specific inherited
// modules. Usercrates are NOT auto-applied — they require an explicit
// positive include via a carrier's `usercrates:` field (see step 3+).
func applyInheritance(entry *EnvoyEntry, commons map[string]*CommonConfig, perEnvOverrides map[string]map[string]*PerEnvOverrideBlock) {
	// Snapshot the entry's own modules/roles/usercrates/vars before merging.
	entry.OwnModules = append([]ModuleRef(nil), entry.Modules...)
	entry.OwnRoles = append([]RoleRef(nil), entry.Roles...)
	entry.OwnUsercrates = append([]ModuleRef(nil), entry.Usercrates...)
	if len(entry.Vars) > 0 {
		entry.OwnVars = make(map[string]any, len(entry.Vars))
		for k, v := range entry.Vars {
			entry.OwnVars[k] = v
		}
	}

	// Nothing to inherit and no environments.vgo overlays to apply — bail.
	// (perEnvOverrides is checked too so an environments.vgo block still
	// applies even when there are no common.vgo files in the chain.)
	if len(commons) == 0 && len(perEnvOverrides) == 0 {
		return
	}

	// Build the ancestor chain from root to the entry's directory.
	chain := inheritanceChain(entry.DirPath, commons)

	// Collect inherited modules, usercrates, and roles (parent first).
	var inheritedModules []ModuleRef
	var inheritedUsercrates []ModuleRef
	var inheritedRoles []RoleRef
	inheritedVars := make(map[string]any)

	for _, cc := range chain {
		inheritedModules = append(inheritedModules, cc.Modules...)
		inheritedUsercrates = append(inheritedUsercrates, cc.Usercrates...)
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

	// Prepend inherited usercrates before the entry's own.
	entry.Usercrates = append(inheritedUsercrates, entry.Usercrates...)

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

	// Apply environments.vgo blocks for the entry's environment.
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

			// Add modules and usercrates from the env block.
			if len(block.Modules) > 0 {
				entry.Modules = append(entry.Modules, block.Modules...)
			}
			if len(block.Usercrates) > 0 {
				entry.Usercrates = append(entry.Usercrates, block.Usercrates...)
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

// loadStockpileWaivers discovers waivers.vgo files at each
// stockpile-tree directory level and merges waiver entries into a flat
// standard → control_id → WaiverEntry map. Expired waivers are excluded.
//
// Canonical-filename rule: waivers live in stockpile/**/waivers.vgo
// ONLY. Compliance claims live in stockpile/**/compliance.vgo ONLY —
// picked up separately by loadComplianceConfigs. A compliance:
// block in a waivers.vgo is a fatal validator error at publish time
// (caught by validateStockpileLayout); at load time it's ignored with a
// slog.Warn.
func loadStockpileWaivers(dir string) (map[string]map[string]*WaiverEntry, []EnvoyWaiverFileEntry, error) {
	merged := make(map[string]map[string]*WaiverEntry)
	var perFile []EnvoyWaiverFileEntry

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return merged, perFile, nil
	}

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}

		rel, _ := filepath.Rel(dir, path)

		if filepath.Base(path) != "waivers.vgo" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		data = sanitizeYAML(data)

		if err := validateComplianceTopLevelKeys(data, filepath.ToSlash(rel)); err != nil {
			return err
		}

		var cc ComplianceConfig
		if unmarshalErr := yaml.Unmarshal(data, &cc); unmarshalErr != nil {
			return fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, unmarshalErr))
		}

		// Canonical-filename rule: compliance claims belong in
		// compliance.vgo, not waivers.vgo. Publish-time validator
		// rejects this fatally; at load time we warn and ignore.
		if len(cc.Compliance) > 0 {
			slog.Warn("compliance: block in waivers.vgo has no effect — move to compliance.vgo",
				"file", filepath.ToSlash(rel))
		}

		fe := EnvoyWaiverFileEntry{
			RelPath: filepath.ToSlash(rel),
			Waivers: make(map[string][]*WaiverEntry),
		}

		now := time.Now()
		for standard, entries := range cc.Waivers {
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

// ModuleComplianceSource records a single compliance.vgo file under the
// modules tree: its stockpile-relative path and the claims it declared.
// Used for trace display and debugging.
type ModuleComplianceSource struct {
	RelPath    string              // e.g., "modules/users/compliance.vgo"
	Compliance map[string][]string // framework → control IDs
}

// validComplianceKeys is the exhaustive set of top-level keys allowed in a
// compliance.vgo file. Anything else is a parse-time error.
var validComplianceKeys = map[string]bool{
	"compliance": true,
	"waivers":    true,
}

// validateComplianceTopLevelKeys rejects compliance.vgo files that declare
// any top-level key other than "compliance:" or "waivers:". This is the
// strict-mode replacement for yaml.Unmarshal's default permissive behavior.
func validateComplianceTopLevelKeys(data []byte, relPath string) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Let the main unmarshal emit the real error; this pre-pass is
		// only for unknown-key detection.
		return nil
	}
	for k := range raw {
		if !validComplianceKeys[k] {
			return fmt.Errorf("%s: unknown top-level key %q — compliance.vgo files accept only 'compliance:' and 'waivers:'", relPath, k)
		}
	}
	return nil
}

// loadComplianceConfigs is the single walker for compliance.vgo claim files.
// It descends the entire stockpile tree — modules/, usercrates/, everything —
// and collects every compliance.vgo exactly once (do not add a parallel
// per-subtree walker; this one already covers them). Returns a map keyed by
// the file's parent directory (stockpile-relative, forward slashes) holding
// the framework → control-IDs map that file declares, plus a flat list of
// per-file source entries for trace display.
//
// Waivers declared in a stockpile-tree compliance.vgo are ignored (modlint
// flags them as a warning); this loader only harvests the compliance: block.
func loadComplianceConfigs(stockpileDir string) (map[string]map[string][]string, []ModuleComplianceSource, error) {
	byDir := make(map[string]map[string][]string)
	var sources []ModuleComplianceSource

	if _, err := os.Stat(stockpileDir); os.IsNotExist(err) {
		return byDir, sources, nil
	}

	err := filepath.WalkDir(stockpileDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Base(path) != "compliance.vgo" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		data = sanitizeYAML(data)

		rel, _ := filepath.Rel(stockpileDir, path)
		if err := validateComplianceTopLevelKeys(data, filepath.ToSlash(rel)); err != nil {
			return err
		}

		var cc ComplianceConfig
		if unmarshalErr := yaml.Unmarshal(data, &cc); unmarshalErr != nil {
			return fmt.Errorf("parsing %s: %w", path, formatYAMLError(data, unmarshalErr))
		}

		// Location-keyed warning: a claims compliance.vgo has no envoy to
		// attach waivers to.
		if len(cc.Waivers) > 0 {
			slog.Warn("waivers: block in a compliance.vgo claims file has no effect — waivers must live in the envoys tree",
				"file", filepath.ToSlash(rel))
		}

		if len(cc.Compliance) == 0 {
			// File has no claims to contribute; skip.
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		dirKey := filepath.ToSlash(filepath.Dir(rel))
		if dirKey == "." {
			dirKey = ""
		}

		// Clone the framework map so later mutation of byDir doesn't
		// leak into sources, and vice versa.
		cloned := make(map[string][]string, len(cc.Compliance))
		for fw, controls := range cc.Compliance {
			cloned[fw] = append([]string(nil), controls...)
		}

		// Union into byDir (multiple compliance.vgo files in the same
		// directory would be unusual but handle it cleanly).
		if existing, ok := byDir[dirKey]; ok {
			for fw, controls := range cloned {
				existing[fw] = unionDedupeStrings(existing[fw], controls)
			}
		} else {
			byDir[dirKey] = cloned
		}

		sources = append(sources, ModuleComplianceSource{
			RelPath:    relSlash,
			Compliance: cloned,
		})

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	return byDir, sources, nil
}

// unionDedupeStrings returns the deduplicated union of two string slices
// in order-preserving fashion (first occurrence wins).
func unionDedupeStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// applyModuleComplianceInheritance unions claims from every ancestor
// compliance.vgo in the modules tree into each module's own Compliance
// block. For a module at modules/users/dan.vgo, this walks ["modules/users",
// "modules"] and unions each matching directory's claims into def.Compliance.
func applyModuleComplianceInheritance(
	defs map[string]*ModuleDefinition,
	byDir map[string]map[string][]string,
) {
	if len(byDir) == 0 {
		return
	}
	for _, def := range defs {
		if def.SourceFile == "" {
			continue
		}
		// SourceFile is stockpile-relative (e.g. "modules/users/dan.vgo").
		dir := filepath.ToSlash(filepath.Dir(def.SourceFile))

		// Walk from the module's directory up to (and including) "modules".
		// Collect directory keys.
		var ancestors []string
		for {
			ancestors = append(ancestors, dir)
			if dir == "modules" || dir == "." || dir == "" {
				break
			}
			parent := filepath.ToSlash(filepath.Dir(dir))
			if parent == dir {
				break
			}
			dir = parent
		}

		// Union ancestor claims into def.InheritedCompliance — the
		// loader-internal framework→control map (operator-facing
		// `compliance:` on a module is the new typed ComplianceClaims
		// shape, not this map).
		for _, d := range ancestors {
			dirClaims, ok := byDir[d]
			if !ok {
				continue
			}
			if def.InheritedCompliance == nil {
				def.InheritedCompliance = make(map[string][]string)
			}
			for fw, controls := range dirClaims {
				def.InheritedCompliance[fw] = unionDedupeStrings(def.InheritedCompliance[fw], controls)
			}
		}
	}
}

// loadPerEnvOverrides loads environments.vgo files from the config
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
		if d.IsDir() || filepath.Base(path) != "environments.vgo" {
			return nil
		}

		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "modules"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "templates"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "tasks"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "workflows"+string(filepath.Separator)) ||
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
			slog.Info("loaded environments.vgo", "path", rel, "environments", len(peo.Env))
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
		// Skip subtrees that aren't part of the envoy assignment hierarchy:
		// modules/ (module defs), templates/ (template fragments), tasks/
		// (task defs), workflows/ (workflow defs), examples/ (read-only
		// reference templates). usercrates/ is skipped below per directory
		// segment; usercrates can live at any depth.
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "modules"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "templates"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "tasks"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "workflows"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, "examples"+string(filepath.Separator)) {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		for _, seg := range strings.Split(relSlash, "/") {
			if seg == "usercrates" {
				return nil
			}
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

		// Canonical files with their own loaders (claims/waivers/per-env
		// overrides) are not envoy entries.
		if baseName == "compliance.vgo" || baseName == "waivers.vgo" || baseName == "environments.vgo" {
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
			// Soft-warn when a match block carries a long inline modules
			// list. The canonical path is to define a role with those
			// modules and assign the role by name; long inline lists are
			// how stockpiles grow one-off configurations that belong in a
			// standard location. Threshold of 4 permits "one envoy gets a
			// module or two beyond its role" without forcing role ceremony
			// on genuinely small exceptions.
			if len(entry.Modules) > 4 {
				slog.Warn("match-block modules: list is longer than 4 — prefer a named role",
					"file", path, "match", entry.Match, "count", len(entry.Modules),
					"guidance", "define a role in roles.vgo and reference it via 'roles:' on the match block")
			}
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

// resolveVars enforces known-secret fields and substitutes secret: prefixes
// with placeholder markers. Each `secret:foo` ref is validated against the
// provider (so a missing-secret error still surfaces at load time, at
// parity with the pre-placeholder behavior) and then replaced with the
// `__VIGO_SECRET__foo__END_VIGO_SECRET__` marker — the resolved plaintext
// value is discarded after the validation lookup, never stored in `vars`.
// Errors are collected across all keys so the operator can fix everything
// in one pass.
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
			if _, err := cs.provider.Get(context.Background(), key); err != nil {
				errs = append(errs, &SecretError{
					Match:  match,
					Attr:   k,
					Secret: key,
					Err:    err,
				})
				continue
			}
			vars[k] = formatSecretPlaceholder(key)
		}
	}
	return errs
}

// resolveModuleSecrets resolves secret: prefixes in module resource attributes.
// It collects all failures rather than stopping at the first. Both whole-value
// (`password: secret:vigo/db/pw`) and embedded (`env: "PW=secret:vigo/db/pw,DB=app"`)
// occurrences are substituted via resolveSecretsInValue.
func (cs *ConfigStore) resolveModuleSecrets(match string, mod *ModuleConfig) []*SecretError {
	var errs []*SecretError
	for i := range mod.Resources {
		attrs := mod.Resources[i].Attributes
		for k, v := range attrs {
			str, ok := v.(string)
			if !ok {
				continue
			}
			if !strings.Contains(str, secretPrefix) {
				continue
			}
			resolved, failures := cs.resolveSecretsInValue(str)
			for _, f := range failures {
				errs = append(errs, &SecretError{
					Match:    match,
					Module:   mod.Name,
					Resource: mod.Resources[i].Name,
					Attr:     k,
					Secret:   f.key,
					Err:      f.err,
				})
			}
			if len(failures) == 0 {
				attrs[k] = resolved
			}
		}
	}
	return errs
}

// secretResolveFailure captures a single unresolved secret reference inside
// a compound attribute value. resolveSecretsInValue returns one per failed
// lookup so the caller can decorate each with its surrounding context (match
// / module / resource / attr).
type secretResolveFailure struct {
	key string
	err error
}

// resolveSecretsInValue substitutes every `secret:<key>` substring in str
// with a placeholder marker (`__VIGO_SECRET__<key>__END_VIGO_SECRET__`)
// after validating that the key resolves against the provider — the
// resolved plaintext value is discarded after the validation lookup.
// A key runs from immediately after `secret:` up to the first byte that is
// not a valid secret-key character (see isSecretKeyChar), so compound k=v
// values like `PW=secret:vigo/db/pw,DB=app` work the same as whole-value
// references. A `secret:` followed immediately by a non-key byte (e.g. the
// literal text "`secret:`" in a config file's content) yields an empty key
// and is preserved verbatim, not mistaken for a reference.
//
// Failures don't poison the whole string: any reference that fails to
// validate is left in place (as `secret:<key>`) and reported. The caller
// only commits the substituted value when failures is empty.
//
// Placeholder markers are emitted exactly once per ref — they are not
// re-scanned for nested `secret:` content.
func (cs *ConfigStore) resolveSecretsInValue(str string) (string, []secretResolveFailure) {
	if !strings.Contains(str, secretPrefix) {
		return str, nil
	}
	var b strings.Builder
	b.Grow(len(str))
	var failures []secretResolveFailure
	i := 0
	for i < len(str) {
		rel := strings.Index(str[i:], secretPrefix)
		if rel < 0 {
			b.WriteString(str[i:])
			break
		}
		b.WriteString(str[i : i+rel])
		keyStart := i + rel + len(secretPrefix)
		keyEnd := keyStart
		for keyEnd < len(str) && isSecretKeyChar(str[keyEnd]) {
			keyEnd++
		}
		key := str[keyStart:keyEnd]
		if key == "" {
			// Bare `secret:` with no key — preserve verbatim, advance past it.
			b.WriteString(secretPrefix)
			i = keyStart
			continue
		}
		if _, err := cs.provider.Get(context.Background(), key); err != nil {
			failures = append(failures, secretResolveFailure{key: key, err: err})
			b.WriteString(secretPrefix)
			b.WriteString(key)
		} else {
			b.WriteString(formatSecretPlaceholder(key))
		}
		i = keyEnd
	}
	return b.String(), failures
}

// isSecretKeyChar reports whether c is valid inside a secret key. Secret keys
// are provider paths (e.g. "vigo/db/dsn"): ASCII alphanumerics plus the path
// punctuation `.` `_` `-` `/`. Any other byte terminates the key. This is an
// allowlist on purpose — a denylist of separators can't tell a real reference
// from literal `secret:` text in file content (documentation, comments,
// the word "secret:" in prose), and would consume trailing punctuation
// (a backtick, `<`, `*`) into a bogus key. The first non-key byte after
// `secret:` yields an empty key, preserved verbatim by the caller.
func isSecretKeyChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '_', c == '-', c == '/':
		return true
	}
	return false
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

			// Record the reference so reachability analysis can tell
			// which template files are live. Path is normalized to
			// forward slashes and treated as stockpile-relative.
			def.TemplateSources = append(def.TemplateSources, filepath.ToSlash(sourcePath))
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

// entryReferencesSecret checks if an envoy entry uses any affected module.
// cfg.Vars is already resolved, so checking it would never match the raw
// secret: prefix; only modules need the affected-check.
func entryReferencesSecret(cfg *EnvoyConfig, affectedModules map[string]bool, _ string) bool {
	for _, mod := range cfg.Modules {
		if affectedModules[mod.Name] {
			return true
		}
	}
	return false
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
			Name:        r.Name,
			Type:        r.Type,
			When:        r.When,
			Attributes:  r.Attributes,
			DependsOn:   r.DependsOn,
			Before:      r.Before,
			Notify:      r.Notify,
			Subscribes:  r.Subscribes,
			WatchSecret: r.WatchSecret,
		})
	}
	return mod
}

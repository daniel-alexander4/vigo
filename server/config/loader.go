package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/isogov/isogov/server/secrets"
	"gopkg.in/yaml.v3"
)

// knownSecretFields are var names that must use the "secret:" prefix.
// If a config sets one of these to a plain value, loading fails.
var knownSecretFields = map[string]bool{
	"dsn":            true,
	"tls_cert_path":  true,
	"tls_key_path":   true,
	"webhook_secret": true,
}

const secretPrefix = "secret:"

// resolvedEntry pairs a match pattern with a resolved client config.
type resolvedEntry struct {
	match  string
	config *ClientConfig
}

// ConfigStore loads, watches, and provides per-client configuration.
// The clientsDir contains .yaml files with client entries at the top level,
// plus modules/ and roles/ subdirectories. Secret references are resolved
// via a secrets.Provider. Hot-reload via fsnotify.
type ConfigStore struct {
	mu         sync.RWMutex
	entries    []resolvedEntry
	rawModules map[string]*ModuleDefinition // unresolved, for secret ref scanning
	dir        string                       // client-configs directory
	provider   secrets.Provider
}

// NewConfigStore creates a ConfigStore that scans clientsDir for client
// YAML files, module definitions from clientsDir/modules/, and role
// definitions from clientsDir/roles/. Secret references are resolved
// using the given provider.
func NewConfigStore(clientsDir string, provider secrets.Provider) (*ConfigStore, error) {
	cs := &ConfigStore{
		dir:      clientsDir,
		provider: provider,
	}
	if err := cs.loadAll(); err != nil {
		return nil, err
	}
	return cs, nil
}

// Dir returns the client-configs directory path.
func (cs *ConfigStore) Dir() string {
	return cs.dir
}

// Get returns the resolved config for the given hostname. Entries are
// evaluated in order — first match wins. If no entry matches, an empty
// config is returned (unconfigured client gets empty module list).
func (cs *ConfigStore) Get(hostname string) *ClientConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for _, entry := range cs.entries {
		matched, err := filepath.Match(entry.match, hostname)
		if err != nil {
			continue
		}
		if matched {
			cfg := cloneClientConfig(entry.config)
			cfg.Client = hostname
			return cfg
		}
	}

	return &ClientConfig{
		Client:  hostname,
		Modules: []ModuleConfig{},
	}
}

// Watch starts fsnotify watchers on the client-configs directory and its
// modules/ and roles/ subdirectories. Config changes are reloaded with
// ~500ms debounce. Blocks until ctx is cancelled.
func (cs *ConfigStore) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	// Watch the top-level dir (client YAML files) and subdirectories.
	for _, dir := range []string{cs.dir, cs.modulesDir(), cs.rolesDir()} {
		if err := watcher.Add(dir); err != nil {
			return fmt.Errorf("watching %s: %w", dir, err)
		}
	}

	var debounceTimer *time.Timer
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !isYAMLFile(event.Name) {
				continue
			}
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				if err := cs.loadAll(); err != nil {
					slog.Error("config reload failed, keeping previous config", "error", err)
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Error("fsnotify error", "error", err)
		}
	}
}

// modulesDir returns the path to the modules subdirectory.
func (cs *ConfigStore) modulesDir() string { return filepath.Join(cs.dir, "modules") }

// rolesDir returns the path to the roles subdirectory.
func (cs *ConfigStore) rolesDir() string { return filepath.Join(cs.dir, "roles") }

// loadAll reads module defs, role defs, and client YAML files, resolves
// all entries, and atomically swaps the config.
func (cs *ConfigStore) loadAll() error {
	moduleDefs, err := loadModuleDefs(cs.modulesDir())
	if err != nil {
		return fmt.Errorf("loading modules: %w", err)
	}

	roleDefs, err := loadRoleDefs(cs.rolesDir())
	if err != nil {
		return fmt.Errorf("loading roles: %w", err)
	}

	clientEntries, err := loadClientsDir(cs.dir)
	if err != nil {
		return fmt.Errorf("loading clients: %w", err)
	}

	// Validate role references.
	for _, role := range roleDefs {
		for _, modName := range role.Modules {
			if _, ok := moduleDefs[modName]; !ok {
				return fmt.Errorf("role %q references unknown module %q", role.Name, modName)
			}
		}
	}

	var entries []resolvedEntry
	for i, entry := range clientEntries {
		cfg, err := cs.resolveEntry(entry, moduleDefs, roleDefs)
		if err != nil {
			return fmt.Errorf("resolving client entry %d (match=%q): %w", i, entry.Match, err)
		}
		entries = append(entries, resolvedEntry{match: entry.Match, config: cfg})
	}

	cs.mu.Lock()
	cs.entries = entries
	cs.rawModules = moduleDefs
	cs.mu.Unlock()

	slog.Info("config loaded",
		"modules", len(moduleDefs),
		"roles", len(roleDefs),
		"entries", len(entries))

	return nil
}

// resolveEntry converts a ClientEntry into a fully resolved ClientConfig.
func (cs *ConfigStore) resolveEntry(entry ClientEntry, moduleDefs map[string]*ModuleDefinition, roleDefs map[string]*RoleDefinition) (*ClientConfig, error) {
	// Collect module names: roles first, then direct modules.
	var moduleNames []string
	for _, roleName := range entry.Roles {
		role, ok := roleDefs[roleName]
		if !ok {
			return nil, fmt.Errorf("unknown role %q", roleName)
		}
		moduleNames = append(moduleNames, role.Modules...)
	}
	moduleNames = append(moduleNames, entry.Modules...)
	moduleNames = dedupStrings(moduleNames)

	// Resolve module definitions into ModuleConfigs.
	var modules []ModuleConfig
	for _, name := range moduleNames {
		def, ok := moduleDefs[name]
		if !ok {
			return nil, fmt.Errorf("unknown module %q", name)
		}
		modules = append(modules, ModuleConfig{
			Name:      def.Name,
			DependsOn: append([]string(nil), def.DependsOn...),
			Resources: cloneResources(def.Resources),
		})
	}

	// Build vars: module defaults first, then client-level overrides.
	vars := make(map[string]any)
	for _, name := range moduleNames {
		for k, v := range moduleDefs[name].Vars {
			vars[k] = v
		}
	}
	for k, v := range entry.Vars {
		vars[k] = v
	}

	cfg := &ClientConfig{
		Client:      entry.Match,
		Environment: entry.Environment,
		Modules:     modules,
		Vars:        vars,
		EnvVars:     entry.EnvVars,
	}

	// Merge environment-specific vars.
	mergeEnvVars(cfg)

	// Enforce known-secret fields and resolve secret: prefixes.
	if err := cs.resolveVars(cfg.Vars); err != nil {
		return nil, fmt.Errorf("resolving vars: %w", err)
	}
	for i := range cfg.Modules {
		if err := cs.resolveModuleSecrets(&cfg.Modules[i]); err != nil {
			return nil, fmt.Errorf("module %q: %w", cfg.Modules[i].Name, err)
		}
	}

	return cfg, nil
}

// ClientsReferencingSecret returns the match patterns of all client entries
// whose configuration contains a "secret:<secretPath>" reference. Used by
// the rotation watcher to identify which clients need force-push.
func (cs *ConfigStore) ClientsReferencingSecret(secretPath string) []string {
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

// EffectiveConfigYAML returns the resolved config for hostname as YAML,
// with secret values redacted. Used by the UI config viewer.
func (cs *ConfigStore) EffectiveConfigYAML(hostname string) (string, error) {
	cfg := cs.Get(hostname)
	if cfg == nil || len(cfg.Modules) == 0 {
		return "", fmt.Errorf("no config found for %s", hostname)
	}

	// Redact resolved secrets in vars.
	redacted := cloneClientConfig(cfg)
	for k, v := range redacted.Vars {
		if s, ok := v.(string); ok && knownSecretFields[k] {
			_ = s
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

// loadModuleDefs reads all YAML files from the modules directory.
func loadModuleDefs(dir string) (map[string]*ModuleDefinition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*ModuleDefinition), nil
		}
		return nil, fmt.Errorf("reading modules dir: %w", err)
	}

	defs := make(map[string]*ModuleDefinition)
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var def ModuleDefinition
		if err := yaml.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if def.Name == "" {
			return nil, fmt.Errorf("%s: missing required 'name' field", path)
		}
		if _, exists := defs[def.Name]; exists {
			return nil, fmt.Errorf("duplicate module name %q in %s", def.Name, path)
		}
		defs[def.Name] = &def
	}
	return defs, nil
}

// loadRoleDefs reads all YAML files from the roles directory.
func loadRoleDefs(dir string) (map[string]*RoleDefinition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*RoleDefinition), nil
		}
		return nil, fmt.Errorf("reading roles dir: %w", err)
	}

	defs := make(map[string]*RoleDefinition)
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
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
	return defs, nil
}

// loadClientsDir reads all .yaml files at the top level of dir (not subdirs)
// and merges their client entries in filesystem order.
func loadClientsDir(dir string) ([]ClientEntry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading clients dir: %w", err)
	}

	var allEntries []ClientEntry
	for _, de := range dirEntries {
		if de.IsDir() || !isYAMLFile(de.Name()) {
			continue
		}
		path := filepath.Join(dir, de.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var cfg ClientsConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		for i, entry := range cfg.Clients {
			if entry.Match == "" {
				return nil, fmt.Errorf("%s entry %d: missing required 'match' field", de.Name(), i)
			}
		}
		allEntries = append(allEntries, cfg.Clients...)
	}
	return allEntries, nil
}

// mergeEnvVars merges base vars with environment-specific overrides.
func mergeEnvVars(cfg *ClientConfig) {
	if cfg.Vars == nil {
		cfg.Vars = make(map[string]any)
	}
	if cfg.Environment == "" || cfg.EnvVars == nil {
		return
	}
	envOverrides, ok := cfg.EnvVars[cfg.Environment]
	if !ok {
		return
	}
	for k, v := range envOverrides {
		cfg.Vars[k] = v
	}
}

// resolveVars enforces known-secret fields and resolves secret: prefixes.
func (cs *ConfigStore) resolveVars(vars map[string]any) error {
	for k, v := range vars {
		str, ok := v.(string)
		if !ok {
			continue
		}
		if knownSecretFields[k] && !strings.HasPrefix(str, secretPrefix) {
			return fmt.Errorf(
				"known-secret field %q must use %q prefix (got %q) -- "+
					"store secrets in isopass, not in config files",
				k, secretPrefix, str,
			)
		}
		if strings.HasPrefix(str, secretPrefix) {
			key := strings.TrimPrefix(str, secretPrefix)
			resolved, err := cs.provider.Get(context.Background(), key)
			if err != nil {
				return fmt.Errorf("resolving secret %q: %w", key, err)
			}
			vars[k] = resolved
		}
	}
	return nil
}

// resolveModuleSecrets resolves secret: prefixes in module resource attributes.
func (cs *ConfigStore) resolveModuleSecrets(mod *ModuleConfig) error {
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
					return fmt.Errorf("resolving secret for resource %q attr %q: %w",
						mod.Resources[i].Name, k, err)
				}
				attrs[k] = resolved
			}
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

// entryReferencesSecret checks if a client entry uses any affected module
// or has the secret in its vars/env_vars.
func entryReferencesSecret(cfg *ClientConfig, affectedModules map[string]bool, needle string) bool {
	// Check if any module is affected.
	for _, mod := range cfg.Modules {
		if affectedModules[mod.Name] {
			return true
		}
	}
	// Check vars (these are pre-merged, so check EnvVars too).
	for _, v := range cfg.Vars {
		if s, ok := v.(string); ok && s == needle {
			return true
		}
	}
	if cfg.EnvVars != nil {
		for _, envMap := range cfg.EnvVars {
			for _, v := range envMap {
				if s, ok := v.(string); ok && s == needle {
					return true
				}
			}
		}
	}
	return false
}

// cloneClientConfig returns a shallow copy of the ClientConfig with a
// fresh Modules slice and Vars map, so modifications don't affect the original.
func cloneClientConfig(cfg *ClientConfig) *ClientConfig {
	clone := *cfg
	clone.Modules = make([]ModuleConfig, len(cfg.Modules))
	copy(clone.Modules, cfg.Modules)
	clone.Vars = make(map[string]any, len(cfg.Vars))
	for k, v := range cfg.Vars {
		clone.Vars[k] = v
	}
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
	}
	return cloned
}

// dedupStrings removes duplicates from a string slice, preserving order.
func dedupStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// isYAMLFile checks if a filename has a YAML extension.
func isYAMLFile(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yaml" || ext == ".yml"
}

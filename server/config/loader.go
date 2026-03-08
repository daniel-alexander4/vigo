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
	"github.com/hobnob/hobnob/server/conditionals"
	"github.com/hobnob/hobnob/server/secrets"
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
	// Watch templates dir if it exists (template files aren't YAML).
	if _, err := os.Stat(cs.templatesDir()); err == nil {
		if err := watcher.Add(cs.templatesDir()); err != nil {
			return fmt.Errorf("watching %s: %w", cs.templatesDir(), err)
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
			inTemplatesDir := strings.HasPrefix(event.Name, cs.templatesDir()+string(os.PathSeparator))
			if !isYAMLFile(event.Name) && !inTemplatesDir {
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

// templatesDir returns the path to the templates subdirectory.
func (cs *ConfigStore) templatesDir() string { return filepath.Join(cs.dir, "templates") }

// loadAll reads module defs, role defs, and client YAML files, resolves
// all entries, and atomically swaps the config.
func (cs *ConfigStore) loadAll() error {
	moduleDefs, err := loadModuleDefs(cs.modulesDir())
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

	clientEntries, err := loadClientsDir(cs.dir)
	if err != nil {
		return fmt.Errorf("loading clients: %w", err)
	}

	// Validate role references.
	for _, role := range roleDefs {
		for _, ref := range role.Modules {
			if _, ok := moduleDefs[ref.Name]; !ok {
				return fmt.Errorf("role %q references unknown module %q", role.Name, ref.Name)
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
	// Collect module refs: roles first, then direct modules. First ref wins
	// for conditional fields (matching existing var merge behavior).
	var moduleRefs []ModuleRef
	seen := make(map[string]bool)
	for _, roleName := range entry.Roles {
		role, ok := roleDefs[roleName]
		if !ok {
			return nil, fmt.Errorf("unknown role %q", roleName)
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
			return nil, fmt.Errorf("unknown module %q", ref.Name)
		}
		modules = append(modules, ModuleConfig{
			Name:      def.Name,
			DependsOn: append([]string(nil), def.DependsOn...),
			When:      ref.When,
			Resources: cloneResources(def.Resources),
		})
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

	// Build vars: module defaults first, then client-level overrides.
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
		// Skip client-level vars that would conflict with conditional var names.
		// Conditional vars take precedence -- they are evaluated at check-in time.
		if condVarNames[k] {
			continue
		}
		if _, exists := vars[k]; !exists {
			varOrder = append(varOrder, k)
		}
		vars[k] = v
	}

	cfg := &ClientConfig{
		Client:          entry.Match,
		Environment:     entry.Environment,
		Modules:         modules,
		Vars:            vars,
		VarOrder:        varOrder,
		EnvVars:         entry.EnvVars,
		ConditionalVars: condVars,
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
		if _, exists := cfg.Vars[k]; !exists {
			cfg.VarOrder = append(cfg.VarOrder, k)
		}
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

// resolveSourceFiles reads external template files referenced by the "source"
// attribute on file resources. The source path is relative to the client-configs
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

			fullPath := filepath.Join(baseDir, sourcePath)
			data, err := os.ReadFile(fullPath)
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

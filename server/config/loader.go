package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/hobnob/hobnob/server/conditionals"
	"github.com/hobnob/hobnob/server/secrets"
	"github.com/hobnob/hobnob/server/validate"
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

// resolvedEntry pairs a match pattern with a resolved node config.
type resolvedEntry struct {
	match  string
	config *NodeConfig
}

// ConfigStore loads, watches, and provides per-node configuration.
// The base dir contains nodes/, modules/, and roles/ subdirectories with
// .hnb/.yaml config files. Secret references are resolved via a
// secrets.Provider. Hot-reload via fsnotify.
type ConfigStore struct {
	mu         sync.RWMutex
	entries    []resolvedEntry
	rawModules map[string]*ModuleDefinition // unresolved, for secret ref scanning
	dir        string                       // node-configs directory
	provider   secrets.Provider
	dangerous  bool // allow non-idempotent resources
	version    atomic.Uint64 // incremented on each successful reload
}

// NewConfigStore creates a ConfigStore that scans nodesDir for node
// config files (.hnb or .yaml), module definitions from nodesDir/modules/,
// and role definitions from nodesDir/roles/. Secret references are resolved
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

// Dir returns the node-configs directory path.
func (cs *ConfigStore) Dir() string {
	return cs.dir
}

// Version returns the current config version. Incremented on each successful
// reload, used by the response cache to detect config changes.
func (cs *ConfigStore) Version() uint64 {
	return cs.version.Load()
}

// Get returns the resolved config for the given hostname. Entries are
// evaluated in order — first match wins. If no entry matches, an empty
// config is returned (unconfigured node gets empty module list).
func (cs *ConfigStore) Get(hostname string) *NodeConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for _, entry := range cs.entries {
		matched, err := filepath.Match(entry.match, hostname)
		if err != nil {
			continue
		}
		if matched {
			cfg := cloneNodeConfig(entry.config)
			cfg.Node = hostname
			return cfg
		}
	}

	return &NodeConfig{
		Node:    hostname,
		Modules: []ModuleConfig{},
	}
}

// Watch starts fsnotify watchers on the node-configs directory and its
// modules/ and roles/ subdirectories. Config changes are reloaded with
// ~500ms debounce. Blocks until ctx is cancelled.
func (cs *ConfigStore) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	// Watch the nodes, modules, roles, and templates trees recursively.
	for _, dir := range []string{cs.nodesDir(), cs.modulesDir(), cs.rolesDir(), cs.templatesDir()} {
		if err := watchRecursive(watcher, dir); err != nil {
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
			inTemplatesDir := strings.HasPrefix(event.Name, cs.templatesDir()+string(os.PathSeparator))
			if !isConfigFile(event.Name) && !inTemplatesDir {
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

// nodesDir returns the path to the nodes subdirectory.
func (cs *ConfigStore) nodesDir() string { return filepath.Join(cs.dir, "nodes") }

// modulesDir returns the path to the modules subdirectory.
func (cs *ConfigStore) modulesDir() string { return filepath.Join(cs.dir, "modules") }

// rolesDir returns the path to the roles subdirectory.
func (cs *ConfigStore) rolesDir() string { return filepath.Join(cs.dir, "roles") }

// templatesDir returns the path to the templates subdirectory.
func (cs *ConfigStore) templatesDir() string { return filepath.Join(cs.dir, "templates") }

// loadAll reads module defs, role defs, and node YAML files, resolves
// all entries, and atomically swaps the config.
func (cs *ConfigStore) loadAll() error {
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

	nodeEntries, err := loadNodesDir(cs.nodesDir())
	if err != nil {
		return fmt.Errorf("loading nodes: %w", err)
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
	for i, entry := range nodeEntries {
		cfg, err := cs.resolveEntry(entry, moduleDefs, roleDefs)
		if err != nil {
			return fmt.Errorf("resolving node entry %d (match=%q): %w", i, entry.Match, err)
		}
		entries = append(entries, resolvedEntry{match: entry.Match, config: cfg})
	}

	cs.mu.Lock()
	cs.entries = entries
	cs.rawModules = moduleDefs
	cs.mu.Unlock()

	v := cs.version.Add(1)

	slog.Info("config loaded",
		"modules", len(moduleDefs),
		"roles", len(roleDefs),
		"entries", len(entries),
		"version", v)

	return nil
}

// resolveEntry converts a NodeEntry into a fully resolved NodeConfig.
func (cs *ConfigStore) resolveEntry(entry NodeEntry, moduleDefs map[string]*ModuleDefinition, roleDefs map[string]*RoleDefinition) (*NodeConfig, error) {
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

	// Build vars: module defaults first, then node-level overrides.
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
		// Skip node-level vars that would conflict with conditional var names.
		// Conditional vars take precedence -- they are evaluated at check-in time.
		if condVarNames[k] {
			continue
		}
		if _, exists := vars[k]; !exists {
			varOrder = append(varOrder, k)
		}
		vars[k] = v
	}

	cfg := &NodeConfig{
		Node:            entry.Match,
		MatchPattern:    entry.Match,
		Environment:     entry.Environment,
		Roles:           entry.Roles,
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

// NodesReferencingSecret returns the match patterns of all node entries
// whose configuration contains a "secret:<secretPath>" reference. Used by
// the rotation watcher to identify which nodes need force-push.
func (cs *ConfigStore) NodesReferencingSecret(secretPath string) []string {
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
	redacted := cloneNodeConfig(cfg)
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
		// A conditional var is any map value with a "nail" key or any
		// known operator key (in, not_in, gt, lt, gte, lte, regex).
		// Maps with operator keys but no nail are caught as validation
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

		// Idempotency validation.
		findings := validate.ValidateModule(moduleToValidate(def))
		for _, f := range findings {
			if f.Severity == validate.SeverityWarning {
				slog.Warn("idempotency warning", "module", def.Name, "finding", f.String())
			}
		}
		if !dangerous && validate.HasErrors(findings) {
			return nil, fmt.Errorf("module %q failed idempotency check in %s:\n%s"+
				"hint: fix the resources above, or start the server with --dangerous to bypass",
				def.Name, path, validate.FormatFindings(findings))
		}

		defs[def.Name] = def
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
	return defs, nil
}

// loadNodesDir reads all config files (.hnb/.yaml) from the nodes directory
// and merges their node entries in filesystem order.
func loadNodesDir(dir string) ([]NodeEntry, error) {
	paths, err := walkConfigFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("reading nodes dir: %w", err)
	}

	var allEntries []NodeEntry
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var cfg NodesConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		for i, entry := range cfg.Nodes {
			if entry.Match == "" {
				return nil, fmt.Errorf("%s entry %d: missing required 'match' field", path, i)
			}
		}
		allEntries = append(allEntries, cfg.Nodes...)
	}
	return allEntries, nil
}

// mergeEnvVars merges base vars with environment-specific overrides.
func mergeEnvVars(cfg *NodeConfig) {
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
// attribute on file resources. The source path is relative to the node-configs
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

// entryReferencesSecret checks if a node entry uses any affected module
// or has the secret in its env_vars (non-active environment overrides retain
// raw secret: prefixes since only the active environment is resolved).
func entryReferencesSecret(cfg *NodeConfig, affectedModules map[string]bool, needle string) bool {
	// Check if any module is affected.
	for _, mod := range cfg.Modules {
		if affectedModules[mod.Name] {
			return true
		}
	}
	// Check env_vars — non-active environments retain raw secret: references.
	// (cfg.Vars is already resolved, so checking it would never match.)
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

// ComputeEnvDiff compares env_vars between two environments across all node
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

		fromVars := entry.config.EnvVars[fromEnv]
		toVars := entry.config.EnvVars[toEnv]

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

// cloneNodeConfig returns a shallow copy of the NodeConfig with a
// fresh Modules slice and Vars map, so modifications don't affect the original.
func cloneNodeConfig(cfg *NodeConfig) *NodeConfig {
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


// watchRecursive adds dir and all its subdirectories to the watcher.
// Silently skips directories that don't exist.
func watchRecursive(watcher *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})
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

// moduleToValidate converts a ModuleDefinition to a validate.Module.
func moduleToValidate(def *ModuleDefinition) validate.Module {
	mod := validate.Module{Name: def.Name}
	for _, r := range def.Resources {
		mod.Resources = append(mod.Resources, validate.Resource{
			Name:       r.Name,
			Type:       r.Type,
			Attributes: r.Attributes,
			Notify:     r.Notify,
			Subscribes: r.Subscribes,
		})
	}
	return mod
}

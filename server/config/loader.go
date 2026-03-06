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

// ConfigStore loads, watches, and provides per-client configuration.
// It resolves "secret:" prefixes via a secrets.Provider and hot-reloads
// on filesystem changes.
type ConfigStore struct {
	mu         sync.RWMutex
	clients    map[string]*ClientConfig
	dir        string
	provider   secrets.Provider
	classifier *Classifier
}

// NewConfigStore creates a ConfigStore that loads client configs from the
// given directory and resolves "secret:" prefixes using the given provider.
// classificationPath may point to a classification.yaml file; if it does
// not exist, classification is a no-op.
func NewConfigStore(clientsDir, classificationPath string, provider secrets.Provider) (*ConfigStore, error) {
	classifier, err := NewClassifier(classificationPath)
	if err != nil {
		return nil, fmt.Errorf("loading classifier: %w", err)
	}

	cs := &ConfigStore{
		clients:    make(map[string]*ClientConfig),
		dir:        clientsDir,
		provider:   provider,
		classifier: classifier,
	}

	if err := cs.loadAll(); err != nil {
		return nil, err
	}
	return cs, nil
}

// Get returns the resolved config for the given hostname. Lookup order:
//  1. Explicit client config from YAML file
//  2. Auto-classification from classification.yaml
//  3. Empty config (unconfigured client gets empty module list)
func (cs *ConfigStore) Get(hostname string) *ClientConfig {
	cs.mu.RLock()
	cfg, ok := cs.clients[hostname]
	cs.mu.RUnlock()
	if ok {
		return cfg
	}

	// Try classification
	if classified := cs.classifier.Classify(hostname); classified != nil {
		return classified
	}

	// Unconfigured client: empty module list
	return &ClientConfig{
		Client:  hostname,
		Modules: []ModuleConfig{},
	}
}

// Watch starts an fsnotify watcher on the clients directory.
// Config changes are reloaded with ~500ms debounce. Blocks until ctx is cancelled.
func (cs *ConfigStore) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(cs.dir); err != nil {
		return fmt.Errorf("watching directory %s: %w", cs.dir, err)
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

// loadAll reads all YAML files from the clients directory, validates them,
// resolves secrets, and atomically swaps the config map.
func (cs *ConfigStore) loadAll() error {
	entries, err := os.ReadDir(cs.dir)
	if err != nil {
		return fmt.Errorf("reading clients dir %s: %w", cs.dir, err)
	}

	newClients := make(map[string]*ClientConfig)
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}
		path := filepath.Join(cs.dir, entry.Name())
		cfg, err := cs.loadClientFile(path)
		if err != nil {
			return fmt.Errorf("loading %s: %w", path, err)
		}
		newClients[cfg.Client] = cfg
	}

	cs.mu.Lock()
	cs.clients = newClients
	cs.mu.Unlock()
	return nil
}

// loadClientFile reads, parses, validates, and resolves a single client YAML.
func (cs *ConfigStore) loadClientFile(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	var cfg ClientConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if cfg.Client == "" {
		return nil, fmt.Errorf("missing required 'client' field in %s", path)
	}

	// Merge env-specific vars into base vars
	mergeEnvVars(&cfg)

	// Enforce known-secret fields and resolve secret: prefixes in vars
	if err := cs.resolveVars(cfg.Vars); err != nil {
		return nil, fmt.Errorf("resolving vars for %s: %w", cfg.Client, err)
	}

	// Resolve secrets in module resources
	for i := range cfg.Modules {
		if err := cs.resolveModuleSecrets(&cfg.Modules[i]); err != nil {
			return nil, fmt.Errorf("resolving module %q secrets: %w", cfg.Modules[i].Name, err)
		}
	}

	return &cfg, nil
}

// mergeEnvVars merges base vars with environment-specific overrides.
// Environment-specific values win on conflict.
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

// resolveVars enforces known-secret fields and resolves secret: prefixes
// in the vars map.
func (cs *ConfigStore) resolveVars(vars map[string]any) error {
	for k, v := range vars {
		str, ok := v.(string)
		if !ok {
			continue
		}

		// Enforce known-secret fields
		if knownSecretFields[k] && !strings.HasPrefix(str, secretPrefix) {
			return fmt.Errorf(
				"known-secret field %q must use %q prefix (got %q) -- "+
					"store secrets in isopass, not in config files",
				k, secretPrefix, str,
			)
		}

		// Resolve secret: prefix
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

// ClientsReferencingSecret returns the hostnames of all clients whose
// configuration contains a "secret:<secretPath>" reference. This is used
// by the rotation watcher to identify which clients need force-push when
// a secret rotates.
func (cs *ConfigStore) ClientsReferencingSecret(secretPath string) []string {
	needle := secretPrefix + secretPath

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var hosts []string
	for hostname, cfg := range cs.clients {
		if configReferencesSecret(cfg, needle) {
			hosts = append(hosts, hostname)
		}
	}
	return hosts
}

// configReferencesSecret checks if any var or resource attribute in the
// client config contains the given secret reference string.
func configReferencesSecret(cfg *ClientConfig, needle string) bool {
	// Check top-level vars.
	for _, v := range cfg.Vars {
		if s, ok := v.(string); ok && s == needle {
			return true
		}
	}
	// Check env_vars.
	for _, envMap := range cfg.EnvVars {
		for _, v := range envMap {
			if s, ok := v.(string); ok && s == needle {
				return true
			}
		}
	}
	// Check module vars and resource attributes.
	for _, mod := range cfg.Modules {
		for _, v := range mod.Vars {
			if s, ok := v.(string); ok && s == needle {
				return true
			}
		}
		for _, res := range mod.Resources {
			for _, v := range res.Attributes {
				if s, ok := v.(string); ok && s == needle {
					return true
				}
			}
		}
	}
	return false
}

// isYAMLFile checks if a filename has a YAML extension.
func isYAMLFile(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yaml" || ext == ".yml"
}

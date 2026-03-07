// Package config handles loading, watching, and resolving per-client
// configuration from YAML files on disk. It integrates with the secrets
// provider to resolve "secret:" prefixes and supports hot-reload via fsnotify.
package config

// ServerConfig represents the top-level server configuration from server.yaml.
type ServerConfig struct {
	Server   ServerSection   `yaml:"server"`
	Database DatabaseSection `yaml:"database"`
	CheckIn  CheckInSection  `yaml:"checkin"`
	Watcher  WatcherSection  `yaml:"watcher"`
}

// WatcherSection holds secret rotation watcher configuration.
type WatcherSection struct {
	Enabled      bool   `yaml:"enabled"`
	PollInterval string `yaml:"poll_interval"`
}

// ServerSection holds listener and TLS config.
type ServerSection struct {
	GRPCListen string    `yaml:"grpc_listen"`
	APIListen  string    `yaml:"api_listen"`
	TLS        TLSConfig `yaml:"tls"`
}

// TLSConfig holds TLS file paths.
type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// DatabaseSection holds database config.
type DatabaseSection struct {
	DSN string `yaml:"dsn"`
}

// CheckInSection holds check-in interval settings.
type CheckInSection struct {
	BaseInterval  string `yaml:"base_interval"`
	JitterPercent int    `yaml:"jitter_percent"`
}

// ClientConfig represents the resolved per-client configuration.
// This is the output type returned by ConfigStore.Get().
type ClientConfig struct {
	Client      string            `yaml:"client"`
	Environment string            `yaml:"environment"`
	Modules     []ModuleConfig    `yaml:"modules"`
	Vars        map[string]any    `yaml:"vars"`
	VarOrder    []string          `yaml:"-"`
	EnvVars     map[string]map[string]any `yaml:"env_vars"`
}

// ModuleConfig describes a module assigned to a client.
type ModuleConfig struct {
	Name      string           `yaml:"name"`
	DependsOn []string         `yaml:"depends_on"`
	Resources []ResourceConfig `yaml:"resources"`
}

// ResourceConfig describes a single resource within a module.
type ResourceConfig struct {
	Name       string         `yaml:"name"`
	Type       string         `yaml:"type"`
	Attributes map[string]any `yaml:",inline"`
	Notify     []string       `yaml:"notify"`
	Subscribes []string       `yaml:"subscribes"`
	When       string         `yaml:"when"`
	DependsOn  []string       `yaml:"depends_on"`
}

// ModuleDefinition is a reusable module loaded from config/modules/*.yaml.
type ModuleDefinition struct {
	Name      string           `yaml:"name"`
	DependsOn []string         `yaml:"depends_on"`
	Resources []ResourceConfig `yaml:"resources"`
	Vars      map[string]any   `yaml:"vars"`
}

// RoleDefinition is a named list of modules loaded from config/roles/*.yaml.
type RoleDefinition struct {
	Name    string   `yaml:"name"`
	Modules []string `yaml:"modules"`
}

// ClientEntry is a single entry in config/clients.yaml.
type ClientEntry struct {
	Match       string                    `yaml:"match"`
	Environment string                    `yaml:"environment"`
	Roles       []string                  `yaml:"roles"`
	Modules     []string                  `yaml:"modules"`
	Vars        map[string]any            `yaml:"vars"`
	EnvVars     map[string]map[string]any `yaml:"env_vars"`
}

// ClientsConfig is the top-level structure of clients.yaml.
type ClientsConfig struct {
	Clients []ClientEntry `yaml:"clients"`
}

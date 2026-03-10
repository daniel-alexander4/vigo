// Package config handles loading, watching, and resolving per-node
// configuration from YAML files on disk. It integrates with the secrets
// provider to resolve "secret:" prefixes and supports hot-reload via fsnotify.
package config

import (
	"fmt"

	"github.com/hobnob/hobnob/server/conditionals"
	"github.com/hobnob/hobnob/server/secrets"
	"gopkg.in/yaml.v3"
)

// ServerConfig represents the top-level server configuration from server.yaml.
type ServerConfig struct {
	Server    ServerSection   `yaml:"server"`
	Database  DatabaseSection `yaml:"database"`
	CheckIn   CheckInSection  `yaml:"checkin"`
	Watcher   WatcherSection  `yaml:"watcher"`
	Secrets   secrets.Config  `yaml:"secrets"`
	Bootstrap BootstrapConfig `yaml:"bootstrap"`
	Tuning    TuningSection   `yaml:"tuning"`
}

// TuningSection holds operational tuning parameters. All fields are optional
// and default to sensible values when omitted from server.yaml.
type TuningSection struct {
	SignatureWindow     string        `yaml:"signature_window"`      // default "5m"
	NodeCacheTTL        string        `yaml:"node_cache_ttl"`        // default "60s"
	LastSeenFlush       string        `yaml:"last_seen_flush"`       // default "10s"
	TraitsPruneInterval string        `yaml:"traits_prune_interval"` // default "5m"
	MetricsInterval     string        `yaml:"metrics_interval"`      // default "30s"
	TraitsScannerTTL    string        `yaml:"traits_scanner_ttl"`    // default "30s"
	RunStore            string        `yaml:"run_store"`             // "database" (default) or "memory"
	RunStoreCapacity    int           `yaml:"run_store_capacity"`    // per-node run history depth for memory mode (default 20)
	Webhook             WebhookTuning `yaml:"webhook"`
}

// WebhookTuning holds webhook dispatch tuning parameters.
type WebhookTuning struct {
	Timeout       string `yaml:"timeout"`        // default "10s"
	MaxRetries    int    `yaml:"max_retries"`     // default 3
	MaxConcurrent int    `yaml:"max_concurrent"`  // default 20
	RetryDelay    string `yaml:"retry_delay"`     // default "5s"
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
	CertFile  string `yaml:"cert_file"`
	KeyFile   string `yaml:"key_file"`
	CAFile    string `yaml:"ca_file"`
	CAKeyFile string `yaml:"ca_key_file"`
}

// TrustedEnrollment defines a hostname/CIDR pattern for token-free enrollment.
// Nodes matching the pattern and originating from an allowed CIDR are enrolled
// without a one-time token (Puppet-style autosigning).
type TrustedEnrollment struct {
	Pattern string   `yaml:"pattern"`
	CIDRs   []string `yaml:"cidrs,omitempty"`
}

// BootstrapConfig holds bootstrap enrollment settings.
type BootstrapConfig struct {
	TrustedPatterns []TrustedEnrollment `yaml:"trusted_enrollment"`
	CertValidity    string              `yaml:"cert_validity"` // e.g. "8760h" (1 year)
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

// ConditionalBlock groups resources under a shared when expression.
// When flattened, block-level when is AND-composed with each child resource's when.
type ConditionalBlock struct {
	When      string           `yaml:"when,omitempty"`
	Resources []ResourceConfig `yaml:"resources"`
}

// NodeConfig represents the resolved per-node configuration.
// This is the output type returned by ConfigStore.Get().
type NodeConfig struct {
	Node            string                    `yaml:"node"`
	MatchPattern    string                    `yaml:"-"` // original match pattern from nodes.hnb
	Environment     string                    `yaml:"environment"`
	Roles           []string                  `yaml:"-"` // role names that were expanded
	Modules         []ModuleConfig            `yaml:"modules"`
	Vars            map[string]any            `yaml:"vars"`
	VarOrder        []string                  `yaml:"-"`
	EnvVars         map[string]map[string]any `yaml:"env_vars"`
	ConditionalVars []conditionals.ConditionalVar `yaml:"-"`
}

// ModuleConfig describes a module assigned to a node.
type ModuleConfig struct {
	Name      string           `yaml:"name"`
	DependsOn []string         `yaml:"depends_on"`
	When      string           `yaml:"when,omitempty"`
	Resources []ResourceConfig `yaml:"resources"`
}

// ResourceConfig describes a single resource within a module.
type ResourceConfig struct {
	Name       string         `yaml:"name"`
	Type       string         `yaml:"type"`
	When       string         `yaml:"when,omitempty"`
	Attributes map[string]any `yaml:",inline"`
	Notify     []string       `yaml:"notify"`
	Subscribes []string       `yaml:"subscribes"`
	DependsOn  []string       `yaml:"depends_on"`
	Before     []string       `yaml:"before"`
}

// ModuleDefinition is a reusable module loaded from config/modules/*.yaml.
// It has a custom UnmarshalYAML that flattens conditional_block entries
// in the resources list into plain ResourceConfig items.
type ModuleDefinition struct {
	Name            string                        `yaml:"name"`
	DependsOn       []string                      `yaml:"depends_on"`
	Before          []string                      `yaml:"before"`
	Resources       []ResourceConfig              `yaml:"resources"`
	Vars            map[string]any                `yaml:"vars"`
	ConditionalVars []conditionals.ConditionalVar `yaml:"-"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ModuleDefinition.
// It handles the "resources" key specially by passing its child nodes
// through FlattenResourceNodes to support conditional_block entries.
// All other keys are decoded via the default YAML decoder.
func (m *ModuleDefinition) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("ModuleDefinition: expected mapping, got %v", value.Kind)
	}

	// Walk key-value pairs in the mapping.
	for i := 0; i < len(value.Content)-1; i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]

		switch key {
		case "name":
			if err := val.Decode(&m.Name); err != nil {
				return fmt.Errorf("decoding name: %w", err)
			}
		case "depends_on", "require":
			if err := val.Decode(&m.DependsOn); err != nil {
				return fmt.Errorf("decoding %s: %w", key, err)
			}
		case "before":
			if err := val.Decode(&m.Before); err != nil {
				return fmt.Errorf("decoding before: %w", err)
			}
		case "vars":
			if err := val.Decode(&m.Vars); err != nil {
				return fmt.Errorf("decoding vars: %w", err)
			}
		case "resources":
			if val.Kind != yaml.SequenceNode {
				return fmt.Errorf("resources: expected sequence, got %v", val.Kind)
			}
			resources, err := FlattenResourceNodes(val.Content)
			if err != nil {
				return fmt.Errorf("flattening resources: %w", err)
			}
			m.Resources = resources
		}
	}

	return nil
}

// ModuleRef is a reference to a module in a role or node entry. It supports
// two YAML forms:
//
//	modules: [nginx]               # plain string -> ModuleRef{Name: "nginx"}
//	modules:
//	  - name: nginx                # object form with optional when expression
//	    when: "is_debian"
type ModuleRef struct {
	Name string `yaml:"name"`
	When string `yaml:"when,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ModuleRef. It handles two forms:
//   - Scalar string: "nginx" -> ModuleRef{Name: "nginx"}
//   - Mapping: {name: nginx, when: "is_debian"} -> decoded normally
func (m *ModuleRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		m.Name = value.Value
		return nil
	case yaml.MappingNode:
		// Use an alias to avoid infinite recursion in UnmarshalYAML.
		type moduleRefAlias ModuleRef
		var alias moduleRefAlias
		if err := value.Decode(&alias); err != nil {
			return err
		}
		*m = ModuleRef(alias)
		return nil
	default:
		return fmt.Errorf("ModuleRef: expected string or mapping, got %v", value.Kind)
	}
}

// RoleDefinition is a named list of modules loaded from config/roles/*.yaml.
type RoleDefinition struct {
	Name    string      `yaml:"name"`
	Modules []ModuleRef `yaml:"modules"`
}

// NodeEntry is a single entry in nodes.hnb. It has a custom UnmarshalYAML
// that merges duplicate 'modules' and 'roles' keys (standard YAML would
// silently keep only the last value).
type NodeEntry struct {
	Match       string                    `yaml:"match"`
	Environment string                    `yaml:"environment"`
	Roles       []string                  `yaml:"roles"`
	Modules     []ModuleRef               `yaml:"modules"`
	Vars        map[string]any            `yaml:"vars"`
	EnvVars     map[string]map[string]any `yaml:"env_vars"`
}

// UnmarshalYAML implements yaml.Unmarshaler for NodeEntry. It walks the
// mapping manually so duplicate 'modules' and 'roles' keys are merged
// instead of the last one winning.
func (n *NodeEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("NodeEntry: expected mapping, got %v", value.Kind)
	}
	for i := 0; i < len(value.Content)-1; i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]
		switch key {
		case "match":
			if err := val.Decode(&n.Match); err != nil {
				return fmt.Errorf("decoding match: %w", err)
			}
		case "environment":
			if err := val.Decode(&n.Environment); err != nil {
				return fmt.Errorf("decoding environment: %w", err)
			}
		case "roles":
			var extra []string
			if err := val.Decode(&extra); err != nil {
				return fmt.Errorf("decoding roles: %w", err)
			}
			n.Roles = append(n.Roles, extra...)
		case "modules":
			var extra []ModuleRef
			if err := val.Decode(&extra); err != nil {
				return fmt.Errorf("decoding modules: %w", err)
			}
			n.Modules = append(n.Modules, extra...)
		case "vars":
			if err := val.Decode(&n.Vars); err != nil {
				return fmt.Errorf("decoding vars: %w", err)
			}
		case "env_vars":
			if err := val.Decode(&n.EnvVars); err != nil {
				return fmt.Errorf("decoding env_vars: %w", err)
			}
		}
	}
	return nil
}

// NodesConfig is the top-level structure of nodes.hnb.
type NodesConfig struct {
	Nodes []NodeEntry `yaml:"nodes"`
}

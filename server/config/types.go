// Package config handles loading, watching, and resolving per-thrall
// configuration from YAML files on disk. It integrates with the secrets
// provider to resolve "secret:" prefixes and supports hot-reload via fsnotify.
package config

import (
	"fmt"

	"github.com/hobnob/hobnob/server/conditionals"
	"github.com/hobnob/hobnob/server/secrets"
	"gopkg.in/yaml.v3"
)

// FederationRole defines the server's role in a federated deployment.
type FederationRole string

const (
	// RoleStandalone is the default: a single server handling everything.
	RoleStandalone FederationRole = "standalone"
	// RoleHub is the central server in a federated deployment.
	// Handles UI, webhooks, bootstrap, and aggregates data from spokes.
	RoleHub FederationRole = "hub"
	// RoleSpoke is a partition server handling agent check-ins for a
	// subset of thralls. Disables webhooks, bootstrap, and secret watcher.
	RoleSpoke FederationRole = "spoke"
)

// FederationSection configures hub-spoke federation. Omit entirely
// (or set role: standalone) for single-server deployments.
type FederationSection struct {
	Role    FederationRole `yaml:"role"`     // standalone (default), hub, spoke
	SpokeID string         `yaml:"spoke_id"` // required for spoke mode
	HubAddr string         `yaml:"hub_addr"` // hub gRPC address (for future spoke→hub communication)
	Spokes  []SpokeEntry   `yaml:"spokes"`   // hub-only: spoke definitions
}

// SpokeEntry defines a spoke server the hub aggregates from.
type SpokeEntry struct {
	ID       string   `yaml:"id"`       // unique spoke identifier
	Addr     string   `yaml:"addr"`     // gRPC address (host:port)
	Patterns []string `yaml:"patterns"` // hostname glob patterns routed to this spoke
}

// Validate checks that federation config is internally consistent.
func (f FederationSection) Validate() error {
	role := f.Role
	if role == "" || role == RoleStandalone {
		return nil
	}
	if role == RoleHub {
		if len(f.Spokes) == 0 {
			return fmt.Errorf("federation.spokes is required when role is 'hub'")
		}
		for i, s := range f.Spokes {
			if s.ID == "" {
				return fmt.Errorf("federation.spokes[%d].id is required", i)
			}
			if s.Addr == "" {
				return fmt.Errorf("federation.spokes[%d].addr is required", i)
			}
		}
		return nil
	}
	if role == RoleSpoke {
		if f.SpokeID == "" {
			return fmt.Errorf("federation.spoke_id is required when role is 'spoke'")
		}
		return nil
	}
	return fmt.Errorf("federation.role must be standalone, hub, or spoke (got %q)", role)
}

// ServerConfig represents the top-level server configuration from server.yaml.
type ServerConfig struct {
	Server     ServerSection    `yaml:"server"`
	Database   DatabaseSection  `yaml:"database"`
	CheckIn    CheckInSection   `yaml:"checkin"`
	Watcher    WatcherSection   `yaml:"watcher"`
	Secrets    secrets.Config   `yaml:"secrets"`
	Bootstrap  BootstrapConfig  `yaml:"bootstrap"`
	Federation FederationSection `yaml:"federation"`
	Tuning     TuningSection    `yaml:"tuning"`
	SMTP       SMTPSection      `yaml:"smtp"`
}

// SMTPSection configures email notifications via an SMTP relay.
// Notifications are disabled when Host is empty.
type SMTPSection struct {
	Host       string   `yaml:"host"`                  // SMTP relay hostname (e.g. "smtp.example.com")
	Port       int      `yaml:"port"`                  // SMTP port (default 587)
	From       string   `yaml:"from"`                  // sender address (e.g. "hobnob@example.com")
	Username   string   `yaml:"username"`               // SMTP auth username (optional)
	Password   string   `yaml:"password"`               // SMTP auth password (supports secret: prefix)
	TLS        bool     `yaml:"tls"`                    // use STARTTLS (default true)
	Recipients []string `yaml:"recipients"`             // default recipient list
	Events     []string `yaml:"events"`                 // event types to send (default: all)
}

// TuningSection holds operational tuning parameters. All fields are optional
// and default to sensible values when omitted from server.yaml.
type TuningSection struct {
	SignatureWindow     string        `yaml:"signature_window"`      // default "5m"
	ThrallCacheTTL        string        `yaml:"thrall_cache_ttl"`      // default "60s"
	LastSeenFlush       string        `yaml:"last_seen_flush"`       // default "10s"
	TraitsPruneInterval string        `yaml:"traits_prune_interval"` // default "5m"
	MetricsInterval     string        `yaml:"metrics_interval"`      // default "30s"
	TraitsScannerTTL    string        `yaml:"traits_scanner_ttl"`    // default "30s"
	RunStore            string        `yaml:"run_store"`             // "database" (default) or "memory"
	RunStoreCapacity    int           `yaml:"run_store_capacity"`    // per-thrall run history depth for memory mode (default 20)
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
// Thralls matching the pattern and originating from an allowed CIDR are enrolled
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

// ThrallConfig represents the resolved per-thrall configuration.
// This is the output type returned by ConfigStore.Get().
type ThrallConfig struct {
	Thrall          string                    `yaml:"thrall"`
	MatchPattern    string                    `yaml:"-"` // original match pattern from thralls.hnb
	Environment     string                    `yaml:"environment"`
	Roles           []string                  `yaml:"-"` // role names that were expanded
	Modules         []ModuleConfig            `yaml:"modules"`
	Vars            map[string]any            `yaml:"vars"`
	VarOrder        []string                  `yaml:"-"`
	EnvVars         map[string]map[string]any `yaml:"env_vars"`
	ConditionalVars []conditionals.ConditionalVar `yaml:"-"`
}

// ModuleConfig describes a module assigned to a thrall.
type ModuleConfig struct {
	Name        string           `yaml:"name"`
	DependsOn   []string         `yaml:"depends_on"`
	When        string           `yaml:"when,omitempty"`
	Resources   []ResourceConfig `yaml:"resources"`
	ContentHash string           `yaml:"-"` // pre-computed at config load for delta transfer
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

// ModuleRef is a reference to a module in a role or thrall entry. It supports
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

// ThrallEntry is a single entry in thralls.hnb. It has a custom UnmarshalYAML
// that merges duplicate 'modules' and 'roles' keys (standard YAML would
// silently keep only the last value).
type ThrallEntry struct {
	Match       string                    `yaml:"match"`
	Environment string                    `yaml:"environment"`
	Roles       []string                  `yaml:"roles"`
	Modules     []ModuleRef               `yaml:"modules"`
	Vars        map[string]any            `yaml:"vars"`
	EnvVars     map[string]map[string]any `yaml:"env_vars"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ThrallEntry. It walks the
// mapping manually so duplicate 'modules' and 'roles' keys are merged
// instead of the last one winning.
func (n *ThrallEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("ThrallEntry: expected mapping, got %v", value.Kind)
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

// ThrallsConfig is the top-level structure of thralls.hnb.
type ThrallsConfig struct {
	Thralls []ThrallEntry `yaml:"thralls"`
}

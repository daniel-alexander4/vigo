// Package config handles loading, watching, and resolving per-client
// configuration from YAML files on disk. It integrates with the secrets
// provider to resolve "secret:" prefixes and supports hot-reload via fsnotify.
package config

import (
	"fmt"

	"github.com/isogov/isogov/server/conditionals"
	"gopkg.in/yaml.v3"
)

// StringOrSlice is a custom YAML type that accepts either a single string or
// a list of strings. This allows conditional fields to use shorthand:
//
//	go_if_all_pass: is_debian        # single string
//	go_if_all_pass: [is_debian, x]   # list form
type StringOrSlice []string

// UnmarshalYAML implements yaml.Unmarshaler for StringOrSlice.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	default:
		return fmt.Errorf("StringOrSlice: expected string or list, got %v", value.Kind)
	}
}

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

// ConditionalBlock groups resources under shared conditional fields.
// When flattened, block-level conditionals are merged onto each child resource.
type ConditionalBlock struct {
	GoIfAllPass   StringOrSlice    `yaml:"go_if_all_pass,omitempty"`
	GoIfAnyPass   StringOrSlice    `yaml:"go_if_any_pass,omitempty"`
	SkipIfAllPass StringOrSlice    `yaml:"skip_if_all_pass,omitempty"`
	SkipIfAnyPass StringOrSlice    `yaml:"skip_if_any_pass,omitempty"`
	Resources     []ResourceConfig `yaml:"resources"`
}

// ClientConfig represents the resolved per-client configuration.
// This is the output type returned by ConfigStore.Get().
type ClientConfig struct {
	Client          string                    `yaml:"client"`
	Environment     string                    `yaml:"environment"`
	Modules         []ModuleConfig            `yaml:"modules"`
	Vars            map[string]any            `yaml:"vars"`
	VarOrder        []string                  `yaml:"-"`
	EnvVars         map[string]map[string]any `yaml:"env_vars"`
	ConditionalVars []conditionals.ConditionalVar `yaml:"-"`
}

// ModuleConfig describes a module assigned to a client.
type ModuleConfig struct {
	Name      string           `yaml:"name"`
	DependsOn []string         `yaml:"depends_on"`
	Resources []ResourceConfig `yaml:"resources"`
}

// ResourceConfig describes a single resource within a module.
type ResourceConfig struct {
	Name          string        `yaml:"name"`
	Type          string        `yaml:"type"`
	GoIfAllPass   StringOrSlice `yaml:"go_if_all_pass,omitempty"`
	GoIfAnyPass   StringOrSlice `yaml:"go_if_any_pass,omitempty"`
	SkipIfAllPass StringOrSlice `yaml:"skip_if_all_pass,omitempty"`
	SkipIfAnyPass StringOrSlice `yaml:"skip_if_any_pass,omitempty"`
	Attributes    map[string]any `yaml:",inline"`
	Notify        []string       `yaml:"notify"`
	Subscribes    []string       `yaml:"subscribes"`
	DependsOn     []string       `yaml:"depends_on"`
}

// ModuleDefinition is a reusable module loaded from config/modules/*.yaml.
// It has a custom UnmarshalYAML that flattens conditional_block entries
// in the resources list into plain ResourceConfig items.
type ModuleDefinition struct {
	Name            string                        `yaml:"name"`
	DependsOn       []string                      `yaml:"depends_on"`
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
		case "depends_on":
			if err := val.Decode(&m.DependsOn); err != nil {
				return fmt.Errorf("decoding depends_on: %w", err)
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

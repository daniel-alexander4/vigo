package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Classifier assigns modules to clients based on hostname/IP glob patterns
// defined in classification.yaml. Rules are evaluated in order -- first match wins.
type Classifier struct {
	rules []ClassificationRule
}

// NewClassifier loads classification rules from the given YAML file.
// If the file does not exist, returns a classifier with no rules (not an error).
func NewClassifier(path string) (*Classifier, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Classifier{}, nil
		}
		return nil, fmt.Errorf("reading classification file: %w", err)
	}
	var cc ClassificationConfig
	if err := yaml.Unmarshal(data, &cc); err != nil {
		return nil, fmt.Errorf("parsing classification file: %w", err)
	}
	return &Classifier{rules: cc.Rules}, nil
}

// Classify returns a ClientConfig for the hostname if it matches a
// classification rule, or nil if no rule matches.
// First matching rule wins.
func (c *Classifier) Classify(hostname string) *ClientConfig {
	for _, rule := range c.rules {
		matched, err := filepath.Match(rule.Match, hostname)
		if err != nil {
			// Invalid pattern -- skip
			continue
		}
		if matched {
			return &ClientConfig{
				Client:      hostname,
				Environment: rule.Environment,
				Modules:     rule.Modules,
			}
		}
	}
	return nil
}

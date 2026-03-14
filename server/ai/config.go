// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"fmt"

	"github.com/hobnob/hobnob/server/config"
)

// Config is the validated AI configuration used by the assistant.
// It wraps config.AISection (the YAML parse type) and adds validation + defaults.
type Config struct {
	config.AISection
}

// ValidateConfig checks the AI configuration for internal consistency.
func ValidateConfig(c config.AISection) error {
	if !c.Enabled {
		return nil
	}
	switch c.Provider {
	case "ollama", "claude", "openai", "openai-compat", "":
		// ok
	default:
		return fmt.Errorf("ai.provider must be ollama, claude, openai, or openai-compat (got %q)", c.Provider)
	}
	if c.Provider == "openai-compat" && c.BaseURL == "" {
		return fmt.Errorf("ai.base_url is required when provider is openai-compat")
	}
	switch c.ContextMode {
	case "tools", "prefetch", "auto", "":
		// ok
	default:
		return fmt.Errorf("ai.context_mode must be tools, prefetch, or auto (got %q)", c.ContextMode)
	}
	return nil
}

// withDefaults returns a copy of the section with defaults applied.
func withDefaults(c config.AISection) config.AISection {
	if c.Provider == "" {
		c.Provider = "ollama"
	}
	if c.Model == "" {
		switch c.Provider {
		case "ollama":
			c.Model = "qwen2.5:7b"
		case "claude":
			c.Model = "claude-sonnet-4-20250514"
		case "openai":
			c.Model = "gpt-4o"
		}
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 4096
	}
	if c.MaxToolRounds == 0 {
		c.MaxToolRounds = 10
	}
	if c.ContextMode == "" {
		switch c.Provider {
		case "ollama":
			c.ContextMode = "auto"
		default:
			c.ContextMode = "tools"
		}
	}
	if c.BaseURL == "" && c.Provider == "ollama" {
		c.BaseURL = "http://ollama:11434/v1"
	}
	return c
}

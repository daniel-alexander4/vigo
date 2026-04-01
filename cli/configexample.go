// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"strings"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/spf13/cobra"
)

var serverConfigExampleCmd = &cobra.Command{
	Use:   "config-example",
	Short: "Print annotated server.yaml with all options, defaults, and descriptions",
	Long: `Print a fully annotated server.yaml reference showing every configuration
section with defaults, descriptions, and valid values.

Use --section to show only a specific section (case-insensitive partial match).`,
	Example: `  vigocli server config-example
  vigocli server config-example --section smtp
  vigocli server config-example --section ai
  vigocli server config-example > server.yaml`,
	RunE: runServerConfigExample,
}

func runServerConfigExample(cmd *cobra.Command, args []string) error {
	section, _ := cmd.Flags().GetString("section")

	content := config.ExampleServerConfig

	if section == "" {
		printColoredExample(content)
		return nil
	}

	// Extract the requested section.
	extracted, available := extractSection(content, section)
	if extracted == "" {
		return fmt.Errorf("section %q not found\n\nAvailable sections:\n  %s",
			section, strings.Join(available, "\n  "))
	}

	printColoredExample(extracted)
	return nil
}

// isSectionHeader returns true if the line is a section header like:
// # ── Section Name ──────...
func isSectionHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "# ── ") || strings.HasPrefix(trimmed, "# ╔")
}

// sectionName extracts the name from a header line like "# ── SMTP Notifications ───..."
func sectionName(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "# ── ") {
		return ""
	}
	// Strip prefix "# ── "
	rest := trimmed[len("# ── "):]
	// Find the closing " ──"
	idx := strings.Index(rest, " ──")
	if idx < 0 {
		return rest
	}
	return rest[:idx]
}

// matchesSection returns true if the query matches the section name.
// Matching priority: exact name, then word-prefix, then substring.
// This prevents "ai" from matching "Audit Trail" before "AI Assistant".
func matchesSection(name, query string) bool {
	lower := strings.ToLower(name)
	if lower == query {
		return true
	}
	for _, word := range strings.Fields(lower) {
		if strings.HasPrefix(word, query) {
			return true
		}
	}
	return false
}

// extractSection finds the section matching the query and returns its content.
// Also returns the list of available section names.
func extractSection(content, query string) (string, []string) {
	lines := strings.Split(content, "\n")
	query = strings.ToLower(query)

	var available []string
	var startIdx, endIdx int
	found := false

	// First pass: collect section names and try word-prefix matching.
	for i, line := range lines {
		name := sectionName(line)
		if name == "" {
			continue
		}
		available = append(available, name)
		if !found && matchesSection(name, query) {
			startIdx = i
			found = true
			continue
		}
		if found && isSectionHeader(line) {
			endIdx = i
			break
		}
	}

	// Fallback: substring match if word-prefix didn't find anything.
	if !found {
		for i, line := range lines {
			name := sectionName(line)
			if name == "" {
				continue
			}
			if strings.Contains(strings.ToLower(name), query) {
				startIdx = i
				found = true
				// Find the end of this section.
				for j := i + 1; j < len(lines); j++ {
					if isSectionHeader(lines[j]) {
						endIdx = j
						break
					}
				}
				break
			}
		}
	}

	if !found {
		return "", available
	}

	if endIdx == 0 {
		endIdx = len(lines)
	}

	// Trim trailing blank lines.
	for endIdx > startIdx && strings.TrimSpace(lines[endIdx-1]) == "" {
		endIdx--
	}

	return strings.Join(lines[startIdx:endIdx], "\n") + "\n", available
}

// printColoredExample prints the example config with section headers highlighted.
func printColoredExample(content string) {
	for _, line := range strings.Split(content, "\n") {
		if isSectionHeader(line) {
			fmt.Println(header(line))
		} else {
			fmt.Println(line)
		}
	}
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package ai

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LoadDocsCorpus reads all .md files from docsDir and concatenates them
// into a single string with section headers. Returns empty string if the
// directory doesn't exist or contains no markdown files.
func LoadDocsCorpus(docsDir string) string {
	info, err := os.Stat(docsDir)
	if err != nil || !info.IsDir() {
		return ""
	}

	var parts []string
	_ = filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(docsDir, path)
		parts = append(parts, "## "+rel+"\n\n"+string(raw))
		return nil
	})

	return strings.Join(parts, "\n\n---\n\n")
}

// CondenseDocsCorpus strips verbose content from a full docs corpus to
// produce a condensed version suitable for small-context LLMs.
// It keeps parameter tables, trait field tables, CLI flag tables, and
// concept overview paragraphs. Strips verbose examples (keeps only the
// first per section), Internals sections, and Troubleshooting sections.
func CondenseDocsCorpus(full string) string {
	sections := strings.Split(full, "\n\n---\n\n")
	var condensed []string

	for _, section := range sections {
		condensed = append(condensed, condenseSection(section))
	}

	return strings.Join(condensed, "\n\n---\n\n")
}

// condenseSection reduces a single doc section by keeping tables and
// first examples, dropping verbose content.
func condenseSection(section string) string {
	lines := strings.Split(section, "\n")
	var result []string
	exampleCount := 0
	skipUntilHeading := false

	dropHeadings := regexp.MustCompile(`^##+ (Internals|Troubleshooting|What Changed)`)
	exampleHeading := regexp.MustCompile(`^##+ .*[Ee]xample`)

	for _, line := range lines {
		// Check if we should stop skipping.
		if skipUntilHeading && strings.HasPrefix(line, "## ") {
			skipUntilHeading = false
		}
		if skipUntilHeading {
			continue
		}

		// Drop Internals/Troubleshooting sections.
		if dropHeadings.MatchString(line) {
			skipUntilHeading = true
			continue
		}

		// Keep only the first example block per section.
		if exampleHeading.MatchString(line) {
			exampleCount++
			if exampleCount > 1 {
				skipUntilHeading = true
				continue
			}
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

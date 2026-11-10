// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ai

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxDocsBytes caps the docs corpus to avoid exceeding LLM context limits.
// ~400 KB ≈ ~100K tokens, leaving headroom for system prompt, conversation
// history, tool results, and prefetched data within a 200K-token model.
const maxDocsBytes = 400 * 1024

// docsPriority controls which doc directories are loaded first.
// Higher-priority dirs are included before lower-priority ones when
// the corpus approaches the byte budget.
var docsPriority = []string{
	"concepts",
	"getting-started",
	"reference/executors",
	"reference/cli",
	"reference/traits",
	"reference/platforms",
	"examples",
	"operations",
	"api",
	"ui",
}

// LoadDocsCorpus reads .md files from docsDir and concatenates them into a
// single string with section headers. Files are loaded in priority order
// (concepts and reference first) and truncated at maxDocsBytes to stay
// within LLM context limits.
func LoadDocsCorpus(docsDir string) string {
	info, err := os.Stat(docsDir)
	if err != nil || !info.IsDir() {
		return ""
	}

	// Collect all .md files grouped by priority bucket.
	buckets := make(map[string][]string) // priority dir → list of abs paths
	var overflow []string                // files not matching any priority dir

	_ = filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(docsDir, path)
		// docs/internal.docs/ holds engineering and strategy memos — never feed them to the operator-facing AI.
		if rel == "internal.docs" || strings.HasPrefix(rel, "internal.docs"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		matched := false
		for _, prefix := range docsPriority {
			if strings.HasPrefix(rel, prefix+"/") || rel == prefix+".md" {
				buckets[prefix] = append(buckets[prefix], path)
				matched = true
				break
			}
		}
		if !matched {
			overflow = append(overflow, path)
		}
		return nil
	})

	var parts []string
	totalBytes := 0

	addFile := func(path string) bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return true // skip unreadable, keep going
		}
		rel, _ := filepath.Rel(docsDir, path)
		entry := "## " + rel + "\n\n" + string(raw)
		if totalBytes+len(entry) > maxDocsBytes {
			return false // budget exhausted
		}
		parts = append(parts, entry)
		totalBytes += len(entry)
		return true
	}

	// Load in priority order.
	for _, prefix := range docsPriority {
		for _, path := range buckets[prefix] {
			if !addFile(path) {
				return strings.Join(parts, "\n\n---\n\n")
			}
		}
	}
	// Then overflow files.
	for _, path := range overflow {
		if !addFile(path) {
			break
		}
	}

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

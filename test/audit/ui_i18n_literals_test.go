// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestUITemplatesNoEnglishLiterals guards the French i18n coverage of the web
// UI: every operator-visible string in ui/templates/ must go through the
// translation helpers ({{T}} / {{tEnum}} / {{tSafe}}) or come from a Go
// variable ({{.X}}) — never a bare English literal. It scans text nodes and
// the user-facing attributes (title / placeholder / aria-label / alt) of every
// template, strips template actions / comments / <script>/<style>/<code>/<pre>
// blocks / HTML entities, and fails on any remaining alphabetic word that is
// not an allowlisted coined noun or acronym.
//
// When it fails you have two honest choices, never a third:
//  1. The string is operator-visible prose → wrap it in {{T $.Locale "key"}}
//     (or {{tSafe}} if it carries trusted HTML) and add the key to BOTH
//     server/i18n/i18n/en.json and fr.json.
//  2. The word is a coined Vigo noun / product name / acronym that stays
//     English by design (like Gitback, Configcrate, HIPAA) → add it to
//     allowedWords below, with a comment if it's not obvious.
//
// Do NOT silence a real prose gap by allowlisting an ordinary English word.
func TestUITemplatesNoEnglishLiterals(t *testing.T) {
	root := repoRoot(t)
	tplDir := filepath.Join(root, "ui", "templates")

	var files []string
	err := filepath.Walk(tplDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".html") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", tplDir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no templates found under %s", tplDir)
	}

	type violation struct {
		file, snippet, word string
		line                int
	}
	var violations []violation

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		content := string(raw)
		// Blank out (length-preserving, so byte offsets still map to lines)
		// the regions that never hold translatable prose.
		for _, re := range blankRegions {
			content = re.ReplaceAllStringFunc(content, blankKeepNewlines)
		}
		rel, _ := filepath.Rel(root, f)

		check := func(candidate string, offset int) {
			for _, w := range offendingWords(candidate) {
				violations = append(violations, violation{
					file:    rel,
					line:    1 + strings.Count(content[:offset], "\n"),
					snippet: squish(candidate),
					word:    w,
				})
			}
		}

		// User-facing attribute values first, while values are still intact.
		for _, m := range reUserAttr.FindAllStringSubmatchIndex(content, -1) {
			check(content[m[2]:m[3]], m[2])
		}
		// Blank ALL attribute values so a stray '>' inside one (e.g.
		// hx-select="tbody > tr") can't split a text node. Length-preserving.
		content = reAttrVal.ReplaceAllStringFunc(content, blankKeepNewlines)
		// Text nodes: anything between a '>' and the next '<'.
		for _, m := range reTextNode.FindAllStringSubmatchIndex(content, -1) {
			check(content[m[2]:m[3]], m[2])
		}
	}

	if len(violations) == 0 {
		return
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})
	var b strings.Builder
	fmt.Fprintf(&b, "found %d unwrapped English literal(s) in ui/templates/ — wrap in {{T}}/{{tEnum}}/{{tSafe}} (and add keys to en.json+fr.json), or allowlist a genuine coined noun/acronym in ui_i18n_literals_test.go:\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(&b, "  %s:%d  word %q  in: %q\n", v.file, v.line, v.word, v.snippet)
	}
	t.Error(b.String())
}

var (
	reTextNode = regexp.MustCompile(`>([^<]*)<`)
	reUserAttr = regexp.MustCompile(`(?i)(?:title|placeholder|aria-label|alt)\s*=\s*"([^"]*)"`)
	reEntity   = regexp.MustCompile(`&#?[0-9a-zA-Z]+;`)
	reFilename = regexp.MustCompile(`[\w.-]*\.(?:vgo|ya?ml|json|html?|md|sh|toml|go|txt|csv|pdf|sock)\b|\b\w+_\w+\b`)
	reURLPath  = regexp.MustCompile(`/[\w./-]+`) // route shown as link text, e.g. <a href="/risk">/risk</a>
	reAttrVal  = regexp.MustCompile(`=\s*"[^"]*"|=\s*'[^']*'`)
	reWord     = regexp.MustCompile(`[A-Za-z]+(?:['’\-][A-Za-z]+)*`)

	// Length-preserving blanking, applied in order before extraction so byte
	// offsets still map to line numbers. Template actions go LAST so their
	// inner quotes/operators can't confuse the HTML-shaped extraction above.
	blankRegions = []*regexp.Regexp{
		regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`), // {{/* template comment */}}
		regexp.MustCompile(`(?s)<!--.*?-->`),        // <!-- html comment -->
		regexp.MustCompile(`(?s)<script.*?</script>`),
		regexp.MustCompile(`(?s)<style.*?</style>`),
		regexp.MustCompile(`(?s)<code[^>]*>.*?</code>`), // identifiers / filenames / commands
		regexp.MustCompile(`(?s)<pre[^>]*>.*?</pre>`),
		regexp.MustCompile(`(?s)\{\{.*?\}\}`), // {{T}} / {{.X}} / {{range}} … — dynamic or already-translated
	}
)

// offendingWords returns the alphabetic words in candidate that look like
// untranslated English prose. Template actions are already blanked from the
// content before extraction (so a {{T}}/{{.X}}-wrapped string arrives empty);
// here we additionally drop HTML entities and dotted filenames/config-keywords
// (common.vgo, server.yaml, depends_on) before checking acronyms and the
// coined-noun allowlist.
func offendingWords(candidate string) []string {
	s := reEntity.ReplaceAllString(candidate, " ")
	s = reURLPath.ReplaceAllString(s, " ")
	s = reFilename.ReplaceAllString(s, " ")
	var out []string
	for _, w := range reWord.FindAllString(s, -1) {
		core := strings.Trim(w, "'’-")
		if len([]rune(core)) < 2 {
			continue // single letters, stray punctuation
		}
		if core == strings.ToUpper(core) {
			continue // acronym / code token (CVE, HIPAA, SHA, OS, AI, …)
		}
		if allowedWords[strings.ToLower(core)] {
			continue
		}
		out = append(out, core)
	}
	return out
}

func blankKeepNewlines(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

func squish(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// allowedWords are tokens that may appear as bare (unwrapped) UI text by
// design: coined Vigo nouns and product/brand names that are NOT translated
// into French (they read identically in the French catalog). Acronyms are
// already skipped by the all-uppercase rule, so they don't need listing.
// Keep this list small — an ordinary English word here is a coverage hole in
// disguise.
var allowedWords = map[string]bool{
	// Coined Vigo nouns / subsystem names (kept English in French too).
	"vigo":         true,
	"envoy":        true, // the Vigo term for a managed node; identical in fr
	"envoys":       true,
	"gitback":      true,
	"longdrawer":   true,
	"lockbox":      true,
	"filecast":     true,
	"curator":      true,
	"poolq":        true,
	"puddle":       true,
	"puddles":      true,
	"spanner":      true,
	"swarm":        true,
	"configcrate":  true,
	"configcrates": true,
	"usercrate":    true,
	"usercrates":   true,
	"hostcrate":    true,
	"hostcrates":   true,
	"connwaer":     true,
	"scrier":       true,
	"sandgorgon":   true,
	"vigosrv":      true,
	"vigocli":      true,
	"bolt":         true, // spanner peer (coined Vigo noun)

	// Config / DAG vocabulary echoing literal .vgo keys — kept English by
	// the i18n doctrine (config keys stay English), shown as trace labels.
	"match":      true,
	"roles":      true,
	"secret":     true,
	"notify":     true,
	"subscribes": true,
	"before":     true,

	// Units and standard technical tokens (never translated).
	"ms":     true,
	"stdout": true,
	"stderr": true,

	// Third-party security-scanner names (proper nouns surfaced on /security).
	"rkhunter":   true,
	"chkrootkit": true,
	"trivy":      true,
	"debsecan":   true,
	"clamav":     true,
	"aide":       true,
	"lynis":      true,
}

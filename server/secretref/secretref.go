// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package secretref is the single source of truth for the `secret:<key>`
// reference grammar: how a reference is found inside a value (Scan) and
// which keys are well formed (ValidateKey). The config loader, modlint and
// the local secrets provider all use it, so a key the linter accepts is a
// key the loader resolves and the provider can store. Stdlib-only leaf, so
// modlint can import it without pulling in server dependencies.
package secretref

import (
	"fmt"
	"strings"
)

// Prefix introduces a secret reference inside a config value.
const Prefix = "secret:"

// Span is one `secret:` reference located inside a value: the byte range
// it occupies and the key it names (empty for a bare `secret:`).
type Span struct {
	Start int    // index of "secret:"
	End   int    // index just past the key
	Key   string // the secret path ("" for a bare "secret:")
}

// IsKeyChar reports whether c can appear inside a secret key during
// tokenization: ASCII alphanumerics plus `.` `_` `-` `/`. Any other byte
// ends the key. Uppercase tokenizes so that `secret:Foo` is reported as an
// invalid key by ValidateKey rather than silently read as literal text.
// This is an allowlist on purpose: a denylist of separators cannot tell a
// real reference from the word "secret:" in prose, and would swallow
// trailing punctuation into a bogus key.
func IsKeyChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '_', c == '-', c == '/':
		return true
	}
	return false
}

// Scan returns every `secret:` occurrence in str, embedded and compound
// references included (`PW=secret:vigo/db/pw,DB=app`).
func Scan(str string) []Span {
	if !strings.Contains(str, Prefix) {
		return nil
	}
	var spans []Span
	i := 0
	for i < len(str) {
		rel := strings.Index(str[i:], Prefix)
		if rel < 0 {
			break
		}
		start := i + rel
		keyStart := start + len(Prefix)
		keyEnd := keyStart
		for keyEnd < len(str) && IsKeyChar(str[keyEnd]) {
			keyEnd++
		}
		spans = append(spans, Span{Start: start, End: keyEnd, Key: str[keyStart:keyEnd]})
		if keyEnd == keyStart {
			i = keyStart // bare `secret:`; step past the prefix
		} else {
			i = keyEnd
		}
	}
	return spans
}

// ValidateKey reports whether key is a well-formed secret path: one or more
// `/`-separated components, each made of lowercase ASCII letters, digits,
// `_`, `-` and `.`, none empty and none starting with `.`. That last rule
// rejects `..` traversal and hidden names such as the local backend's
// `.master.key`.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("secret key is empty")
	}
	for _, comp := range strings.Split(key, "/") {
		if comp == "" {
			return fmt.Errorf("secret key %q has an empty path component", key)
		}
		if comp[0] == '.' {
			return fmt.Errorf("secret key %q has a component starting with '.' (no '..' or hidden names)", key)
		}
		for i := 0; i < len(comp); i++ {
			c := comp[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
			if !ok {
				return fmt.Errorf("secret key %q contains %q (allowed: lowercase letters, digits, '_', '-', '.', '/')", key, string(c))
			}
		}
	}
	return nil
}

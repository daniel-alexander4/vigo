// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"reflect"
	"strings"
)

// ExampleSectionYAML returns an annotated YAML starter template for the given
// top-level server.yaml section. It is rendered on the Server Config detail
// page when the section is absent from the live config, so an operator can
// copy-paste a working shape into server.yaml.
//
// Rules:
//   - The section header is always uncommented.
//   - Scalar fields with a `default:` tag render commented-out with the default
//     as the value (optional — leave uncommented for the default).
//   - Scalar fields without a default render uncommented with a starter value
//     drawn from the `example:` tag (required — must be set).
//   - Slice-of-struct fields render one entry with the same rules applied to
//     each inner field, uncommented at the entry level when the slice has no
//     default.
//   - Sub-sections (nested struct fields) render commented-out — they are
//     opt-in feature blocks (integrations.*, auth.oidc) that the operator
//     uncomments to enable.
//   - Every field and subsection is preceded by a `# ` comment containing its
//     description from the `doc:` tag, wrapped at ~78 columns.
//
// Returns "" if the named section does not exist in ServerConfig.
func ExampleSectionYAML(name string) string {
	t := reflect.TypeOf(ServerConfig{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if yamlFieldName(f) != name {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s:\n", name)
		renderStructBody(&b, unwrapType(f.Type), "  ", false)
		return strings.TrimRight(b.String(), "\n") + "\n"
	}
	return ""
}

// renderStructBody writes every field of a struct type as annotated YAML into
// b. When commentedParent is true, every line emitted is prefixed with `# ` so
// a whole sub-section renders as a commented block (the operator uncomments
// the whole block to enable it).
func renderStructBody(b *strings.Builder, t reflect.Type, indent string, commentedParent bool) {
	if t.Kind() != reflect.Struct {
		return
	}
	first := true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := yamlFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		first = false
		renderFieldExample(b, f, name, indent, commentedParent)
	}
}

// renderFieldExample writes a single field as annotated YAML. The field may be
// a scalar, list, map, slice-of-struct, or nested struct (sub-section).
func renderFieldExample(b *strings.Builder, f reflect.StructField, name, indent string, commentedParent bool) {
	doc := f.Tag.Get("doc")
	def := f.Tag.Get("default")
	example := f.Tag.Get("example")

	ft := unwrapType(f.Type)

	// Inside an already-commented sub-section, per-field description comments
	// become `# # description` which reads as clutter. Drop them — the top of
	// the sub-section has its own description, and the full options reference
	// is rendered in the definition list below the example.
	if doc != "" && !commentedParent {
		writeWrappedComment(b, doc, indent)
	}

	// Sub-section: commented-out header with commented interior.
	if ft.Kind() == reflect.Struct {
		writeLine(b, fmt.Sprintf("%s:", name), indent, true)
		renderStructBody(b, ft, indent+"  ", true)
		return
	}

	// Slice-of-struct: render one placeholder entry.
	if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
		entryCommented := commentedParent || def != ""
		writeLine(b, fmt.Sprintf("%s:", name), indent, entryCommented)
		renderSliceEntry(b, ft.Elem(), indent+"  ", entryCommented)
		return
	}

	// Scalar, list-of-scalar, or map.
	val := starterValue(def, example, ft)
	lineCommented := commentedParent || def != ""
	trailing := ""
	if def != "" {
		trailing = "  # (default: " + def + ")"
	}
	writeLine(b, fmt.Sprintf("%s: %s%s", name, val, trailing), indent, lineCommented)
}

// renderSliceEntry emits a single entry of a slice-of-struct field. The first
// inner-field line gets the `- ` YAML list marker; subsequent lines align
// under it with two extra spaces of indent.
func renderSliceEntry(b *strings.Builder, t reflect.Type, indent string, commented bool) {
	if t.Kind() != reflect.Struct {
		return
	}
	first := true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := yamlFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		doc := f.Tag.Get("doc")
		def := f.Tag.Get("default")
		example := f.Tag.Get("example")
		ft := unwrapType(f.Type)
		if !first {
			b.WriteString("\n")
		}

		fieldIndent := indent
		linePrefix := ""
		if first {
			linePrefix = "- "
		} else {
			fieldIndent = indent + "  "
		}

		if doc != "" && !commented {
			writeWrappedComment(b, doc, fieldIndent)
		}
		val := starterValue(def, example, ft)
		trailing := ""
		if def != "" {
			trailing = "  # (default: " + def + ")"
		}
		lineCommented := commented || def != ""
		writeLine(b, fmt.Sprintf("%s%s: %s%s", linePrefix, name, val, trailing), fieldIndent, lineCommented)
		first = false
	}
}

// starterValue picks a YAML-safe starter value for a scalar or list-of-scalar
// field. Prefers the default, then the first example token, then a type-
// appropriate zero value.
func starterValue(def, example string, t reflect.Type) string {
	if def != "" {
		return def
	}
	if example != "" {
		trimmed := strings.TrimSpace(example)
		if strings.HasPrefix(trimmed, "[") {
			// Already in YAML flow-list form — use as-is.
			return trimmed
		}
		first := strings.SplitN(example, ",", 2)[0]
		first = strings.TrimSpace(first)
		if idx := strings.Index(first, " ("); idx >= 0 {
			first = first[:idx]
		}
		if first != "" {
			return first
		}
	}
	switch t.Kind() {
	case reflect.String:
		return `""`
	case reflect.Bool:
		return "false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "0"
	case reflect.Float32, reflect.Float64:
		return "0"
	case reflect.Slice, reflect.Array:
		return "[]"
	case reflect.Map:
		return "{}"
	}
	return `""`
}

// writeWrappedComment emits the given text as a `# ` comment, wrapping each
// physical line at roughly 78 columns minus the indent.
func writeWrappedComment(b *strings.Builder, text, indent string) {
	prefix := indent + "# "
	width := 78 - len(prefix)
	if width < 20 {
		width = 20
	}
	words := strings.Fields(text)
	var line strings.Builder
	for _, w := range words {
		if line.Len() > 0 && line.Len()+1+len(w) > width {
			b.WriteString(prefix + line.String() + "\n")
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(w)
	}
	if line.Len() > 0 {
		b.WriteString(prefix + line.String() + "\n")
	}
}

// writeLine writes a single indented YAML line, optionally commented.
func writeLine(b *strings.Builder, text, indent string, commented bool) {
	if commented {
		b.WriteString(indent + "# " + text + "\n")
	} else {
		b.WriteString(indent + text + "\n")
	}
}

// unwrapType strips pointer layers to return the pointee type.
func unwrapType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

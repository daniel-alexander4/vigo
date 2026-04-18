// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"reflect"
	"strings"
	"sync"
)

// Schema describes one top-level section of server.yaml (e.g. "auth", "swarm"),
// its scalar/list/map fields, and any nested sub-sections (e.g. auth.oidc, or
// integrations.slack). It is derived at runtime via reflection over the
// ServerConfig struct so that the "doc:", "options:", and "default:" struct
// tags are the single source of truth for the Server Config page.
type SchemaSection struct {
	Name        string          // yaml key (e.g. "swarm", "slack", "oidc")
	GoName      string          // Go field name on the parent struct
	Description string          // from the `doc:` tag on the containing field
	Fields      []SchemaField   // scalar / list / map leaf fields
	Subsections []SchemaSection // nested struct fields (e.g. auth.oidc)
	IsPointer   bool            // true when the field is a pointer-to-struct (integrations.*)
}

// SchemaField describes a single scalar, list, or map field on a section.
type SchemaField struct {
	Name        string // yaml key
	GoName      string // Go field name
	GoType      string // human-readable type string ("string", "bool", "int", "[]string", "map[string]string")
	Kind        string // "scalar" | "list" | "map"
	Description string // from the `doc:` tag
	Options     string // pipe-separated enum values from the `options:` tag (may be empty)
	Default     string // from the `default:` tag (may be empty)
}

var (
	schemaOnce  sync.Once
	schemaCache []SchemaSection
)

// ServerConfigSchema returns the top-level sections of server.yaml. The slice
// is computed once on first call and cached for the process lifetime — the
// shape of ServerConfig is fixed at compile time, so no invalidation is needed.
func ServerConfigSchema() []SchemaSection {
	schemaOnce.Do(func() {
		schemaCache = buildServerConfigSchema()
	})
	return schemaCache
}

func buildServerConfigSchema() []SchemaSection {
	t := reflect.TypeOf(ServerConfig{})
	sections := make([]SchemaSection, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := yamlFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		sec := buildSection(f, name)
		sections = append(sections, sec)
	}
	return sections
}

// buildSection turns a struct field whose type is a struct (or pointer to
// struct) into a SchemaSection, recursing into its own fields.
func buildSection(f reflect.StructField, yamlName string) SchemaSection {
	sec := SchemaSection{
		Name:        yamlName,
		GoName:      f.Name,
		Description: f.Tag.Get("doc"),
	}

	inner, isPointer := unwrapStructType(f.Type)
	sec.IsPointer = isPointer
	if inner == nil || inner.Kind() != reflect.Struct {
		// Not a struct: treat as a leaf with no fields. Should not happen
		// for top-level ServerConfig fields but kept defensive.
		return sec
	}

	for i := 0; i < inner.NumField(); i++ {
		cf := inner.Field(i)
		if !cf.IsExported() {
			continue
		}
		cname := yamlFieldName(cf)
		if cname == "" || cname == "-" {
			continue
		}

		ct, _ := unwrapStructType(cf.Type)
		if ct != nil && ct.Kind() == reflect.Struct {
			sec.Subsections = append(sec.Subsections, buildSection(cf, cname))
			continue
		}
		sec.Fields = append(sec.Fields, buildField(cf, cname))
	}
	return sec
}

// buildField turns a scalar / slice / map struct field into a SchemaField.
func buildField(f reflect.StructField, yamlName string) SchemaField {
	t := f.Type
	// Unwrap pointer-to-scalar (e.g. *bool for StreamEditSection.Enabled).
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	kind := "scalar"
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		kind = "list"
	case reflect.Map:
		kind = "map"
	}

	return SchemaField{
		Name:        yamlName,
		GoName:      f.Name,
		GoType:      humanType(f.Type),
		Kind:        kind,
		Description: f.Tag.Get("doc"),
		Options:     f.Tag.Get("options"),
		Default:     f.Tag.Get("default"),
	}
}

// unwrapStructType returns (structType, isPointer) for a struct or
// pointer-to-struct type. Returns (nil, _) for non-struct types.
func unwrapStructType(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() == reflect.Ptr {
		if t.Elem().Kind() == reflect.Struct {
			return t.Elem(), true
		}
		return nil, true
	}
	if t.Kind() == reflect.Struct {
		return t, false
	}
	return nil, false
}

// yamlFieldName returns the yaml key for a struct field, stripping ",omitempty"
// and other options. Returns "" if the field has no yaml tag (not exposed to
// YAML) and "-" if the tag explicitly omits the field.
func yamlFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		return ""
	}
	if idx := strings.Index(tag, ","); idx >= 0 {
		tag = tag[:idx]
	}
	return tag
}

// humanType renders a reflect.Type as a short human-readable string suitable
// for display in the config UI. Shows pointer-to-struct as the inner type
// (the pointer is an implementation detail of "optional section").
func humanType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		return humanType(t.Elem())
	case reflect.Slice, reflect.Array:
		return "[]" + humanType(t.Elem())
	case reflect.Map:
		return "map[" + humanType(t.Key()) + "]" + humanType(t.Elem())
	case reflect.Struct:
		return t.Name()
	default:
		return t.Kind().String()
	}
}

// FindSchemaSection returns the top-level section with the given yaml name, or
// nil if no such section exists. Used by the UI detail handler to validate the
// {section} path parameter.
func FindSchemaSection(name string) *SchemaSection {
	for i, s := range ServerConfigSchema() {
		if s.Name == name {
			return &ServerConfigSchema()[i]
		}
	}
	return nil
}

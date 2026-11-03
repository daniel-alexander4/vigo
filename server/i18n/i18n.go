// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package i18n is the shared localization catalog and per-request locale
// resolution for Vigo's web surfaces. It lives outside the ui package so
// server/auth (the login/TOTP pages) can localize too: ui imports
// server/auth, so the catalog can't live in ui without an import cycle.
//
// The lookup is a plain locale→key→string map, not a printf-style message
// formatter: UI chrome is static keyed strings (counts/values are rendered
// separately), so message-formatter semantics — where a literal '%' in a
// translated string becomes a format directive — would be a foot-gun for no
// benefit. CLDR-aware date/number formatting is a separate, later concern.
//
// Catalogs are embedded JSON authored as flat {"key": "text"} objects. The
// English catalog is the source of truth and the fallback: a key missing from
// a non-default locale resolves to its English value, and a key missing
// everywhere resolves to the key itself (loud, not blank).
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/config"
)

const (
	// DefaultLocaleFallback is the source language and last-resort fallback.
	DefaultLocaleFallback = "en"
	// LocaleCookieName is the persistent per-operator language choice set by
	// the topbar picker (a display preference, like the theme toggle).
	LocaleCookieName = "vigo_lang"
)

//go:embed i18n/*.json
var catalogFiles embed.FS

// catalogs maps locale → key → translated string. Populated once at init and
// never mutated, so it's safe to read concurrently without locking.
var catalogs = map[string]map[string]string{}

// localeNames maps a locale code to its endonym for the picker label.
var localeNames = map[string]string{
	"en": "English",
	"fr": "Français",
	"de": "Deutsch",
	"es": "Español",
	"pl": "Polski",
	"pt": "Português",
	"it": "Italiano",
}

func init() {
	entries, err := catalogFiles.ReadDir("i18n")
	if err != nil {
		panic(fmt.Sprintf("i18n: reading embedded catalogs: %v", err))
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		raw, err := catalogFiles.ReadFile("i18n/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("i18n: reading catalog %s: %v", e.Name(), err))
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("i18n: parsing catalog %s: %v", e.Name(), err))
		}
		catalogs[locale] = m
	}
}

// LocaleStr coerces a template-supplied locale argument to a string. The
// locale arrives as $.Locale — a string on PageData-backed renders but nil
// when a partial is invoked with a dict (a map with no "Locale" key) or any
// data lacking the field — coercing to "" there yields the English fallback
// instead of a render error.
func LocaleStr(v any) string {
	s, _ := v.(string)
	return s
}

// CatalogT resolves a translation key for a locale, falling back to the
// English (source) value and finally to the key itself. Registered in the ui
// template FuncMap as "T"; templates call {{T $.Locale "some.key"}} — $.Locale
// (root scope) so it stays correct inside {{range}}/{{with}} blocks.
func CatalogT(localeArg any, key string) string {
	locale := LocaleStr(localeArg)
	if m, ok := catalogs[locale]; ok {
		if s, ok := m[key]; ok && s != "" {
			return s
		}
	}
	if m, ok := catalogs[DefaultLocaleFallback]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	return key
}

// CatalogTSafe is the FuncMap entry "tSafe": like CatalogT but returns the
// resolved translation as template.HTML, so authored catalog punctuation
// (apostrophes, ampersands, em-dashes) renders literally rather than being
// re-escaped. Catalog values are trusted, author-controlled chrome — never
// user data. Use only when a translated string is emitted directly into HTML
// text content; CatalogT (plain string) is the default and is required
// wherever the value feeds a string-typed helper.
func CatalogTSafe(localeArg any, key string) template.HTML {
	return template.HTML(CatalogT(localeArg, key)) //nolint:gosec // trusted authored catalog strings, not user input
}

// LocaleChoice is one option in the topbar language picker.
type LocaleChoice struct {
	Code  string // e.g. "fr"
	Label string // endonym, e.g. "Français"
}

// SupportedLocales returns the effective locale set: the configured list when
// present, otherwise every locale with a bundled catalog — restricted to
// locales that actually have a catalog so the picker can't offer an empty one.
func SupportedLocales(cfg config.I18nSection) []string {
	want := cfg.SupportedLocales
	if len(want) == 0 {
		want = make([]string, 0, len(catalogs))
		for loc := range catalogs {
			want = append(want, loc)
		}
	}
	out := make([]string, 0, len(want))
	for _, loc := range want {
		if _, ok := catalogs[loc]; ok {
			out = append(out, loc)
		}
	}
	sort.Strings(out)
	return out
}

// LocaleSupported reports whether loc is in the effective supported set.
func LocaleSupported(cfg config.I18nSection, loc string) bool {
	for _, s := range SupportedLocales(cfg) {
		if s == loc {
			return true
		}
	}
	return false
}

// DefaultLocale is the configured default, or "en".
func DefaultLocale(cfg config.I18nSection) string {
	if cfg.DefaultLocale != "" {
		return cfg.DefaultLocale
	}
	return DefaultLocaleFallback
}

// LocaleChoices returns the picker options when i18n is enabled and more than
// one locale is available; otherwise nil (the topbar hides the picker).
func LocaleChoices(cfg config.I18nSection) []LocaleChoice {
	if !cfg.Enabled {
		return nil
	}
	locs := SupportedLocales(cfg)
	if len(locs) < 2 {
		return nil
	}
	out := make([]LocaleChoice, 0, len(locs))
	for _, loc := range locs {
		label := localeNames[loc]
		if label == "" {
			label = loc
		}
		out = append(out, LocaleChoice{Code: loc, Label: label})
	}
	return out
}

// LocaleFromRequest resolves the locale for a request: saved cookie >
// Accept-Language > configured default. The cookie is set client-side by the
// topbar picker. When i18n is disabled it always returns the default, so the
// UI renders English with no per-request work beyond this short-circuit. Used
// by every web surface that renders localized content — ui pages/fragments,
// the docs handler, and the server/auth login pages (which have no user yet,
// so cookie + Accept-Language are the only signals).
func LocaleFromRequest(cfg config.I18nSection, r *http.Request) string {
	def := DefaultLocale(cfg)
	if !cfg.Enabled {
		return def
	}
	if c, err := r.Cookie(LocaleCookieName); err == nil && LocaleSupported(cfg, c.Value) {
		return c.Value
	}
	if loc := MatchAcceptLanguage(cfg, r.Header.Get("Accept-Language")); loc != "" {
		return loc
	}
	return def
}

// MatchAcceptLanguage returns the first supported locale named in an
// Accept-Language header, matched on the primary subtag (so "fr-CA" matches
// "fr"). Header order is treated as preference order — sufficient for the
// small supported set; full q-value weighting isn't worth the complexity.
func MatchAcceptLanguage(cfg config.I18nSection, header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if i := strings.IndexByte(tag, ';'); i >= 0 {
			tag = tag[:i]
		}
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		primary := tag
		if i := strings.IndexByte(primary, '-'); i >= 0 {
			primary = primary[:i]
		}
		primary = strings.ToLower(primary)
		if LocaleSupported(cfg, primary) {
			return primary
		}
	}
	return ""
}

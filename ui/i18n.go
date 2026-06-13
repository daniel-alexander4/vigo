// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

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

// i18n: key-based UI translation with English fallback.
//
// The lookup is a plain locale→key→string map, not a printf-style message
// formatter: UI chrome is static keyed strings (counts and values are
// rendered separately in templates), so message-formatter semantics — where
// a literal '%' in a translated string becomes a format directive — would be
// a foot-gun for no benefit. CLDR-aware date/number *formatting* is a
// separate, later concern (golang.org/x/text/message) and does not belong on
// this lookup path.
//
// Catalogs are embedded JSON authored as flat {"key": "text"} objects. The
// English catalog is the source of truth and the fallback: any key missing
// from a non-default locale resolves to its English value, and any key
// missing everywhere resolves to the key itself (loud, not blank).

const (
	defaultLocaleFallback = "en"
	localeCookieName      = "vigo_lang"
)

//go:embed i18n/*.json
var i18nCatalogFiles embed.FS

// catalogs maps locale → key → translated string. Populated once at init
// from the embedded JSON files and never mutated, so it's safe to read
// concurrently from the template FuncMap without locking.
var catalogs = map[string]map[string]string{}

// localeNames maps a locale code to its endonym for the picker label.
// A locale with a bundled catalog but no entry here falls back to the code.
var localeNames = map[string]string{
	"en": "English",
	"fr": "Français",
}

func init() {
	entries, err := i18nCatalogFiles.ReadDir("i18n")
	if err != nil {
		panic(fmt.Sprintf("ui: reading embedded i18n catalogs: %v", err))
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		raw, err := i18nCatalogFiles.ReadFile("i18n/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("ui: reading i18n catalog %s: %v", e.Name(), err))
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("ui: parsing i18n catalog %s: %v", e.Name(), err))
		}
		catalogs[locale] = m
	}
}

// localeStr coerces a template-supplied locale argument to a string. The
// locale arrives as $.Locale, which is a string on PageData-backed renders
// but nil when a partial is invoked with a dict (a map with no "Locale" key)
// or any data lacking the field — coercing to "" there yields the English
// fallback instead of a render error.
func localeStr(v any) string {
	s, _ := v.(string)
	return s
}

// catalogT resolves a translation key for a locale, falling back to the
// English (source) value and finally to the key itself. Registered in the
// template FuncMap as "T"; templates call {{T $.Locale "some.key"}} — using
// $.Locale (root scope) so it stays correct inside {{range}}/{{with}} blocks.
func catalogT(localeArg any, key string) string {
	locale := localeStr(localeArg)
	if m, ok := catalogs[locale]; ok {
		if s, ok := m[key]; ok && s != "" {
			return s
		}
	}
	if m, ok := catalogs[defaultLocaleFallback]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	return key
}

// catalogTSafe is the FuncMap entry "tSafe": like "T" but returns the
// resolved translation as template.HTML, so authored catalog punctuation
// (apostrophes, ampersands, em-dashes) renders literally rather than being
// re-escaped by html/template. Catalog values are trusted, author-controlled
// chrome — never user data. Use {{tSafe}} only when a translated string is
// emitted directly into HTML text content; {{T}} (plain string) remains the
// default and is required wherever the value feeds a string-typed helper
// (sortHeader, printf format, dict label, …).
func catalogTSafe(localeArg any, key string) template.HTML {
	return template.HTML(catalogT(localeArg, key)) //nolint:gosec // trusted authored catalog strings, not user input
}

// LocaleChoice is one option in the topbar language picker.
type LocaleChoice struct {
	Code  string // e.g. "fr"
	Label string // endonym, e.g. "Français"
}

// SetI18n wires the localization config into the handler. Mirrors
// SetBranding — called once at startup from server.go.
func (h *Handler) SetI18n(cfg config.I18nSection) {
	h.i18n = cfg
}

// supportedLocales returns the effective locale set: the configured list
// when present, otherwise every locale with a bundled catalog. The result
// is restricted to locales that actually have a catalog so the picker can't
// offer a locale that renders empty.
func (h *Handler) supportedLocales() []string {
	want := h.i18n.SupportedLocales
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

// localeSupported reports whether loc is in the effective supported set.
func (h *Handler) localeSupported(loc string) bool {
	for _, s := range h.supportedLocales() {
		if s == loc {
			return true
		}
	}
	return false
}

// defaultLocale is the configured default, or "en".
func (h *Handler) defaultLocale() string {
	if h.i18n.DefaultLocale != "" {
		return h.i18n.DefaultLocale
	}
	return defaultLocaleFallback
}

// localeChoices returns the picker options when i18n is enabled and more
// than one locale is available; otherwise nil (the topbar hides the picker).
func (h *Handler) localeChoices() []LocaleChoice {
	if !h.i18n.Enabled {
		return nil
	}
	locs := h.supportedLocales()
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

// localeFromRequest resolves the locale for a request: saved cookie >
// Accept-Language > configured default. The cookie is set client-side by
// the topbar picker (a display preference, like the theme toggle — the UI
// is observability-only, so there's no server write path). When i18n is
// disabled it always returns the default, so the UI renders English with no
// per-request work beyond this short-circuit. This is the single resolution
// point — every full page (and the docs handler) routes through
// setCommonWithRequest, which calls this; no middleware is needed.
// LocaleFromRequest is the exported wrapper around localeFromRequest, used
// by server/api report builders (which import ui) to stamp the resolved
// locale onto report payloads rendered through ui fragment templates.
func (h *Handler) LocaleFromRequest(r *http.Request) string {
	return h.localeFromRequest(r)
}

func (h *Handler) localeFromRequest(r *http.Request) string {
	def := h.defaultLocale()
	if !h.i18n.Enabled {
		return def
	}
	if c, err := r.Cookie(localeCookieName); err == nil && h.localeSupported(c.Value) {
		return c.Value
	}
	if loc := h.matchAcceptLanguage(r.Header.Get("Accept-Language")); loc != "" {
		return loc
	}
	return def
}

// matchAcceptLanguage returns the first supported locale named in an
// Accept-Language header, matched on the primary subtag (so "fr-CA" matches
// "fr"). Header order is treated as preference order — sufficient for the
// small supported set; full q-value weighting isn't worth the complexity.
func (h *Handler) matchAcceptLanguage(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		// Strip any ";q=..." weight and whitespace.
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
		if h.localeSupported(primary) {
			return primary
		}
	}
	return ""
}

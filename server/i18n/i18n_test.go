// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/config"
)

// TestCatalogCompleteness is the drift guard for the chrome catalogs: every
// key in the English (source) catalog must exist in every other locale, and
// no locale may carry an orphan key the English catalog doesn't define.
// English is the fallback, so a missing key degrades to English silently —
// this test makes that drift loud at build time instead.
func TestCatalogCompleteness(t *testing.T) {
	en, ok := catalogs[DefaultLocaleFallback]
	if !ok {
		t.Fatalf("no %q catalog loaded", DefaultLocaleFallback)
	}
	if len(en) == 0 {
		t.Fatal("English catalog is empty")
	}
	for locale, m := range catalogs {
		if locale == DefaultLocaleFallback {
			continue
		}
		for key := range en {
			if _, ok := m[key]; !ok {
				t.Errorf("locale %q missing key %q (present in English)", locale, key)
			}
		}
		for key := range m {
			if _, ok := en[key]; !ok {
				t.Errorf("locale %q has orphan key %q (not in English)", locale, key)
			}
		}
	}
}

func TestCatalogT(t *testing.T) {
	if got := CatalogT("fr", "chrome.sign_out"); got != "Se déconnecter" {
		t.Errorf("CatalogT(fr, chrome.sign_out) = %q, want French", got)
	}
	if got := CatalogT("en", "chrome.sign_out"); got != "Sign out" {
		t.Errorf("CatalogT(en, chrome.sign_out) = %q, want English", got)
	}
	// Unknown locale falls back to English.
	if got := CatalogT("zz", "chrome.sign_out"); got != "Sign out" {
		t.Errorf("CatalogT(zz, ...) = %q, want English fallback", got)
	}
	// A bundled non-English locale resolves (de added 0.76.x).
	if got := CatalogT("de", "chrome.sign_out"); got != "Abmelden" {
		t.Errorf("CatalogT(de, chrome.sign_out) = %q, want German", got)
	}
	// Nil locale (dict-rendered partial) falls back to English, no panic.
	if got := CatalogT(nil, "chrome.sign_out"); got != "Sign out" {
		t.Errorf("CatalogT(nil, ...) = %q, want English fallback", got)
	}
	// Unknown key surfaces the key itself (loud, not blank).
	if got := CatalogT("fr", "no.such.key"); got != "no.such.key" {
		t.Errorf("CatalogT(fr, no.such.key) = %q, want the key echoed back", got)
	}
}

func TestLocaleFromRequest(t *testing.T) {
	enabled := config.I18nSection{Enabled: true, DefaultLocale: "en", SupportedLocales: []string{"en", "fr"}}
	disabled := config.I18nSection{Enabled: false, DefaultLocale: "en", SupportedLocales: []string{"en", "fr"}}

	newReq := func(cookie, acceptLang string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: LocaleCookieName, Value: cookie})
		}
		if acceptLang != "" {
			r.Header.Set("Accept-Language", acceptLang)
		}
		return r
	}

	tests := []struct {
		name       string
		cfg        config.I18nSection
		cookie     string
		acceptLang string
		want       string
	}{
		{"disabled ignores everything", disabled, "fr", "fr-CA,fr;q=0.9", "en"},
		{"cookie wins", enabled, "fr", "en", "fr"},
		{"unsupported cookie falls through to header", enabled, "de", "fr", "fr"},
		{"accept-language primary subtag", enabled, "", "fr-CA,fr;q=0.9,en;q=0.8", "fr"},
		{"unsupported accept-language uses default", enabled, "", "de,es;q=0.9", "en"},
		{"nothing set uses default", enabled, "", "", "en"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LocaleFromRequest(tc.cfg, newReq(tc.cookie, tc.acceptLang)); got != tc.want {
				t.Errorf("LocaleFromRequest = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocaleChoices(t *testing.T) {
	if got := LocaleChoices(config.I18nSection{Enabled: false, SupportedLocales: []string{"en", "fr"}}); got != nil {
		t.Errorf("LocaleChoices(disabled) = %v, want nil", got)
	}
	if got := LocaleChoices(config.I18nSection{Enabled: true, SupportedLocales: []string{"en"}}); got != nil {
		t.Errorf("LocaleChoices(one locale) = %v, want nil", got)
	}
	if got := LocaleChoices(config.I18nSection{Enabled: true, SupportedLocales: []string{"en", "fr"}}); len(got) != 2 {
		t.Errorf("LocaleChoices = %v, want 2 options", got)
	}
}

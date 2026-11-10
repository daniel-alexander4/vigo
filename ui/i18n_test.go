// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/i18n"
)

func TestLocaleFromRequest(t *testing.T) {
	enabled := &Handler{i18n: config.I18nSection{
		Enabled:          true,
		DefaultLocale:    "en",
		SupportedLocales: []string{"en", "fr"},
	}}
	disabled := &Handler{i18n: config.I18nSection{
		Enabled:          false,
		DefaultLocale:    "en",
		SupportedLocales: []string{"en", "fr"},
	}}

	newReq := func(cookie, acceptLang string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: i18n.LocaleCookieName, Value: cookie})
		}
		if acceptLang != "" {
			r.Header.Set("Accept-Language", acceptLang)
		}
		return r
	}

	tests := []struct {
		name       string
		h          *Handler
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
			got := tc.h.localeFromRequest(newReq(tc.cookie, tc.acceptLang))
			if got != tc.want {
				t.Errorf("localeFromRequest = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderLocale is the end-to-end proof: the same page template renders
// English chrome at Locale "en" (byte-for-byte the original strings, so a
// disabled-i18n deployment is unchanged) and French chrome at Locale "fr".
func TestRenderLocale(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	// NoEnvoys collapses the nav to the brand block, avoiding nil-CurrentUser
	// derefs in the permission-gated nav links — keeps this a pure chrome test.
	render := func(locale string) string {
		data := SupportPageData{PageData: PageData{
			Title:    "Support",
			NoEnvoys: true,
			Theme:    "light",
			Locale:   locale,
		}}
		rec := httptest.NewRecorder()
		r.Render(rec, "support", data)
		if rec.Code != http.StatusOK {
			t.Fatalf("render(%q) status = %d", locale, rec.Code)
		}
		return rec.Body.String()
	}

	en := render("en")
	if !strings.Contains(en, "Getting help") {
		t.Error("en render missing original English heading 'Getting help'")
	}
	if strings.Contains(en, "Obtenir de l'aide") {
		t.Error("en render unexpectedly contains French")
	}

	fr := render("fr")
	if !strings.Contains(fr, "Obtenir de l&#39;aide") && !strings.Contains(fr, "Obtenir de l'aide") {
		t.Error("fr render missing French heading 'Obtenir de l'aide'")
	}
	if strings.Contains(fr, "Getting help") {
		t.Error("fr render still shows English heading 'Getting help'")
	}
}

func TestLocaleChoicesHiddenWhenDisabled(t *testing.T) {
	disabled := &Handler{i18n: config.I18nSection{Enabled: false, SupportedLocales: []string{"en", "fr"}}}
	if got := disabled.localeChoices(); got != nil {
		t.Errorf("localeChoices() with i18n disabled = %v, want nil", got)
	}
	single := &Handler{i18n: config.I18nSection{Enabled: true, SupportedLocales: []string{"en"}}}
	if got := single.localeChoices(); got != nil {
		t.Errorf("localeChoices() with one locale = %v, want nil (no picker)", got)
	}
	multi := &Handler{i18n: config.I18nSection{Enabled: true, SupportedLocales: []string{"en", "fr"}}}
	if got := multi.localeChoices(); len(got) != 2 {
		t.Errorf("localeChoices() = %v, want 2 options", got)
	}
}

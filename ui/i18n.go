// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/i18n"
)

// The localization catalog + per-request locale resolution live in the shared
// server/i18n package (so server/auth can localize the login pages without an
// import cycle — ui imports server/auth). The methods here are thin,
// config-bound wrappers over that package, keeping the call sites in handler.go
// and the templates unchanged.

// LocaleChoice aliases the shared picker-option type so PageData.Locales and
// the topbar template need no change.
type LocaleChoice = i18n.LocaleChoice

// SetI18n wires the localization config into the handler. Mirrors SetBranding
// — called once at startup from server.go.
func (h *Handler) SetI18n(cfg config.I18nSection) {
	h.i18n = cfg
}

// localeFromRequest resolves the locale for a request via the shared resolver.
// It's the single resolution point in ui — every full page (and the docs
// handler) routes through setCommonWithRequest, which calls this; no middleware.
func (h *Handler) localeFromRequest(r *http.Request) string {
	return i18n.LocaleFromRequest(h.i18n, r)
}

// LocaleFromRequest is the exported wrapper used by server/api report builders
// (which import ui) to stamp the resolved locale onto report payloads rendered
// through ui fragment templates.
func (h *Handler) LocaleFromRequest(r *http.Request) string {
	return i18n.LocaleFromRequest(h.i18n, r)
}

// localeChoices returns the topbar picker options for the current config.
func (h *Handler) localeChoices() []LocaleChoice {
	return i18n.LocaleChoices(h.i18n)
}

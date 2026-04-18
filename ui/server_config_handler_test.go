// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daniel-alexander4/vigo/server/auth"
)

// serverConfigFixture writes a server.yaml to a temp directory that exercises
// every state the Server Config UI must render correctly: enabled, explicitly
// disabled, empty mapping, absent, null, and a section with a secret: ref.
const serverConfigFixture = `
server:
  hostname: vigo.example.com
  grpc_listen: ":1530"

# database is commented out — should be absent

auth: {}

swarm:
  enabled: false
  port: 1531

ai:
  enabled: true
  provider: claude
  api_key: "secret:vigo/ai/key"

scrier: null

integrations:
  slack:
    enabled: true
    webhook_url: "secret:vigo/slack/webhook"
  jira:
    enabled: false
`

func writeServerConfigFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte(serverConfigFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// adminRequest builds a request with an admin user attached via auth context,
// matching what the auth middleware would have done in production.
func adminRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	ctx := auth.ContextWithUser(req.Context(), &auth.User{
		ID:       "test-admin",
		Username: "admin",
		Role:     "admin",
	})
	return req.WithContext(ctx)
}

// viewerRequest builds a request with a viewer user — the UI will 403.
func viewerRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	ctx := auth.ContextWithUser(req.Context(), &auth.User{
		ID:       "test-viewer",
		Username: "viewer",
		Role:     "viewer",
	})
	return req.WithContext(ctx)
}

func newServerConfigHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	h := newTestHandler(t)
	h.authEnabled = true
	path := writeServerConfigFixture(t)
	h.serverConfigPath = path
	return h, path
}

func TestServerConfig_AdminGate(t *testing.T) {
	h, _ := newServerConfigHandler(t)

	// Viewer → 403 on both routes.
	for _, target := range []string{"/server/config", "/server/config/ai"} {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, target)
		if target == "/server/config/ai" {
			req.SetPathValue("section", "ai")
			h.ServerConfigSection(rec, req)
		} else {
			h.ServerConfig(rec, req)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s = %d, want %d", target, rec.Code, http.StatusForbidden)
		}
	}
}

func TestServerConfig_IndexRendersCards(t *testing.T) {
	h, _ := newServerConfigHandler(t)
	rec := httptest.NewRecorder()
	h.ServerConfig(rec, adminRequest(http.MethodGet, "/server/config"))

	if rec.Code != http.StatusOK {
		t.Fatalf("ServerConfig returned %d, want 200", rec.Code)
	}

	body := rec.Body.String()

	// Every top-level section should render a card — spot-check the ones
	// exercised by the fixture plus a couple that aren't.
	for _, section := range []string{"server", "auth", "swarm", "ai", "scrier", "integrations", "database"} {
		// Card anchor uses href="/server/config/<name>" for navigation.
		want := `href="/server/config/` + section + `"`
		if !strings.Contains(body, want) {
			t.Errorf("index missing card link for section %q (looked for %s)", section, want)
		}
	}

	// Enabled sections (server, ai, integrations) should render the
	// "enabled" pill. Disabled should render the "disabled" pill at least
	// once.
	if !strings.Contains(body, "status-pill--converged") {
		t.Error("index should render an enabled pill for at least one section")
	}
	if !strings.Contains(body, "status-pill--no-data") {
		t.Error("index should render a disabled pill for at least one section")
	}
}

func TestServerConfig_DetailEnabledSection(t *testing.T) {
	h, _ := newServerConfigHandler(t)
	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/server/config/ai")
	req.SetPathValue("section", "ai")
	h.ServerConfigSection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServerConfigSection returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// Should show the enabled pill and the current-values block.
	if !strings.Contains(body, "Current Values") {
		t.Error("detail page for enabled section should have a Current Values block")
	}
	if !strings.Contains(body, "Available Options") {
		t.Error("detail page should always show Available Options")
	}

	// The api_key value must appear as the literal secret: reference. It
	// must NOT be resolved — the page reads the raw YAML, not the resolved
	// struct.
	if !strings.Contains(body, "secret:vigo/ai/key") {
		t.Error("detail page should render the literal secret: reference for ai.api_key")
	}
	// And it must never render any sign of a resolved plaintext. We
	// didn't put a secrets provider in the fixture, but double-check that
	// the literal string is what's shown.
	if strings.Contains(body, "resolved-secret-value") {
		t.Error("detail page leaked a resolved secret value")
	}
}

func TestServerConfig_DetailDisabledSection(t *testing.T) {
	h, _ := newServerConfigHandler(t)

	// "database" is commented out in the fixture — detail page should
	// show only the schema (no Current Values block).
	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/server/config/database")
	req.SetPathValue("section", "database")
	h.ServerConfigSection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServerConfigSection(database) = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, "Current Values") {
		t.Error("disabled section detail page should NOT show a Current Values block")
	}
	if !strings.Contains(body, "Available Options") {
		t.Error("disabled section detail page should still show Available Options")
	}
}

func TestServerConfig_UnknownSection404(t *testing.T) {
	h, _ := newServerConfigHandler(t)
	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/server/config/does-not-exist")
	req.SetPathValue("section", "does-not-exist")
	h.ServerConfigSection(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown section returned %d, want 404", rec.Code)
	}
}

func TestServerConfig_Subsections(t *testing.T) {
	h, _ := newServerConfigHandler(t)
	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/server/config/integrations")
	req.SetPathValue("section", "integrations")
	h.ServerConfigSection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("integrations detail = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// Should enumerate every integration sub-section.
	for _, sub := range []string{"integrations.slack", "integrations.jira", "integrations.pagerduty"} {
		if !strings.Contains(body, sub) {
			t.Errorf("integrations detail missing subsection header %q", sub)
		}
	}
	// Slack is enabled in the fixture; webhook_url should render as the
	// literal secret: reference.
	if !strings.Contains(body, "secret:vigo/slack/webhook") {
		t.Error("integrations.slack.webhook_url should render as the literal secret: reference")
	}
}


// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/tungol"
	"git.alexander4.io/vigo/server/license"
)

// CheckStatus represents the outcome of a single health check.
type CheckStatus string

const (
	StatusPass CheckStatus = "pass"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
	StatusSkip CheckStatus = "skip"
)

// CheckScope distinguishes diagnostics that describe the Vigo server
// itself from those that describe the managed fleet. The Vigo Health
// page renders only server-scope checks; fleet-scope conditions belong
// on dashboard/envoy surfaces and must not drag the server's headline
// status down.
type CheckScope string

const (
	ScopeServer CheckScope = "server"
	ScopeFleet  CheckScope = "fleet"
)

// DoctorCheck is a single health check result.
type DoctorCheck struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Scope   CheckScope  `json:"scope"`
	Message string      `json:"message,omitempty"`
}

// DoctorResponse is the full doctor endpoint response.
type DoctorResponse struct {
	Status string        `json:"status"` // healthy, degraded, unhealthy
	Checks []DoctorCheck `json:"checks"`
}

// CycleLookup resolves an envoy's smoothed observed inter-CheckIn
// cadence from a live FleetIndex. Implementations return 0 when the
// envoy has no agent-reported cadence yet (bootstrap).
type CycleLookup func(envoyID string) time.Duration

// DoctorConfig holds the dependencies for the doctor handler.
type DoctorConfig struct {
	SQLDB          *sql.DB
	CertFile       string
	KeyFile        string
	Enforcer       *license.Enforcer
	ConfigStore    *config.ConfigStore
	Queries        *db.Queries
	Interval       time.Duration
	Hub            *tungol.Hub
	Integrations   bool
	Role           config.TungolRole
	SecretsBackend string
	// CycleLookup, when set, supplies the per-envoy observed cycle so
	// the stale-envoys check uses the same per-envoy threshold the UI
	// envoys page uses. nil → bootstrap (configured interval only).
	CycleLookup    CycleLookup
}

// DoctorHandler serves the /api/v1/doctor health check endpoint.
type DoctorHandler struct {
	sqlDB          *sql.DB
	certFile       string
	keyFile        string
	enforcer       *license.Enforcer
	configStore    *config.ConfigStore
	queries        *db.Queries
	baseInterval   time.Duration
	hub            *tungol.Hub
	integrations   bool
	role           config.TungolRole
	secretsBackend string
	cycleLookup    CycleLookup
	swarmCfgMu     sync.RWMutex
	swarmCfg       config.SwarmSection
}

// SetSwarmConfig is called from main.go on startup and on every
// successful config reload so the swarm-pattern-order doctor check
// always sees the live config.
func (h *DoctorHandler) SetSwarmConfig(s config.SwarmSection) {
	h.swarmCfgMu.Lock()
	h.swarmCfg = s
	h.swarmCfgMu.Unlock()
}

// NewDoctorHandler creates a doctor handler with the given dependencies.
func NewDoctorHandler(cfg DoctorConfig) *DoctorHandler {
	return &DoctorHandler{
		sqlDB:          cfg.SQLDB,
		certFile:       cfg.CertFile,
		keyFile:        cfg.KeyFile,
		enforcer:       cfg.Enforcer,
		configStore:    cfg.ConfigStore,
		queries:        cfg.Queries,
		baseInterval:   cfg.Interval,
		hub:            cfg.Hub,
		integrations:   cfg.Integrations,
		role:           cfg.Role,
		secretsBackend: cfg.SecretsBackend,
		cycleLookup:    cfg.CycleLookup,
	}
}

// RegisterRoutes mounts the doctor endpoint on the given mux.
// Doctor surfaces server health (cert validity, DB stats, peer
// status, secrets backend reachability) — fleet.read is the right
// gate.
func (h *DoctorHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/doctor", read(http.HandlerFunc(h.handleDoctor)))
}

// RunChecks executes all health checks and returns the overall status and results.
// This is used by the UI health page via the HealthChecker interface.
func (h *DoctorHandler) RunChecks() (string, []DoctorCheck) {
	ctx := context.Background()
	checks := []DoctorCheck{
		h.checkDatabase(ctx),
		h.checkTLS(),
		h.checkSecrets(),
		h.checkLicense(ctx),
		h.checkSpanner(),
		h.checkIntegrations(),
		h.checkConfig(),
		h.checkSwarmPatterns(),
		h.checkStaleEnvoys(ctx),
	}
	overall := "healthy"
	for _, c := range checks {
		if c.Status == StatusFail {
			overall = "unhealthy"
			break
		}
		if c.Status == StatusWarn && overall == "healthy" {
			overall = "degraded"
		}
	}
	return overall, checks
}

func (h *DoctorHandler) handleDoctor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	checks := []DoctorCheck{
		h.checkDatabase(ctx),
		h.checkTLS(),
		h.checkSecrets(),
		h.checkLicense(ctx),
		h.checkSpanner(),
		h.checkIntegrations(),
		h.checkConfig(),
		h.checkSwarmPatterns(),
		h.checkStaleEnvoys(ctx),
	}

	overall := "healthy"
	for _, c := range checks {
		if c.Status == StatusFail {
			overall = "unhealthy"
			break
		}
		if c.Status == StatusWarn && overall == "healthy" {
			overall = "degraded"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DoctorResponse{
		Status: overall,
		Checks: checks,
	})
}

func (h *DoctorHandler) checkDatabase(ctx context.Context) DoctorCheck {
	if err := h.sqlDB.PingContext(ctx); err != nil {
		return DoctorCheck{Name: "database", Scope: ScopeServer, Status: StatusFail, Message: err.Error()}
	}
	return DoctorCheck{Name: "database", Scope: ScopeServer, Status: StatusPass}
}

func (h *DoctorHandler) checkTLS() DoctorCheck {
	cert, err := tls.LoadX509KeyPair(h.certFile, h.keyFile)
	if err != nil {
		return DoctorCheck{Name: "tls", Scope: ScopeServer, Status: StatusFail, Message: err.Error()}
	}

	if len(cert.Certificate) == 0 {
		return DoctorCheck{Name: "tls", Scope: ScopeServer, Status: StatusFail, Message: "no certificates in chain"}
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return DoctorCheck{Name: "tls", Scope: ScopeServer, Status: StatusFail, Message: fmt.Sprintf("parsing cert: %v", err)}
	}

	daysLeft := int(time.Until(leaf.NotAfter).Hours() / 24)
	expiry := leaf.NotAfter.Format("2006-01-02")

	if daysLeft < 0 {
		return DoctorCheck{Name: "tls", Scope: ScopeServer, Status: StatusFail, Message: fmt.Sprintf("cert expired %s", expiry)}
	}
	if daysLeft < 30 {
		return DoctorCheck{Name: "tls", Scope: ScopeServer, Status: StatusWarn, Message: fmt.Sprintf("cert expires in %d days (%s)", daysLeft, expiry)}
	}
	return DoctorCheck{Name: "tls", Scope: ScopeServer, Status: StatusPass, Message: fmt.Sprintf("expires %s", expiry)}
}

func (h *DoctorHandler) checkSecrets() DoctorCheck {
	if h.secretsBackend == "" {
		return DoctorCheck{Name: "secrets", Scope: ScopeServer, Status: StatusSkip}
	}
	return DoctorCheck{Name: "secrets", Scope: ScopeServer, Status: StatusPass, Message: h.secretsBackend}
}

func (h *DoctorHandler) checkLicense(ctx context.Context) DoctorCheck {
	status := h.enforcer.GetStatus()

	switch status.Stage {
	case license.StageCompliant:
		msg := fmt.Sprintf("%s, %d/%d envoys", status.Stage, status.ActiveEnvoys, status.MaxEnvoys)
		if status.DaysRemaining > 0 && status.DaysRemaining <= 30 {
			return DoctorCheck{Name: "license", Scope: ScopeServer, Status: StatusWarn, Message: fmt.Sprintf("%s, expires in %d days", msg, status.DaysRemaining)}
		}
		return DoctorCheck{Name: "license", Scope: ScopeServer, Status: StatusPass, Message: msg}
	case license.StageGrace:
		return DoctorCheck{Name: "license", Scope: ScopeServer, Status: StatusWarn, Message: fmt.Sprintf("Grace Period: %s (%d days left)", status.Violation, status.GraceDaysLeft)}
	default:
		return DoctorCheck{Name: "license", Scope: ScopeServer, Status: StatusFail, Message: fmt.Sprintf("%s: %s", status.Stage, status.Violation)}
	}
}

func (h *DoctorHandler) checkSpanner() DoctorCheck {
	switch h.role {
	case config.RoleHub:
		if h.hub == nil {
			return DoctorCheck{Name: "tungol", Scope: ScopeServer, Status: StatusFail, Message: "hub mode but no spoke connections"}
		}
		return DoctorCheck{Name: "tungol", Scope: ScopeServer, Status: StatusPass, Message: fmt.Sprintf("hub, %d spokes", h.hub.SpokeCount())}
	case config.RoleSpoke:
		return DoctorCheck{Name: "tungol", Scope: ScopeServer, Status: StatusPass, Message: "spoke"}
	default:
		return DoctorCheck{Name: "tungol", Scope: ScopeServer, Status: StatusSkip, Message: "standalone"}
	}
}

func (h *DoctorHandler) checkIntegrations() DoctorCheck {
	if !h.integrations {
		return DoctorCheck{Name: "integrations", Scope: ScopeServer, Status: StatusSkip, Message: "no integrations configured"}
	}
	return DoctorCheck{Name: "integrations", Scope: ScopeServer, Status: StatusPass, Message: "enabled"}
}

func (h *DoctorHandler) checkConfig() DoctorCheck {
	stats := h.configStore.Stats()
	msg := fmt.Sprintf("%d modules, %d entries", stats.Modules, stats.Entries)

	if stats.Modules == 0 {
		return DoctorCheck{Name: "config", Scope: ScopeServer, Status: StatusWarn, Message: "no modules loaded"}
	}
	return DoctorCheck{Name: "config", Scope: ScopeServer, Status: StatusPass, Message: msg}
}

// checkSwarmPatterns surfaces unreachable deny entries in the
// `swarm.*.enabled` pattern lists — the silent-fail case the
// `["*", "-rasp1"]` doc warning is meant to catch. Server logs the
// same findings at startup and on reload; this check makes the same
// signal available via `vigocli doctor` so operators can spot the
// problem without grepping logs.
func (h *DoctorHandler) checkSwarmPatterns() DoctorCheck {
	h.swarmCfgMu.RLock()
	swarm := h.swarmCfg
	h.swarmCfgMu.RUnlock()
	findings := config.ValidateSwarmPatternOrder(swarm)
	if len(findings) == 0 {
		return DoctorCheck{
			Name:    "swarm_pattern_order",
			Scope:   ScopeServer,
			Status:  StatusPass,
			Message: "all swarm.*.enabled pattern lists well-ordered",
		}
	}
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, f.String())
	}
	return DoctorCheck{
		Name:    "swarm_pattern_order",
		Scope:   ScopeServer,
		Status:  StatusWarn,
		Message: strings.Join(lines, "; "),
	}
}

func (h *DoctorHandler) checkStaleEnvoys(ctx context.Context) DoctorCheck {
	envoys, err := h.queries.ListEnvoys(ctx)
	if err != nil {
		return DoctorCheck{Name: "stale_envoys", Scope: ScopeFleet, Status: StatusFail, Message: err.Error()}
	}

	var stale []string
	for _, t := range envoys {
		if t.Revoked != 0 {
			continue
		}
		if !t.LastSeen.Valid {
			continue
		}
		var observed time.Duration
		if h.cycleLookup != nil {
			observed = h.cycleLookup(t.ID)
		}
		if freshness.IsStaleFromCycle(t.LastSeen.Time, h.baseInterval, observed) {
			stale = append(stale, t.Hostname)
		}
	}

	if len(stale) == 0 {
		return DoctorCheck{Name: "stale_envoys", Scope: ScopeFleet, Status: StatusPass}
	}

	msg := fmt.Sprintf("%d stale", len(stale))
	if len(stale) <= 5 {
		msg += ": " + joinHostnames(stale)
	}
	return DoctorCheck{Name: "stale_envoys", Scope: ScopeFleet, Status: StatusWarn, Message: msg}
}

func joinHostnames(names []string) string {
	result := ""
	for i, n := range names {
		if i > 0 {
			result += ", "
		}
		result += n
	}
	return result
}

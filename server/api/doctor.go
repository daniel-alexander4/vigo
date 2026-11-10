// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"
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
	SQLDB            *sql.DB
	CertFile         string
	KeyFile          string
	ConfigStore      *config.ConfigStore
	Queries          *db.Queries
	UserStore        *auth.UserStore
	Interval         time.Duration
	OfflineThreshold time.Duration
	Integrations     bool
	Mode             config.SpannerMode
	SecretsBackend   string
	// CycleLookup, when set, supplies the per-envoy observed cycle so
	// the stale-envoys check uses the same per-envoy threshold the UI
	// envoys page uses. nil → bootstrap (configured interval only).
	CycleLookup CycleLookup
}

// DoctorHandler serves the /api/v1/doctor health check endpoint.
type DoctorHandler struct {
	sqlDB            *sql.DB
	certFile         string
	keyFile          string
	configStore      *config.ConfigStore
	queries          *db.Queries
	userStore        *auth.UserStore
	baseInterval     time.Duration
	offlineThreshold time.Duration
	integrations     bool
	mode             config.SpannerMode
	secretsBackend   string
	cycleLookup      CycleLookup
	swarmCfgMu       sync.RWMutex
	swarmCfg         config.SwarmSection
}

// SetSwarmConfig is called from main.go on startup and on every
// successful config reload so the swarm-pattern-order doctor check
// always sees the live config.
func (h *DoctorHandler) SetSwarmConfig(s config.SwarmSection) {
	h.swarmCfgMu.Lock()
	h.swarmCfg = s
	h.swarmCfgMu.Unlock()
}

// SetUserStore wires the UserStore late so the webusers_replication
// check can count unsigned legacy rows. Doctor is constructed before
// UserStore in server.go (UserStore depends on auth provider), so
// the wiring runs post-construction (matches SetSwarmConfig pattern).
func (h *DoctorHandler) SetUserStore(us *auth.UserStore) {
	h.userStore = us
}

// NewDoctorHandler creates a doctor handler with the given dependencies.
func NewDoctorHandler(cfg DoctorConfig) *DoctorHandler {
	return &DoctorHandler{
		sqlDB:            cfg.SQLDB,
		certFile:         cfg.CertFile,
		keyFile:          cfg.KeyFile,
		configStore:      cfg.ConfigStore,
		queries:          cfg.Queries,
		userStore:        cfg.UserStore,
		baseInterval:     cfg.Interval,
		offlineThreshold: cfg.OfflineThreshold,
		integrations:     cfg.Integrations,
		mode:             cfg.Mode,
		secretsBackend:   cfg.SecretsBackend,
		cycleLookup:      cfg.CycleLookup,
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
	return h.runChecks(context.Background())
}

// runChecks runs every check in display order and derives the overall status.
// Shared by RunChecks (UI health page) and the /api/v1/doctor handler.
func (h *DoctorHandler) runChecks(ctx context.Context) (string, []DoctorCheck) {
	checks := []DoctorCheck{
		h.checkDatabase(ctx),
		h.checkTLS(),
		h.checkSecrets(),
		h.checkSpanner(),
		h.checkIntegrations(),
		h.checkConfig(),
		h.checkSwarmPatterns(),
		h.checkStaleEnvoys(ctx),
		h.checkUnpinnedEnvoys(ctx),
		h.checkWebusersReplication(ctx),
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
	overall, checks := h.runChecks(r.Context())
	writeJSON(w, r, http.StatusOK, DoctorResponse{
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

func (h *DoctorHandler) checkSpanner() DoctorCheck {
	if h.mode == config.ModeSpanner {
		return DoctorCheck{Name: "spanner", Scope: ScopeServer, Status: StatusPass, Message: "spanner — peer-equal bolt"}
	}
	return DoctorCheck{Name: "spanner", Scope: ScopeServer, Status: StatusSkip, Message: "standalone"}
}

func (h *DoctorHandler) checkIntegrations() DoctorCheck {
	if !h.integrations {
		return DoctorCheck{Name: "integrations", Scope: ScopeServer, Status: StatusSkip, Message: "no integrations configured"}
	}
	return DoctorCheck{Name: "integrations", Scope: ScopeServer, Status: StatusPass, Message: "enabled"}
}

func (h *DoctorHandler) checkConfig() DoctorCheck {
	if err := h.configStore.LastReloadError(); err != nil {
		// IdempotencyErrors at startup leave the store empty with the
		// error stashed in lastReloadErr. Without this, an empty store
		// would surface as a "no configcrates loaded" WARN with no hint that
		// reload actually failed.
		return DoctorCheck{Name: "config", Scope: ScopeServer, Status: StatusFail, Message: "reload failed: " + err.Error()}
	}
	stats := h.configStore.Stats()
	msg := fmt.Sprintf("%d configcrates, %d entries", stats.Configcrates, stats.Entries)

	if stats.Configcrates == 0 {
		return DoctorCheck{Name: "config", Scope: ScopeServer, Status: StatusWarn, Message: "no configcrates loaded"}
	}
	return DoctorCheck{Name: "config", Scope: ScopeServer, Status: StatusPass, Message: msg}
}

// checkWebusersReplication surfaces unsigned (pre-ADR-030 Slice
// 2-iii-c) user rows still awaiting first-startup re-signing. In
// spanner mode the count should be zero after a normal upgrade
// cycle — Slice 2-iii-f's startup hook re-signs every legacy row
// synchronously, so non-zero indicates a re-sign that failed
// (logged) or new code that mints rows without going through the
// UserStore signer path. Skipped in standalone mode (no signer,
// every row would always be "unsigned" — the warn would be noise).
func (h *DoctorHandler) checkWebusersReplication(ctx context.Context) DoctorCheck {
	if h.mode == config.ModeStandalone {
		return DoctorCheck{Name: "webusers_replication", Scope: ScopeServer, Status: StatusSkip, Message: "standalone — replication N/A"}
	}
	if h.userStore == nil {
		return DoctorCheck{Name: "webusers_replication", Scope: ScopeServer, Status: StatusSkip, Message: "user store unavailable"}
	}
	count, err := h.userStore.CountUnsignedUsers(ctx)
	if err != nil {
		return DoctorCheck{Name: "webusers_replication", Scope: ScopeServer, Status: StatusFail, Message: "counting unsigned users: " + err.Error()}
	}
	if count > 0 {
		return DoctorCheck{Name: "webusers_replication", Scope: ScopeServer, Status: StatusWarn, Message: fmt.Sprintf("%d webuser row(s) awaiting first-replication (legacy unsigned)", count)}
	}
	return DoctorCheck{Name: "webusers_replication", Scope: ScopeServer, Status: StatusPass, Message: "all webusers signed"}
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
		if freshness.IsStaleFromCycle(t.LastSeen.Time, h.baseInterval, observed, h.offlineThreshold) {
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

// checkUnpinnedEnvoys reports non-revoked envoys with no enrolled mTLS
// client-cert pin. The check-in verify path refuses them (fail-closed), so
// each needs re-enrolling to capture a pin.
func (h *DoctorHandler) checkUnpinnedEnvoys(ctx context.Context) DoctorCheck {
	envoys, err := h.queries.ListEnvoys(ctx)
	if err != nil {
		return DoctorCheck{Name: "unpinned_envoys", Scope: ScopeFleet, Status: StatusFail, Message: err.Error()}
	}
	var unpinned []string
	for _, e := range envoys {
		if e.Revoked == 0 && e.CertSpkiSha256 == "" {
			unpinned = append(unpinned, e.Hostname)
		}
	}
	if len(unpinned) == 0 {
		return DoctorCheck{Name: "unpinned_envoys", Scope: ScopeFleet, Status: StatusPass}
	}
	msg := fmt.Sprintf("%d envoy(s) have no enrolled client-cert pin and are refused at check-in", len(unpinned))
	if len(unpinned) <= 5 {
		msg += ": " + joinHostnames(unpinned)
	}
	msg += " — re-enroll each: curl -sSfk https://<server>:8443/bootstrap | sudo sh"
	return DoctorCheck{Name: "unpinned_envoys", Scope: ScopeFleet, Status: StatusWarn, Message: msg}
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

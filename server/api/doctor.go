// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	"github.com/hobnob/hobnob/server/license"
)

// CheckStatus represents the outcome of a single health check.
type CheckStatus string

const (
	StatusPass CheckStatus = "pass"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
	StatusSkip CheckStatus = "skip"
)

// DoctorCheck is a single health check result.
type DoctorCheck struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message,omitempty"`
}

// DoctorResponse is the full doctor endpoint response.
type DoctorResponse struct {
	Status string        `json:"status"` // healthy, degraded, unhealthy
	Checks []DoctorCheck `json:"checks"`
}

// DoctorConfig holds the dependencies for the doctor handler.
type DoctorConfig struct {
	SQLDB          *sql.DB
	CertFile       string
	KeyFile        string
	Enforcer       *license.Enforcer
	ConfigStore    *config.ConfigStore
	Queries        *db.Queries
	BaseInterval   time.Duration
	Hub            *federation.Hub
	SMTPHost       string
	SMTPPort       int
	Role           config.FederationRole
	SecretsBackend string
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
	hub            *federation.Hub
	smtpHost       string
	smtpPort       int
	role           config.FederationRole
	secretsBackend string
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
		baseInterval:   cfg.BaseInterval,
		hub:            cfg.Hub,
		smtpHost:       cfg.SMTPHost,
		smtpPort:       cfg.SMTPPort,
		role:           cfg.Role,
		secretsBackend: cfg.SecretsBackend,
	}
}

// RegisterRoutes mounts the doctor endpoint on the given mux.
func (h *DoctorHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/doctor", h.handleDoctor)
}

func (h *DoctorHandler) handleDoctor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	checks := []DoctorCheck{
		h.checkDatabase(ctx),
		h.checkTLS(),
		h.checkSecrets(),
		h.checkLicense(ctx),
		h.checkFederation(),
		h.checkSMTP(),
		h.checkConfig(),
		h.checkStaleThralls(ctx),
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
		return DoctorCheck{Name: "database", Status: StatusFail, Message: err.Error()}
	}
	return DoctorCheck{Name: "database", Status: StatusPass}
}

func (h *DoctorHandler) checkTLS() DoctorCheck {
	cert, err := tls.LoadX509KeyPair(h.certFile, h.keyFile)
	if err != nil {
		return DoctorCheck{Name: "tls", Status: StatusFail, Message: err.Error()}
	}

	if len(cert.Certificate) == 0 {
		return DoctorCheck{Name: "tls", Status: StatusFail, Message: "no certificates in chain"}
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return DoctorCheck{Name: "tls", Status: StatusFail, Message: fmt.Sprintf("parsing cert: %v", err)}
	}

	daysLeft := int(time.Until(leaf.NotAfter).Hours() / 24)
	expiry := leaf.NotAfter.Format("2006-01-02")

	if daysLeft < 0 {
		return DoctorCheck{Name: "tls", Status: StatusFail, Message: fmt.Sprintf("cert expired %s", expiry)}
	}
	if daysLeft < 30 {
		return DoctorCheck{Name: "tls", Status: StatusWarn, Message: fmt.Sprintf("cert expires in %d days (%s)", daysLeft, expiry)}
	}
	return DoctorCheck{Name: "tls", Status: StatusPass, Message: fmt.Sprintf("expires %s", expiry)}
}

func (h *DoctorHandler) checkSecrets() DoctorCheck {
	if h.secretsBackend == "" {
		return DoctorCheck{Name: "secrets", Status: StatusSkip}
	}
	return DoctorCheck{Name: "secrets", Status: StatusPass, Message: h.secretsBackend}
}

func (h *DoctorHandler) checkLicense(ctx context.Context) DoctorCheck {
	status := h.enforcer.GetStatus()

	switch status.Stage {
	case license.StageCompliant:
		msg := fmt.Sprintf("%s, %d/%d thralls", status.Stage, status.ActiveThralls, status.MaxThralls)
		if status.DaysRemaining > 0 && status.DaysRemaining <= 30 {
			return DoctorCheck{Name: "license", Status: StatusWarn, Message: fmt.Sprintf("%s, expires in %d days", msg, status.DaysRemaining)}
		}
		return DoctorCheck{Name: "license", Status: StatusPass, Message: msg}
	case license.StageGrace:
		return DoctorCheck{Name: "license", Status: StatusWarn, Message: fmt.Sprintf("Grace Period: %s (%d days left)", status.Violation, status.GraceDaysLeft)}
	default:
		return DoctorCheck{Name: "license", Status: StatusFail, Message: fmt.Sprintf("%s: %s", status.Stage, status.Violation)}
	}
}

func (h *DoctorHandler) checkFederation() DoctorCheck {
	switch h.role {
	case config.RoleHub:
		if h.hub == nil {
			return DoctorCheck{Name: "federation", Status: StatusFail, Message: "hub mode but no spoke connections"}
		}
		return DoctorCheck{Name: "federation", Status: StatusPass, Message: fmt.Sprintf("hub, %d spokes", h.hub.SpokeCount())}
	case config.RoleSpoke:
		return DoctorCheck{Name: "federation", Status: StatusPass, Message: "spoke"}
	default:
		return DoctorCheck{Name: "federation", Status: StatusSkip, Message: "standalone"}
	}
}

func (h *DoctorHandler) checkSMTP() DoctorCheck {
	if h.smtpHost == "" {
		return DoctorCheck{Name: "smtp", Status: StatusSkip, Message: "not configured"}
	}

	port := h.smtpPort
	if port == 0 {
		port = 587
	}
	addr := fmt.Sprintf("%s:%d", h.smtpHost, port)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return DoctorCheck{Name: "smtp", Status: StatusFail, Message: fmt.Sprintf("cannot reach %s: %v", addr, err)}
	}
	conn.Close()

	return DoctorCheck{Name: "smtp", Status: StatusPass, Message: addr}
}

func (h *DoctorHandler) checkConfig() DoctorCheck {
	stats := h.configStore.Stats()
	msg := fmt.Sprintf("%d modules, %d entries", stats.Modules, stats.Entries)

	if stats.Modules == 0 {
		return DoctorCheck{Name: "config", Status: StatusWarn, Message: "no modules loaded"}
	}
	return DoctorCheck{Name: "config", Status: StatusPass, Message: msg}
}

func (h *DoctorHandler) checkStaleThralls(ctx context.Context) DoctorCheck {
	thralls, err := h.queries.ListThralls(ctx)
	if err != nil {
		return DoctorCheck{Name: "stale_thralls", Status: StatusFail, Message: err.Error()}
	}

	staleThreshold := 2 * h.baseInterval
	var stale []string
	for _, t := range thralls {
		if t.Revoked != 0 {
			continue
		}
		if !t.LastSeen.Valid {
			continue
		}
		if time.Since(t.LastSeen.Time) > staleThreshold {
			stale = append(stale, t.Hostname)
		}
	}

	if len(stale) == 0 {
		return DoctorCheck{Name: "stale_thralls", Status: StatusPass}
	}

	msg := fmt.Sprintf("%d stale", len(stale))
	if len(stale) <= 5 {
		msg += ": " + joinHostnames(stale)
	}
	return DoctorCheck{Name: "stale_thralls", Status: StatusWarn, Message: msg}
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

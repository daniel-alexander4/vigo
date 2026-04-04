// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package tui provides a bubbletea terminal dashboard for vigocli.
package tui

import "time"

// DataSource abstracts the REST API so the TUI is decoupled from transport.
type DataSource interface {
	FetchEnvoys() ([]Envoy, error)
	FetchRuns(limit int) ([]Run, error)
	FetchRunDetail(id string) (RunDetail, error)
	FetchRisk() (FleetRisk, error)
	FetchAudit(limit int) ([]AuditEvent, error)
}

// Envoy is a managed node in the fleet.
type Envoy struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	IPAddress  string `json:"ipAddress"`
	EnrolledAt string `json:"enrolledAt"`
	LastSeen   string `json:"lastSeen"`
	Revoked    bool   `json:"revoked"`
}

// Run is a convergence run summary.
type Run struct {
	ID           string `json:"id"`
	EnvoyID      string `json:"envoyId"`
	Hostname     string `json:"hostname"`
	StartedAt    string `json:"startedAt"`
	Status       string `json:"status"`
	ChangedCount int32  `json:"changedCount"`
	FailedCount  int32  `json:"failedCount"`
}

// RunDetail is a single run with per-resource results.
type RunDetail struct {
	Run     Run            `json:"run"`
	Results []RunResult    `json:"results"`
}

// RunResult is a per-resource outcome within a run.
type RunResult struct {
	ModuleName   string `json:"moduleName"`
	ResourceType string `json:"resourceType"`
	ResourceName string `json:"resourceName"`
	ActionTaken  string `json:"actionTaken"`
	Changed      bool   `json:"changed"`
	ErrorMessage string `json:"errorMessage"`
	DurationMs   int32  `json:"durationMs"`
}

// FleetRisk is the fleet-wide risk summary.
type FleetRisk struct {
	TotalEnvoys  int                `json:"total_envoys"`
	ScoredEnvoys int                `json:"scored_envoys"`
	AvgScore     float64            `json:"avg_score"`
	MaxScore     int                `json:"max_score"`
	Distribution map[string]int     `json:"distribution"`
	TopRisks     []RiskEnvoy        `json:"top_risks"`
}

// RiskEnvoy is a single envoy's risk score.
type RiskEnvoy struct {
	EnvoyID  string `json:"envoy_id"`
	Hostname string `json:"hostname"`
	Score    int    `json:"score"`
	Level    string `json:"level"`
}

// AuditEvent is a single audit log entry.
type AuditEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Operator  string    `json:"operator"`
	Summary   string    `json:"summary"`
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daniel-alexander4/vigo/cli/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:     "tui",
	Aliases: []string{"vigotui"},
	Short:   "Launch the interactive terminal dashboard",
	Long:  "Opens a full-screen terminal UI with fleet dashboard, envoy list, runs, risk, and audit views. Auto-refreshes on a configurable interval.",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		interval, _ := cmd.Flags().GetInt("refresh")
		ds := &tuiDataSource{client: client}
		m := tui.New(ds, time.Duration(interval)*time.Second)
		p := tea.NewProgram(m, tea.WithAltScreen())
		_, err := p.Run()
		return err
	},
}

func init() {
	tuiCmd.Flags().Int("refresh", 10, "Auto-refresh interval in seconds (0 to disable)")
	rootCmd.AddCommand(tuiCmd)
}

// tuiDataSource adapts apiClient to the tui.DataSource interface.
type tuiDataSource struct {
	client *apiClient
}

func (ds *tuiDataSource) FetchEnvoys() ([]tui.Envoy, error) {
	var resp struct {
		Envoys []tui.Envoy `json:"envoys"`
	}
	if err := ds.client.get("/api/v1/envoys", &resp); err != nil {
		return nil, err
	}
	return resp.Envoys, nil
}

func (ds *tuiDataSource) FetchRuns(limit int) ([]tui.Run, error) {
	path := fmt.Sprintf("/api/v1/runs?limit=%d", limit)
	var resp struct {
		Runs []tui.Run `json:"runs"`
	}
	if err := ds.client.get(path, &resp); err != nil {
		return nil, err
	}
	return resp.Runs, nil
}

func (ds *tuiDataSource) FetchRunDetail(id string) (tui.RunDetail, error) {
	var resp struct {
		Run struct {
			ID           string `json:"id"`
			EnvoyID      string `json:"envoyId"`
			StartedAt    string `json:"startedAt"`
			FinishedAt   string `json:"finishedAt"`
			Status       string `json:"status"`
			ModuleCount  int32  `json:"moduleCount"`
			ChangedCount int32  `json:"changedCount"`
			FailedCount  int32  `json:"failedCount"`
			SkippedCount int32  `json:"skippedCount"`
		} `json:"run"`
		Results []tui.RunResult `json:"results"`
	}
	if err := ds.client.get("/api/v1/runs/"+id, &resp); err != nil {
		return tui.RunDetail{}, err
	}
	return tui.RunDetail{
		Run: tui.Run{
			ID:           resp.Run.ID,
			EnvoyID:      resp.Run.EnvoyID,
			StartedAt:    resp.Run.StartedAt,
			Status:       resp.Run.Status,
			ChangedCount: resp.Run.ChangedCount,
			FailedCount:  resp.Run.FailedCount,
		},
		Results: resp.Results,
	}, nil
}

func (ds *tuiDataSource) FetchRisk() (tui.FleetRisk, error) {
	var resp tui.FleetRisk
	if err := ds.client.get("/api/v1/risk", &resp); err != nil {
		return tui.FleetRisk{}, err
	}
	return resp, nil
}

func (ds *tuiDataSource) FetchAudit(limit int) ([]tui.AuditEvent, error) {
	path := fmt.Sprintf("/api/v1/audit?limit=%d", limit)
	var raw json.RawMessage
	if err := ds.client.get(path, &raw); err != nil {
		return nil, err
	}
	var events []tui.AuditEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		// Try wrapped format.
		var resp struct {
			Events []tui.AuditEvent `json:"events"`
		}
		if err2 := json.Unmarshal(raw, &resp); err2 != nil {
			return nil, err
		}
		events = resp.Events
	}
	return events, nil
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"git.alexander4.io/vigo/server/config"
)

// --- Autotask / Datto PSA ---

type autotaskTarget struct {
	cfg    *config.AutotaskIntegration
	client *http.Client
}

func (a *autotaskTarget) Name() string                  { return "autotask" }
func (a *autotaskTarget) Accepts(event Event) bool { return acceptsEvent(a.cfg.Events, event.Name) }
func (a *autotaskTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"title":       fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"description": fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s", e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"queueID":     a.cfg.QueueID,
		"status":      1, // new
		"priority":    autotaskPriority(e.Name),
	}
	url := strings.TrimSuffix(a.cfg.URL, "/") + "/Tickets"
	auth := basicAuth(a.cfg.Username + ":" + a.cfg.Password)
	headers := map[string]string{"Authorization": "Basic " + auth}
	return postJSON(ctx, a.client, url, headers, payload)
}

func autotaskPriority(eventName string) int {
	if strings.Contains(eventName, "rootkit") || strings.Contains(eventName, "malware") {
		return 1 // critical
	}
	if strings.Contains(eventName, "failure") {
		return 2 // high
	}
	return 3 // medium
}

// --- NinjaRMM ---

type ninjaRMMTarget struct {
	cfg    *config.NinjaRMMIntegration
	client *http.Client
}

func (n *ninjaRMMTarget) Name() string                  { return "ninjarmm" }
func (n *ninjaRMMTarget) Accepts(event Event) bool { return acceptsEvent(n.cfg.Events, event.Name) }
func (n *ninjaRMMTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"subject":     fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"description": fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s", e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"priority":    "NORMAL",
		"type":        "PROBLEM",
	}
	if strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		payload["priority"] = "HIGH"
	}
	url := strings.TrimSuffix(n.cfg.URL, "/") + "/v2/ticketing/ticket"
	headers := map[string]string{
		"Authorization": "Bearer " + n.cfg.ClientSecret,
	}
	return postJSON(ctx, n.client, url, headers, payload)
}

// --- Kaseya VSA ---

type kaseyaTarget struct {
	cfg    *config.KaseyaIntegration
	client *http.Client
}

func (k *kaseyaTarget) Name() string                  { return "kaseya" }
func (k *kaseyaTarget) Accepts(event Event) bool { return acceptsEvent(k.cfg.Events, event.Name) }
func (k *kaseyaTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"ServiceDeskTicketId": 0,
		"Summary":            fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"Description":        fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s", e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"Priority":           "Normal",
	}
	url := strings.TrimSuffix(k.cfg.URL, "/") + "/api/v1.0/automation/servicedesks/tickets"
	auth := basicAuth(k.cfg.Username + ":" + k.cfg.Password)
	headers := map[string]string{"Authorization": "Basic " + auth}
	return postJSON(ctx, k.client, url, headers, payload)
}

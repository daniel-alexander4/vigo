// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package integrations provides event dispatching to external platforms
// (Slack, PagerDuty, ConnectWise, Splunk, etc.). Each integration
// subscribes to Vigo event types and formats payloads for its target
// platform's API.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/config"
)

// Event represents a Vigo event to be dispatched to integrations.
type Event struct {
	Name      string
	Timestamp time.Time
	Hostname  string
	EnvoyID   string
	Summary   string
	Details   map[string]any
}

// Dispatcher routes events to all configured and enabled integrations.
type Dispatcher struct {
	targets []target
	client  *http.Client
}

type target interface {
	Name() string
	Accepts(eventName string) bool
	Dispatch(ctx context.Context, event Event) error
}

// New creates a Dispatcher from the integrations config.
func New(cfg config.IntegrationsSection) *Dispatcher {
	d := &Dispatcher{
		client: &http.Client{Timeout: 10 * time.Second},
	}

	if cfg.Slack != nil && cfg.Slack.Enabled {
		d.targets = append(d.targets, &slackTarget{cfg: cfg.Slack, client: d.client})
	}
	if cfg.PagerDuty != nil && cfg.PagerDuty.Enabled {
		d.targets = append(d.targets, &pagerdutyTarget{cfg: cfg.PagerDuty, client: d.client})
	}
	if cfg.Opsgenie != nil && cfg.Opsgenie.Enabled {
		d.targets = append(d.targets, &opsgenieTarget{cfg: cfg.Opsgenie, client: d.client})
	}
	if cfg.Teams != nil && cfg.Teams.Enabled {
		d.targets = append(d.targets, &teamsTarget{cfg: cfg.Teams, client: d.client})
	}
	if cfg.ConnectWise != nil && cfg.ConnectWise.Enabled {
		d.targets = append(d.targets, &connectwiseTarget{cfg: cfg.ConnectWise, client: d.client})
	}
	if cfg.Splunk != nil && cfg.Splunk.Enabled {
		d.targets = append(d.targets, &splunkTarget{cfg: cfg.Splunk, client: d.client})
	}
	if cfg.Elastic != nil && cfg.Elastic.Enabled {
		d.targets = append(d.targets, &elasticTarget{cfg: cfg.Elastic, client: d.client})
	}
	if cfg.Datadog != nil && cfg.Datadog.Enabled {
		d.targets = append(d.targets, &datadogTarget{cfg: cfg.Datadog, client: d.client})
	}
	if cfg.Jira != nil && cfg.Jira.Enabled {
		d.targets = append(d.targets, &jiraTarget{cfg: cfg.Jira, client: d.client})
	}
	if cfg.Zendesk != nil && cfg.Zendesk.Enabled {
		d.targets = append(d.targets, &zendeskTarget{cfg: cfg.Zendesk, client: d.client})
	}
	if cfg.Freshservice != nil && cfg.Freshservice.Enabled {
		d.targets = append(d.targets, &freshserviceTarget{cfg: cfg.Freshservice, client: d.client})
	}
	if cfg.Linear != nil && cfg.Linear.Enabled {
		d.targets = append(d.targets, &linearTarget{cfg: cfg.Linear, client: d.client})
	}
	if cfg.GitHubIssues != nil && cfg.GitHubIssues.Enabled {
		d.targets = append(d.targets, &githubIssuesTarget{cfg: cfg.GitHubIssues, client: d.client})
	}
	if cfg.Autotask != nil && cfg.Autotask.Enabled {
		d.targets = append(d.targets, &autotaskTarget{cfg: cfg.Autotask, client: d.client})
	}
	if cfg.NinjaRMM != nil && cfg.NinjaRMM.Enabled {
		d.targets = append(d.targets, &ninjaRMMTarget{cfg: cfg.NinjaRMM, client: d.client})
	}
	if cfg.Kaseya != nil && cfg.Kaseya.Enabled {
		d.targets = append(d.targets, &kaseyaTarget{cfg: cfg.Kaseya, client: d.client})
	}
	if cfg.SumoLogic != nil && cfg.SumoLogic.Enabled {
		d.targets = append(d.targets, &sumoLogicTarget{cfg: cfg.SumoLogic, client: d.client})
	}
	if cfg.Sentinel != nil && cfg.Sentinel.Enabled {
		d.targets = append(d.targets, &sentinelTarget{cfg: cfg.Sentinel, client: d.client})
	}
	if cfg.Device42 != nil && cfg.Device42.Enabled {
		d.targets = append(d.targets, &device42Target{cfg: cfg.Device42, client: d.client})
	}
	if cfg.SnipeIT != nil && cfg.SnipeIT.Enabled {
		d.targets = append(d.targets, &snipeITTarget{cfg: cfg.SnipeIT, client: d.client})
	}
	if cfg.ServiceNow != nil && cfg.ServiceNow.Enabled {
		d.targets = append(d.targets, &servicenowTarget{cfg: cfg.ServiceNow, client: d.client})
	}

	return d
}

// Enabled returns true if any integration is configured and enabled.
func (d *Dispatcher) Enabled() bool {
	return len(d.targets) > 0
}

// Dispatch sends an event to all matching integrations asynchronously.
func (d *Dispatcher) Dispatch(event Event) {
	for _, t := range d.targets {
		if !t.Accepts(event.Name) {
			continue
		}
		go func(tgt target) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := tgt.Dispatch(ctx, event); err != nil {
				slog.Error("integration dispatch failed",
					"target", tgt.Name(),
					"event", event.Name,
					"error", err,
				)
			}
		}(t)
	}
}

// acceptsEvent checks if an event name matches the target's event filter.
func acceptsEvent(filter []string, eventName string) bool {
	if len(filter) == 0 {
		return true // empty filter = accept all
	}
	for _, f := range filter {
		if f == eventName {
			return true
		}
	}
	return false
}

// postJSON sends a JSON POST request.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

// --- Slack ---

type slackTarget struct {
	cfg    *config.SlackIntegration
	client *http.Client
}

func (s *slackTarget) Name() string                        { return "slack" }
func (s *slackTarget) Accepts(eventName string) bool       { return acceptsEvent(s.cfg.Events, eventName) }
func (s *slackTarget) Dispatch(ctx context.Context, e Event) error {
	color := "#36a64f" // green
	if strings.Contains(e.Name, "failure") || strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		color = "#cc0000"
	} else if strings.Contains(e.Name, "drift") || strings.Contains(e.Name, "warning") || strings.Contains(e.Name, "stale") {
		color = "#f2c744"
	}

	payload := map[string]any{
		"attachments": []map[string]any{{
			"color":  color,
			"title":  fmt.Sprintf("[Vigo] %s", e.Name),
			"text":   e.Summary,
			"fields": []map[string]string{
				{"title": "Host", "value": e.Hostname, "short": "true"},
				{"title": "Time", "value": e.Timestamp.Format("2006-01-02 15:04:05 UTC"), "short": "true"},
			},
			"footer": "Vigo",
			"ts":     e.Timestamp.Unix(),
		}},
	}
	return postJSON(ctx, s.client, s.cfg.WebhookURL, nil, payload)
}

// --- PagerDuty ---

type pagerdutyTarget struct {
	cfg    *config.PagerDutyIntegration
	client *http.Client
}

func (p *pagerdutyTarget) Name() string                  { return "pagerduty" }
func (p *pagerdutyTarget) Accepts(eventName string) bool { return acceptsEvent(p.cfg.Events, eventName) }
func (p *pagerdutyTarget) Dispatch(ctx context.Context, e Event) error {
	severity := "warning"
	if strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") || strings.Contains(e.Name, "integrity") {
		severity = "critical"
	} else if strings.Contains(e.Name, "failure") || strings.Contains(e.Name, "cve_critical") {
		severity = "error"
	}

	payload := map[string]any{
		"routing_key":  p.cfg.RoutingKey,
		"event_action": "trigger",
		"payload": map[string]any{
			"summary":   fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Summary),
			"source":    e.Hostname,
			"severity":  severity,
			"timestamp": e.Timestamp.Format(time.RFC3339),
			"custom_details": e.Details,
		},
	}
	return postJSON(ctx, p.client, "https://events.pagerduty.com/v2/enqueue", nil, payload)
}

// --- Opsgenie ---

type opsgenieTarget struct {
	cfg    *config.OpsgenieIntegration
	client *http.Client
}

func (o *opsgenieTarget) Name() string                  { return "opsgenie" }
func (o *opsgenieTarget) Accepts(eventName string) bool { return acceptsEvent(o.cfg.Events, eventName) }
func (o *opsgenieTarget) Dispatch(ctx context.Context, e Event) error {
	priority := "P3"
	if strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		priority = "P1"
	} else if strings.Contains(e.Name, "failure") || strings.Contains(e.Name, "cve_critical") {
		priority = "P2"
	}

	payload := map[string]any{
		"message":     fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"description": e.Summary,
		"priority":    priority,
		"source":      "vigo",
		"entity":      e.Hostname,
		"details":     e.Details,
	}
	headers := map[string]string{"Authorization": "GenieKey " + o.cfg.APIKey}
	return postJSON(ctx, o.client, "https://api.opsgenie.com/v2/alerts", headers, payload)
}

// --- Microsoft Teams ---

type teamsTarget struct {
	cfg    *config.TeamsIntegration
	client *http.Client
}

func (t *teamsTarget) Name() string                  { return "teams" }
func (t *teamsTarget) Accepts(eventName string) bool { return acceptsEvent(t.cfg.Events, eventName) }
func (t *teamsTarget) Dispatch(ctx context.Context, e Event) error {
	color := "00cc00"
	if strings.Contains(e.Name, "failure") || strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		color = "cc0000"
	} else if strings.Contains(e.Name, "drift") || strings.Contains(e.Name, "stale") {
		color = "f2c744"
	}

	payload := map[string]any{
		"@type":      "MessageCard",
		"@context":   "http://schema.org/extensions",
		"themeColor": color,
		"summary":    fmt.Sprintf("[Vigo] %s", e.Name),
		"sections": []map[string]any{{
			"activityTitle": fmt.Sprintf("[Vigo] %s", e.Name),
			"facts": []map[string]string{
				{"name": "Host", "value": e.Hostname},
				{"name": "Event", "value": e.Name},
				{"name": "Time", "value": e.Timestamp.Format("2006-01-02 15:04:05 UTC")},
			},
			"text": e.Summary,
		}},
	}
	return postJSON(ctx, t.client, t.cfg.WebhookURL, nil, payload)
}

// --- ConnectWise ---

type connectwiseTarget struct {
	cfg    *config.ConnectWiseIntegration
	client *http.Client
}

func (c *connectwiseTarget) Name() string                  { return "connectwise" }
func (c *connectwiseTarget) Accepts(eventName string) bool { return acceptsEvent(c.cfg.Events, eventName) }
func (c *connectwiseTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"summary": fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"board":   map[string]int{"id": c.cfg.BoardID},
		"status":  map[string]string{"name": "New"},
		"initialDescription": fmt.Sprintf(
			"Vigo detected %s on %s at %s.\n\n%s",
			e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary,
		),
	}
	url := strings.TrimSuffix(c.cfg.URL, "/") + "/service/tickets"
	auth := fmt.Sprintf("%s+%s:%s", c.cfg.CompanyID, c.cfg.PublicKey, c.cfg.PrivateKey)
	headers := map[string]string{
		"Authorization": "Basic " + basicAuth(auth),
		"clientId":      "vigo",
	}
	return postJSON(ctx, c.client, url, headers, payload)
}

func basicAuth(creds string) string {
	return base64Encode([]byte(creds))
}

func base64Encode(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	for i := 0; i < len(data); i += 3 {
		var n uint32
		remaining := len(data) - i
		n = uint32(data[i]) << 16
		if remaining > 1 { n |= uint32(data[i+1]) << 8 }
		if remaining > 2 { n |= uint32(data[i+2]) }
		b.WriteByte(alphabet[(n>>18)&0x3f])
		b.WriteByte(alphabet[(n>>12)&0x3f])
		if remaining > 1 { b.WriteByte(alphabet[(n>>6)&0x3f]) } else { b.WriteByte('=') }
		if remaining > 2 { b.WriteByte(alphabet[n&0x3f]) } else { b.WriteByte('=') }
	}
	return b.String()
}

// --- Splunk HEC ---

type splunkTarget struct {
	cfg    *config.SplunkIntegration
	client *http.Client
}

func (s *splunkTarget) Name() string                  { return "splunk" }
func (s *splunkTarget) Accepts(eventName string) bool { return acceptsEvent(s.cfg.Events, eventName) }
func (s *splunkTarget) Dispatch(ctx context.Context, e Event) error {
	source := s.cfg.Source
	if source == "" {
		source = "vigo"
	}
	payload := map[string]any{
		"time":       e.Timestamp.Unix(),
		"host":       e.Hostname,
		"source":     source,
		"sourcetype": "vigo:event",
		"event": map[string]any{
			"name":      e.Name,
			"hostname":  e.Hostname,
			"envoy_id":  e.EnvoyID,
			"summary":   e.Summary,
			"details":   e.Details,
			"timestamp": e.Timestamp.Format(time.RFC3339),
		},
	}
	if s.cfg.Index != "" {
		payload["index"] = s.cfg.Index
	}
	headers := map[string]string{"Authorization": "Splunk " + s.cfg.Token}
	url := strings.TrimSuffix(s.cfg.URL, "/") + "/services/collector/event"
	return postJSON(ctx, s.client, url, headers, payload)
}

// --- Elasticsearch ---

type elasticTarget struct {
	cfg    *config.ElasticIntegration
	client *http.Client
}

func (e *elasticTarget) Name() string                  { return "elastic" }
func (e *elasticTarget) Accepts(eventName string) bool { return acceptsEvent(e.cfg.Events, eventName) }
func (e *elasticTarget) Dispatch(ctx context.Context, ev Event) error {
	index := e.cfg.Index
	if index == "" {
		index = "vigo-events"
	}
	doc := map[string]any{
		"@timestamp": ev.Timestamp.Format(time.RFC3339),
		"event":      ev.Name,
		"hostname":   ev.Hostname,
		"envoy_id":   ev.EnvoyID,
		"summary":    ev.Summary,
		"details":    ev.Details,
	}
	url := fmt.Sprintf("%s/%s/_doc", strings.TrimSuffix(e.cfg.URL, "/"), index)
	var headers map[string]string
	if e.cfg.APIKey != "" {
		headers = map[string]string{"Authorization": "ApiKey " + e.cfg.APIKey}
	}
	return postJSON(ctx, e.client, url, headers, doc)
}

// --- Datadog ---

type datadogTarget struct {
	cfg    *config.DatadogIntegration
	client *http.Client
}

func (d *datadogTarget) Name() string                  { return "datadog" }
func (d *datadogTarget) Accepts(eventName string) bool { return acceptsEvent(d.cfg.Events, eventName) }
func (d *datadogTarget) Dispatch(ctx context.Context, e Event) error {
	alertType := "info"
	if strings.Contains(e.Name, "failure") || strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		alertType = "error"
	} else if strings.Contains(e.Name, "drift") || strings.Contains(e.Name, "stale") {
		alertType = "warning"
	}

	payload := map[string]any{
		"title":      fmt.Sprintf("[Vigo] %s", e.Name),
		"text":       e.Summary,
		"host":       e.Hostname,
		"tags":       []string{"source:vigo", "event:" + e.Name},
		"alert_type": alertType,
		"date_happened": e.Timestamp.Unix(),
	}
	site := d.cfg.Site
	if site == "" {
		site = "datadoghq.com"
	}
	url := fmt.Sprintf("https://api.%s/api/v1/events", site)
	headers := map[string]string{"DD-API-KEY": d.cfg.APIKey}
	return postJSON(ctx, d.client, url, headers, payload)
}

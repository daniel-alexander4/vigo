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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/daemon"
)

// Severity levels for events — integrations can filter by minimum severity.
type Severity int

const (
	Info     Severity = iota // drift.detected, run.success, envoy.enrolled, secret.rotated
	Warning                  // run.failure, run.blocked, envoy.stale, envoy.offline
	Critical                 // security.*, convergence.threshold, config.reload.failure
)

// String returns the severity name for display and PagerDuty/Opsgenie mapping.
func (s Severity) String() string {
	switch s {
	case Critical:
		return "critical"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

// DefaultSeverity returns the default severity for a given event name.
func DefaultSeverity(eventName string) Severity {
	switch {
	case strings.HasPrefix(eventName, "security."):
		return Critical
	case eventName == "convergence.threshold" || eventName == "config.reload.failure":
		return Critical
	case eventName == "run.failure" || eventName == "run.blocked":
		return Warning
	case eventName == "envoy.stale" || eventName == "envoy.offline":
		return Warning
	case eventName == "envoy.revoked":
		return Warning
	case eventName == "resource.blocked":
		return Warning
	default:
		return Info
	}
}

// Event represents a Vigo event to be dispatched to integrations.
type Event struct {
	Name      string
	Timestamp time.Time
	Hostname  string
	EnvoyID   string
	Summary   string
	Details   map[string]any
	Severity  Severity
}

// suppressionWindow is the time window for grouping identical events.
const suppressionWindow = 60 * time.Second

// suppressionThreshold is the number of identical events before suppression kicks in.
const suppressionThreshold = 10

// dispatcherInflightCap bounds the number of integration-dispatch goroutines
// in flight at any moment across all targets. The Dispatcher fans an event
// out one goroutine per accepting target with no caller-side throttle, and
// the audit log fans through this same dispatcher (every Writer.Record()
// emits an audit.<eventType> event), so an audit burst × N accepting
// targets can spawn thousands of goroutines per second.
//
// Cap = 256 is well above realistic steady-state (≤30 targets × ~3 in-flight
// average ≈ 90) but bounds runaway. When saturated, Dispatch drops the
// per-target attempt with a warn log — caller never blocks (would pin the
// audit hot path). Matches the swarm/server.rs reject-on-saturation shape.
const dispatcherInflightCap = 256

// Dispatcher routes events to all configured and enabled integrations.
type Dispatcher struct {
	targets    []target
	client     *http.Client
	suppressor *eventSuppressor
	inflight   chan struct{} // bounded semaphore — see dispatcherInflightCap
}

// eventSuppressor tracks recent events for fleet-wide dedup.
type eventSuppressor struct {
	mu      sync.Mutex
	windows map[string]*suppressionEntry
}

type suppressionEntry struct {
	count     int
	firstSeen time.Time
	envoys    map[string]bool // unique envoy IDs
	flushed   bool            // true once the grouped notification was sent
}

func newEventSuppressor() *eventSuppressor {
	return &eventSuppressor{
		windows: make(map[string]*suppressionEntry),
	}
}

// check returns true if the event should be dispatched normally, false if suppressed.
// When suppression threshold is reached, returns a modified event with fleet-wide summary.
func (s *eventSuppressor) check(event *Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Per-envoy events only — fleet-wide events (no envoy) always pass through.
	if event.EnvoyID == "" {
		return true
	}

	key := event.Name
	entry, exists := s.windows[key]
	if !exists || time.Since(entry.firstSeen) > suppressionWindow {
		// Start new window.
		s.windows[key] = &suppressionEntry{
			count:     1,
			firstSeen: time.Now(),
			envoys:    map[string]bool{event.EnvoyID: true},
		}
		return true
	}

	entry.count++
	entry.envoys[event.EnvoyID] = true

	if entry.count >= suppressionThreshold && !entry.flushed {
		// Threshold reached — rewrite event as a grouped summary.
		entry.flushed = true
		event.Summary = fmt.Sprintf("%s across %d envoys", event.Name, len(entry.envoys))
		event.Hostname = fmt.Sprintf("%d envoys", len(entry.envoys))
		event.EnvoyID = ""
		event.Details["suppressed_count"] = entry.count
		event.Details["affected_envoys"] = len(entry.envoys)
		return true
	}

	if entry.count >= suppressionThreshold {
		// Already flushed the group notification — suppress individual events.
		return false
	}

	// Below threshold — dispatch normally.
	return true
}

type target interface {
	Name() string
	Accepts(event Event) bool
	Dispatch(ctx context.Context, event Event) error
}

// ParseSeverity converts a config string to a Severity value.
func ParseSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "critical":
		return Critical
	case "warning":
		return Warning
	default:
		return Info
	}
}

// acceptsWithSeverity checks event name filter AND minimum severity.
func acceptsWithSeverity(filter []string, minSeverity string, event Event) bool {
	if !acceptsEvent(filter, event.Name) {
		return false
	}
	if minSeverity != "" {
		return event.Severity >= ParseSeverity(minSeverity)
	}
	return true
}

// New creates a Dispatcher from the integrations config.
func New(cfg config.IntegrationsSection) *Dispatcher {
	d := &Dispatcher{
		client:     &http.Client{Timeout: 10 * time.Second},
		suppressor: newEventSuppressor(),
		inflight:   make(chan struct{}, dispatcherInflightCap),
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
		if t, err := newSentinelTarget(cfg.Sentinel, d.client); err != nil {
			slog.Error("integration init failed", "target", "sentinel", "error", err)
		} else {
			d.targets = append(d.targets, t)
		}
	}
	if cfg.Loki != nil && cfg.Loki.Enabled {
		d.targets = append(d.targets, &lokiTarget{cfg: cfg.Loki, client: d.client})
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

// Count returns the number of enabled integration targets.
func (d *Dispatcher) Count() int {
	return len(d.targets)
}

// Dispatch sends an event to all matching integrations asynchronously.
// If Severity is zero-value (Info), it's auto-assigned from DefaultSeverity.
// Fleet-wide suppression: when 10+ envoys fire the same event within 60s,
// individual events are suppressed and a single grouped notification is sent.
func (d *Dispatcher) Dispatch(event Event) {
	if event.Severity == 0 && event.Name != "" {
		event.Severity = DefaultSeverity(event.Name)
	}
	if event.Details == nil {
		event.Details = make(map[string]any)
	}
	if !d.suppressor.check(&event) {
		return // suppressed — group notification already sent
	}
	for _, t := range d.targets {
		if !t.Accepts(event) {
			continue
		}
		// Bound in-flight dispatch goroutines. Non-blocking acquire so the
		// audit hot path can't be pinned by a slow webhook target; the warn
		// log on saturation is the operator-visible signal.
		select {
		case d.inflight <- struct{}{}:
			// acquired permit
		default:
			slog.Warn("integration dispatch capacity exceeded — dropping",
				"target", t.Name(),
				"event", event.Name,
				"inflight_cap", cap(d.inflight),
			)
			continue
		}
		go func(tgt target) {
			defer func() { <-d.inflight }()
			defer daemon.Recover("integration-dispatch "+tgt.Name()+" event="+event.Name, nil)
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

// DispatchRotation implements the watcher.EventDispatcher interface for secret
// rotation events. Converts the rotation-specific parameters into a standard Event.
func (d *Dispatcher) DispatchRotation(ctx context.Context, event string, secretPath string, affected int) {
	d.Dispatch(Event{
		Name:      event,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("secret %s rotated, %d envoys affected", secretPath, affected),
		Details: map[string]any{
			"path":     secretPath,
			"affected": affected,
		},
	})
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

func (s *slackTarget) Name() string { return "slack" }
func (s *slackTarget) Accepts(event Event) bool {
	return acceptsWithSeverity(s.cfg.Events, s.cfg.MinSeverity, event)
}
func (s *slackTarget) Dispatch(ctx context.Context, e Event) error {
	color := "#36a64f" // green (info)
	switch e.Severity {
	case Critical:
		color = "#cc0000"
	case Warning:
		color = "#f2c744"
	}

	payload := map[string]any{
		"attachments": []map[string]any{{
			"color": color,
			"title": fmt.Sprintf("[Vigo] %s", e.Name),
			"text":  e.Summary,
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

func (p *pagerdutyTarget) Name() string { return "pagerduty" }
func (p *pagerdutyTarget) Accepts(event Event) bool {
	return acceptsWithSeverity(p.cfg.Events, p.cfg.MinSeverity, event)
}
func (p *pagerdutyTarget) Dispatch(ctx context.Context, e Event) error {
	severity := e.Severity.String()
	if severity == "info" {
		severity = "warning" // PagerDuty doesn't have "info" severity
	}

	// Determine event action: resolve events clear previous incidents.
	action := "trigger"
	if p.cfg.AutoResolve && isResolveEvent(e.Name) {
		action = "resolve"
	}

	// Dedup key groups related events into a single incident.
	// Same error class across multiple envoys → one incident, not N.
	dedupKey := pagerdutyDedupKey(e)

	payload := map[string]any{
		"routing_key":  p.cfg.RoutingKey,
		"event_action": action,
		"dedup_key":    dedupKey,
		"payload": map[string]any{
			"summary":        fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Summary),
			"source":         e.Hostname,
			"severity":       severity,
			"timestamp":      e.Timestamp.Format(time.RFC3339),
			"custom_details": e.Details,
		},
	}
	return postJSON(ctx, p.client, "https://events.pagerduty.com/v2/enqueue", nil, payload)
}

// pagerdutyDedupKey generates a dedup key for PagerDuty incident grouping.
// Events with the same dedup key are grouped into a single incident.
func pagerdutyDedupKey(e Event) string {
	// For resource.blocked events, group by error class (not per-envoy).
	if e.Name == "resource.blocked" {
		configcrate, _ := e.Details["configcrate"].(string)
		resource, _ := e.Details["resource"].(string)
		return fmt.Sprintf("vigo:%s:%s:%s", e.Name, configcrate, resource)
	}
	// For security events, group by event type (one incident per threat type).
	if strings.HasPrefix(e.Name, "security.") {
		return fmt.Sprintf("vigo:%s", e.Name)
	}
	// For fleet-wide events (no hostname), use event name only.
	if e.Hostname == "" {
		return fmt.Sprintf("vigo:%s", e.Name)
	}
	// Default: per-envoy, per-event.
	return fmt.Sprintf("vigo:%s:%s", e.Name, e.EnvoyID)
}

// isResolveEvent returns true for events that indicate a previous issue has cleared.
func isResolveEvent(name string) bool {
	return name == "envoy.online" || name == "resource.unblocked"
}

// --- Opsgenie ---

type opsgenieTarget struct {
	cfg    *config.OpsgenieIntegration
	client *http.Client
}

func (o *opsgenieTarget) Name() string { return "opsgenie" }
func (o *opsgenieTarget) Accepts(event Event) bool {
	return acceptsWithSeverity(o.cfg.Events, o.cfg.MinSeverity, event)
}
func (o *opsgenieTarget) Dispatch(ctx context.Context, e Event) error {
	priority := "P3"
	switch e.Severity {
	case Critical:
		priority = "P1"
	case Warning:
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

func (t *teamsTarget) Name() string { return "teams" }
func (t *teamsTarget) Accepts(event Event) bool {
	return acceptsWithSeverity(t.cfg.Events, t.cfg.MinSeverity, event)
}
func (t *teamsTarget) Dispatch(ctx context.Context, e Event) error {
	color := "00cc00"
	switch e.Severity {
	case Critical:
		color = "cc0000"
	case Warning:
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

func (c *connectwiseTarget) Name() string             { return "connectwise" }
func (c *connectwiseTarget) Accepts(event Event) bool { return acceptsEvent(c.cfg.Events, event.Name) }
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

// basicAuth returns the base64 of "user:pass"-style credentials for an
// HTTP Basic Authorization header.
func basicAuth(creds string) string {
	return base64.StdEncoding.EncodeToString([]byte(creds))
}

// --- Splunk HEC ---

type splunkTarget struct {
	cfg    *config.SplunkIntegration
	client *http.Client
}

func (s *splunkTarget) Name() string             { return "splunk" }
func (s *splunkTarget) Accepts(event Event) bool { return acceptsEvent(s.cfg.Events, event.Name) }
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

func (e *elasticTarget) Name() string             { return "elastic" }
func (e *elasticTarget) Accepts(event Event) bool { return acceptsEvent(e.cfg.Events, event.Name) }
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

func (d *datadogTarget) Name() string             { return "datadog" }
func (d *datadogTarget) Accepts(event Event) bool { return acceptsEvent(d.cfg.Events, event.Name) }
func (d *datadogTarget) Dispatch(ctx context.Context, e Event) error {
	alertType := e.Severity.String()
	if alertType == "critical" {
		alertType = "error" // Datadog uses "error" not "critical"
	}

	payload := map[string]any{
		"title":         fmt.Sprintf("[Vigo] %s", e.Name),
		"text":          e.Summary,
		"host":          e.Hostname,
		"tags":          []string{"source:vigo", "event:" + e.Name},
		"alert_type":    alertType,
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

// --- Grafana Loki ---

type lokiTarget struct {
	cfg    *config.LokiIntegration
	client *http.Client
}

func (l *lokiTarget) Name() string             { return "loki" }
func (l *lokiTarget) Accepts(event Event) bool { return acceptsEvent(l.cfg.Events, event.Name) }
func (l *lokiTarget) Dispatch(ctx context.Context, e Event) error {
	// Body carries the high-cardinality fields; labels stay low-cardinality
	// (service, severity, event_name) per Loki best practice.
	body, err := json.Marshal(map[string]any{
		"hostname": e.Hostname,
		"envoy_id": e.EnvoyID,
		"summary":  e.Summary,
		"details":  e.Details,
	})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	stream := map[string]string{
		"service":    "vigo",
		"severity":   e.Severity.String(),
		"event_name": e.Name,
	}
	payload := map[string]any{
		"streams": []map[string]any{{
			"stream": stream,
			"values": [][]string{{
				fmt.Sprintf("%d", e.Timestamp.UnixNano()),
				string(body),
			}},
		}},
	}

	headers := map[string]string{}
	if l.cfg.TenantID != "" {
		headers["X-Scope-OrgID"] = l.cfg.TenantID
	}
	if l.cfg.BearerToken != "" {
		headers["Authorization"] = "Bearer " + l.cfg.BearerToken
	} else if l.cfg.Username != "" {
		headers["Authorization"] = "Basic " + basicAuth(l.cfg.Username+":"+l.cfg.Password)
	}

	url := strings.TrimSuffix(l.cfg.URL, "/") + "/loki/api/v1/push"
	return postJSON(ctx, l.client, url, headers, payload)
}

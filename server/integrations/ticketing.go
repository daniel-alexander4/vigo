// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/daniel-alexander4/vigo/server/config"
)

// --- Jira ---

type jiraTarget struct {
	cfg    *config.JiraIntegration
	client *http.Client
}

func (j *jiraTarget) Name() string                  { return "jira" }
func (j *jiraTarget) Accepts(eventName string) bool { return acceptsEvent(j.cfg.Events, eventName) }
func (j *jiraTarget) Dispatch(ctx context.Context, e Event) error {
	issueType := j.cfg.IssueType
	if issueType == "" {
		issueType = "Task"
	}
	payload := map[string]any{
		"fields": map[string]any{
			"project":   map[string]string{"key": j.cfg.Project},
			"summary":   fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
			"issuetype": map[string]string{"name": issueType},
			"description": map[string]any{
				"type":    "doc",
				"version": 1,
				"content": []map[string]any{{
					"type": "paragraph",
					"content": []map[string]any{{
						"type": "text",
						"text": fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s",
							e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
					}},
				}},
			},
		},
	}
	url := strings.TrimSuffix(j.cfg.URL, "/") + "/rest/api/3/issue"
	auth := basicAuth(j.cfg.Email + ":" + j.cfg.APIToken)
	headers := map[string]string{"Authorization": "Basic " + auth}
	return postJSON(ctx, j.client, url, headers, payload)
}

// --- Zendesk ---

type zendeskTarget struct {
	cfg    *config.ZendeskIntegration
	client *http.Client
}

func (z *zendeskTarget) Name() string                  { return "zendesk" }
func (z *zendeskTarget) Accepts(eventName string) bool { return acceptsEvent(z.cfg.Events, eventName) }
func (z *zendeskTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"ticket": map[string]any{
			"subject": fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
			"comment": map[string]string{
				"body": fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s",
					e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
			},
			"priority": zendeskPriority(e.Name),
		},
	}
	url := fmt.Sprintf("https://%s.zendesk.com/api/v2/tickets.json", z.cfg.Subdomain)
	auth := basicAuth(z.cfg.Email + "/token:" + z.cfg.APIToken)
	headers := map[string]string{"Authorization": "Basic " + auth}
	return postJSON(ctx, z.client, url, headers, payload)
}

func zendeskPriority(eventName string) string {
	if strings.Contains(eventName, "rootkit") || strings.Contains(eventName, "malware") {
		return "urgent"
	}
	if strings.Contains(eventName, "failure") || strings.Contains(eventName, "cve_critical") {
		return "high"
	}
	return "normal"
}

// --- Freshservice ---

type freshserviceTarget struct {
	cfg    *config.FreshserviceIntegration
	client *http.Client
}

func (f *freshserviceTarget) Name() string                  { return "freshservice" }
func (f *freshserviceTarget) Accepts(eventName string) bool { return acceptsEvent(f.cfg.Events, eventName) }
func (f *freshserviceTarget) Dispatch(ctx context.Context, e Event) error {
	priority := 2 // medium
	if strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		priority = 4 // urgent
	} else if strings.Contains(e.Name, "failure") {
		priority = 3 // high
	}

	payload := map[string]any{
		"subject":     fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"description": fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s", e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"priority":    priority,
		"status":      2, // open
	}
	url := fmt.Sprintf("https://%s/api/v2/tickets", f.cfg.Domain)
	auth := basicAuth(f.cfg.APIKey + ":X")
	headers := map[string]string{"Authorization": "Basic " + auth}
	return postJSON(ctx, f.client, url, headers, payload)
}

// --- Linear ---

type linearTarget struct {
	cfg    *config.LinearIntegration
	client *http.Client
}

func (l *linearTarget) Name() string                  { return "linear" }
func (l *linearTarget) Accepts(eventName string) bool { return acceptsEvent(l.cfg.Events, eventName) }
func (l *linearTarget) Dispatch(ctx context.Context, e Event) error {
	priority := 3 // medium
	if strings.Contains(e.Name, "rootkit") || strings.Contains(e.Name, "malware") {
		priority = 1 // urgent
	} else if strings.Contains(e.Name, "failure") {
		priority = 2 // high
	}

	// Linear uses GraphQL API.
	query := `mutation CreateIssue($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url } } }`
	payload := map[string]any{
		"query": query,
		"variables": map[string]any{
			"input": map[string]any{
				"teamId":      l.cfg.TeamID,
				"title":       fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
				"description": fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s", e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
				"priority":    priority,
			},
		},
	}
	headers := map[string]string{"Authorization": l.cfg.APIKey}
	return postJSON(ctx, l.client, "https://api.linear.app/graphql", headers, payload)
}

// --- GitHub Issues ---

type githubIssuesTarget struct {
	cfg    *config.GitHubIssuesIntegration
	client *http.Client
}

func (g *githubIssuesTarget) Name() string                  { return "github_issues" }
func (g *githubIssuesTarget) Accepts(eventName string) bool { return acceptsEvent(g.cfg.Events, eventName) }
func (g *githubIssuesTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"title": fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"body": fmt.Sprintf("Vigo detected **%s** on `%s` at %s.\n\n%s",
			e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
	}
	if len(g.cfg.Labels) > 0 {
		payload["labels"] = g.cfg.Labels
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues", g.cfg.Owner, g.cfg.Repo)
	headers := map[string]string{
		"Authorization": "Bearer " + g.cfg.Token,
		"Accept":        "application/vnd.github+json",
	}
	return postJSON(ctx, g.client, url, headers, payload)
}

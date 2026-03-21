// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package smtp sends email notifications for server events via an SMTP relay.
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/daniel-alexander4/vigo/server/metrics"
)

// Event mirrors webhook.EventPayload but is decoupled from that package.
type Event struct {
	Name      string
	Timestamp time.Time
	Envoy    *EnvoyInfo
	Run       *RunInfo
	Details   map[string]any
}

// EnvoyInfo contains envoy metadata for email templates.
type EnvoyInfo struct {
	ID       string
	Hostname string
	IP       string
}

// RunInfo contains run metadata for email templates.
type RunInfo struct {
	ID      string
	Status  string
	Changed int32
	Failed  int32
}

// AllEvents lists every event name the SMTP notifier recognises.
// Used for documentation and config validation.
var AllEvents = []string{
	"envoy.enrolled",
	"run.success",
	"run.failure",
	"drift.detected",
	"secret.rotated",
	"config.reload.failure",
	"envoy.stale",
	"compliance.threshold",
	"security.rootkit",
	"security.malware",
	"security.integrity",
	"security.cve_critical",
	"security.hardening_drop",
}

// EventOverride holds per-event notification settings.
type EventOverride struct {
	Recipients []string // override default recipients (empty = use default)
}

// DigestConfig controls batching of notifications into periodic summaries.
type DigestConfig struct {
	Interval time.Duration
	Events   map[string]bool // events routed to digest instead of immediate
}

// Config holds SMTP connection and notification settings.
type Config struct {
	Host       string
	Port       int
	From       string
	Username   string
	Password   string
	TLS        bool
	Recipients []string                  // default recipient list
	Events     map[string]*EventOverride // nil = all events enabled; key present = enabled
	Digest     *DigestConfig
}

// Notifier sends email notifications for server events.
type Notifier struct {
	cfg  Config
	tmpl *template.Template

	mu        sync.Mutex
	digestBuf []Event
}

// NewNotifier creates a Notifier. Returns nil if host is empty (disabled).
func NewNotifier(cfg Config) *Notifier {
	if cfg.Host == "" {
		return nil
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	return &Notifier{
		cfg:  cfg,
		tmpl: emailTemplates,
	}
}

// Enabled returns true if the notifier is configured and active.
func (n *Notifier) Enabled() bool {
	return n != nil
}

// Start runs the digest flush loop. Call in a goroutine.
// Returns immediately if digest is not configured.
func (n *Notifier) Start(ctx context.Context) {
	if n == nil || n.cfg.Digest == nil || n.cfg.Digest.Interval <= 0 {
		return
	}
	slog.Info("smtp: digest mode enabled", "interval", n.cfg.Digest.Interval, "events", len(n.cfg.Digest.Events))

	ticker := time.NewTicker(n.cfg.Digest.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			n.flushDigest()
			return
		case <-ticker.C:
			n.flushDigest()
		}
	}
}

// Notify sends an email for the given event. It is non-blocking — errors are
// logged but never returned to the caller. Events configured for digest mode
// are buffered and sent as a periodic summary.
func (n *Notifier) Notify(event Event) {
	if n == nil {
		return
	}

	// Check event filter.
	if n.cfg.Events != nil {
		if _, ok := n.cfg.Events[event.Name]; !ok {
			return
		}
	}

	recipients := n.recipientsFor(event.Name)
	if len(recipients) == 0 {
		return
	}

	// Route to digest buffer if configured.
	if n.cfg.Digest != nil && n.cfg.Digest.Events[event.Name] {
		n.mu.Lock()
		n.digestBuf = append(n.digestBuf, event)
		n.mu.Unlock()
		return
	}

	go func() {
		if err := n.send(event, recipients); err != nil {
			metrics.SMTPSendsTotal.WithLabelValues(event.Name, "error").Inc()
			slog.Error("smtp: failed to send notification",
				"event", event.Name,
				"error", err,
			)
		} else {
			metrics.SMTPSendsTotal.WithLabelValues(event.Name, "success").Inc()
			slog.Info("smtp: notification sent",
				"event", event.Name,
				"recipients", len(recipients),
			)
		}
	}()
}

// recipientsFor returns the recipient list for a given event name.
// Per-event overrides take precedence over default recipients.
func (n *Notifier) recipientsFor(eventName string) []string {
	if n.cfg.Events != nil {
		if override, ok := n.cfg.Events[eventName]; ok && override != nil && len(override.Recipients) > 0 {
			return override.Recipients
		}
	}
	return n.cfg.Recipients
}

// send renders the email and delivers it via SMTP.
func (n *Notifier) send(event Event, recipients []string) error {
	subject, body, err := n.render(event)
	if err != nil {
		return fmt.Errorf("render template: %w", err)
	}

	msg := buildMessage(n.cfg.From, recipients, subject, body)
	return n.deliver(recipients, msg)
}

// deliver sends a pre-built email message via SMTP.
func (n *Notifier) deliver(recipients []string, msg []byte) error {
	addr := net.JoinHostPort(n.cfg.Host, fmt.Sprintf("%d", n.cfg.Port))

	var auth smtp.Auth
	if n.cfg.Username != "" {
		auth = smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.Host)
	}

	if n.cfg.TLS {
		return sendWithSTARTTLS(addr, n.cfg.Host, auth, n.cfg.From, recipients, msg)
	}
	return smtp.SendMail(addr, auth, n.cfg.From, recipients, msg)
}

// flushDigest sends digest emails summarizing all buffered events,
// grouped by recipient set so per-event overrides are respected.
func (n *Notifier) flushDigest() {
	n.mu.Lock()
	events := n.digestBuf
	n.digestBuf = nil
	n.mu.Unlock()

	if len(events) == 0 {
		return
	}

	// Group events by resolved recipient set.
	type group struct {
		recipients []string
		events     []Event
	}
	groups := make(map[string]*group)

	for _, e := range events {
		rcpt := n.recipientsFor(e.Name)
		key := strings.Join(rcpt, ",")
		g, ok := groups[key]
		if !ok {
			g = &group{recipients: rcpt}
			groups[key] = g
		}
		g.events = append(g.events, e)
	}

	for _, g := range groups {
		subject, body := n.renderDigest(g.events)
		msg := buildMessage(n.cfg.From, g.recipients, subject, body)
		if err := n.deliver(g.recipients, msg); err != nil {
			slog.Error("smtp: failed to send digest",
				"events", len(g.events),
				"error", err,
			)
		} else {
			slog.Info("smtp: digest sent",
				"events", len(g.events),
				"recipients", len(g.recipients),
			)
		}
	}
}

// renderDigest builds a summary email for a batch of events.
func (n *Notifier) renderDigest(events []Event) (subject, body string) {
	counts := make(map[string]int)
	for _, e := range events {
		counts[e.Name]++
	}

	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)

	data := struct {
		Events []Event
		Counts map[string]int
		Types  []string
		Total  int
	}{events, counts, types, len(events)}

	var buf bytes.Buffer
	if err := n.tmpl.ExecuteTemplate(&buf, "digest", data); err != nil {
		return fmt.Sprintf("[Vigo] Digest: %d events", len(events)),
			fmt.Sprintf("%d events in this digest period.", len(events))
	}

	text := buf.String()
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+1:])
	}
	return text, ""
}

// sendWithSTARTTLS connects to the SMTP server and upgrades to TLS.
func sendWithSTARTTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer c.Close()

	if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}

	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// render executes the appropriate template for the event.
func (n *Notifier) render(event Event) (subject, body string, err error) {
	// Try event-specific template, fall back to default.
	tmplName := event.Name
	if n.tmpl.Lookup(tmplName) == nil {
		tmplName = "default"
	}

	var buf bytes.Buffer
	if err := n.tmpl.ExecuteTemplate(&buf, tmplName, event); err != nil {
		return "", "", err
	}

	// First line is the subject, rest is the body.
	text := buf.String()
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+1:]), nil
	}
	return text, "", nil
}

// buildMessage constructs an RFC 2822 email message.
func buildMessage(from string, to []string, subject, body string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)
	return buf.Bytes()
}

// Package smtp sends email notifications for server events via an SMTP relay.
package smtp

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"text/template"
	"time"
)

// Event mirrors webhook.EventPayload but is decoupled from that package.
type Event struct {
	Name      string
	Timestamp time.Time
	Thrall    *ThrallInfo
	Run       *RunInfo
	Details   map[string]any
}

// ThrallInfo contains thrall metadata for email templates.
type ThrallInfo struct {
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

// Config holds SMTP connection and notification settings.
type Config struct {
	Host       string
	Port       int
	From       string
	Username   string
	Password   string
	TLS        bool
	Recipients []string
	Events     map[string]bool // nil = all events enabled
}

// Notifier sends email notifications for server events.
type Notifier struct {
	cfg  Config
	tmpl *template.Template
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

// Notify sends an email for the given event. It is non-blocking — errors are
// logged but never returned to the caller.
func (n *Notifier) Notify(event Event) {
	if n == nil {
		return
	}

	// Check event filter.
	if n.cfg.Events != nil && !n.cfg.Events[event.Name] {
		return
	}

	if len(n.cfg.Recipients) == 0 {
		return
	}

	go func() {
		if err := n.send(event); err != nil {
			slog.Error("smtp: failed to send notification",
				"event", event.Name,
				"error", err,
			)
		} else {
			slog.Info("smtp: notification sent",
				"event", event.Name,
				"recipients", len(n.cfg.Recipients),
			)
		}
	}()
}

// send renders the email and delivers it via SMTP.
func (n *Notifier) send(event Event) error {
	subject, body, err := n.render(event)
	if err != nil {
		return fmt.Errorf("render template: %w", err)
	}

	msg := buildMessage(n.cfg.From, n.cfg.Recipients, subject, body)

	addr := net.JoinHostPort(n.cfg.Host, fmt.Sprintf("%d", n.cfg.Port))

	var auth smtp.Auth
	if n.cfg.Username != "" {
		auth = smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.Host)
	}

	if n.cfg.TLS {
		return sendWithSTARTTLS(addr, n.cfg.Host, auth, n.cfg.From, n.cfg.Recipients, msg)
	}
	return smtp.SendMail(addr, auth, n.cfg.From, n.cfg.Recipients, msg)
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

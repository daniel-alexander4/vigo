// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package smtp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewNotifier_DisabledWhenNoHost(t *testing.T) {
	n := NewNotifier(Config{})
	if n != nil {
		t.Fatal("expected nil notifier when host is empty")
	}
	if n.Enabled() {
		t.Fatal("nil notifier should not be enabled")
	}
	// Should not panic.
	n.Notify(Event{Name: "test"})
}

func TestNewNotifier_DefaultPort(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	if n.cfg.Port != 587 {
		t.Errorf("expected default port 587, got %d", n.cfg.Port)
	}
}

func TestRender_EnrolledEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "thrall.enrolled",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Thrall: &ThrallInfo{
			ID:       "abc-123",
			Hostname: "web01.example.com",
			IP:       "10.0.0.1",
		},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "web01.example.com") {
		t.Errorf("subject should contain hostname: %s", subject)
	}
	if !strings.Contains(body, "abc-123") {
		t.Errorf("body should contain thrall ID: %s", body)
	}
}

func TestRender_RunFailureEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "run.failure",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Thrall:    &ThrallInfo{ID: "abc", Hostname: "db01"},
		Run:       &RunInfo{ID: "run-1", Status: "failure", Changed: 2, Failed: 1},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "FAILED") {
		t.Errorf("subject should contain FAILED: %s", subject)
	}
	if !strings.Contains(body, "Failed:   1") {
		t.Errorf("body should contain failed count: %s", body)
	}
}

func TestRender_DriftEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "drift.detected",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Thrall:    &ThrallInfo{ID: "abc", Hostname: "web01"},
		Run:       &RunInfo{ID: "run-1", Status: "success", Changed: 3},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "Drift") {
		t.Errorf("subject should contain Drift: %s", subject)
	}
	if !strings.Contains(body, "Changed:  3") {
		t.Errorf("body should contain changed count: %s", body)
	}
}

func TestRender_DefaultTemplate(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "some.unknown.event",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "some.unknown.event") {
		t.Errorf("subject should contain event name: %s", subject)
	}
	if !strings.Contains(body, "some.unknown.event") {
		t.Errorf("body should contain event name: %s", body)
	}
}

func TestRender_SecretRotatedEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "secret.rotated",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Details: map[string]any{
			"path":     "hobnob/db/dsn",
			"affected": 3,
		},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "hobnob/db/dsn") {
		t.Errorf("subject should contain secret path: %s", subject)
	}
	if !strings.Contains(body, "3 thrall(s)") {
		t.Errorf("body should contain affected count: %s", body)
	}
}

func TestRender_ConfigReloadFailureEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "config.reload.failure",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Details: map[string]any{
			"error": "modules/nginx.hnb: duplicate key 'resources'",
		},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "Config reload failed") {
		t.Errorf("subject should indicate failure: %s", subject)
	}
	if !strings.Contains(body, "duplicate key") {
		t.Errorf("body should contain error message: %s", body)
	}
}

func TestRender_ThrallStaleEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "thrall.stale",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Thrall:    &ThrallInfo{ID: "abc-123", Hostname: "web01.example.com", IP: "10.0.0.1"},
		Details: map[string]any{
			"last_seen": "2026-03-10 11:45:00 UTC",
		},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "web01.example.com") {
		t.Errorf("subject should contain hostname: %s", subject)
	}
	if !strings.Contains(body, "11:45:00") {
		t.Errorf("body should contain last seen time: %s", body)
	}
}

func TestRender_ComplianceThresholdEvent(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	event := Event{
		Name:      "compliance.threshold",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Details: map[string]any{
			"compliant_pct": 75,
			"threshold":     90,
			"compliant":     15,
			"total":         20,
		},
	}

	subject, body, err := n.render(event)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(subject, "75%") {
		t.Errorf("subject should contain percentage: %s", subject)
	}
	if !strings.Contains(body, "15/20") {
		t.Errorf("body should contain compliant/total: %s", body)
	}
	if !strings.Contains(body, "90%") {
		t.Errorf("body should contain threshold: %s", body)
	}
}

func TestAllEventsHaveTemplates(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	for _, eventName := range AllEvents {
		if n.tmpl.Lookup(eventName) == nil {
			t.Errorf("AllEvents lists %q but no template defined", eventName)
		}
	}
}

func TestEventFilter(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Events: map[string]*EventOverride{
			"run.failure": {},
		},
	})

	// run.failure is in the filter.
	if _, ok := n.cfg.Events["run.failure"]; !ok {
		t.Error("run.failure should be enabled")
	}

	// run.success is not in the filter.
	if _, ok := n.cfg.Events["run.success"]; ok {
		t.Error("run.success should not be enabled")
	}
}

func TestPerEventRecipients(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Events: map[string]*EventOverride{
			"run.failure":    {Recipients: []string{"ops@example.com", "oncall@example.com"}},
			"run.success":    {},
			"thrall.enrolled": nil,
		},
	})

	// run.failure has per-event recipients.
	got := n.recipientsFor("run.failure")
	if len(got) != 2 || got[0] != "ops@example.com" {
		t.Errorf("run.failure should use override recipients, got %v", got)
	}

	// run.success has empty override — falls back to default.
	got = n.recipientsFor("run.success")
	if len(got) != 1 || got[0] != "admin@example.com" {
		t.Errorf("run.success should use default recipients, got %v", got)
	}

	// thrall.enrolled has nil override — falls back to default.
	got = n.recipientsFor("thrall.enrolled")
	if len(got) != 1 || got[0] != "admin@example.com" {
		t.Errorf("thrall.enrolled should use default recipients, got %v", got)
	}
}

func TestPerEventRecipients_AllEventsEnabled(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"default@example.com"},
		Events:     nil, // all events enabled
	})

	got := n.recipientsFor("run.failure")
	if len(got) != 1 || got[0] != "default@example.com" {
		t.Errorf("should use default recipients when Events is nil, got %v", got)
	}
}

func TestDigest_BuffersEvents(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Digest: &DigestConfig{
			Interval: time.Hour,
			Events:   map[string]bool{"run.success": true, "drift.detected": true},
		},
	})

	// Digest events should be buffered, not sent immediately.
	n.Notify(Event{
		Name:      "run.success",
		Timestamp: time.Now(),
		Thrall:    &ThrallInfo{ID: "a", Hostname: "web01"},
		Run:       &RunInfo{ID: "r1", Status: "success"},
	})
	n.Notify(Event{
		Name:      "drift.detected",
		Timestamp: time.Now(),
		Thrall:    &ThrallInfo{ID: "b", Hostname: "web02"},
		Run:       &RunInfo{ID: "r2", Status: "success", Changed: 2},
	})

	n.mu.Lock()
	count := len(n.digestBuf)
	n.mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 buffered events, got %d", count)
	}
}

func TestDigest_NonDigestEventsSentImmediately(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Digest: &DigestConfig{
			Interval: time.Hour,
			Events:   map[string]bool{"run.success": true},
		},
	})

	// run.failure is NOT in digest — should not be buffered.
	n.Notify(Event{
		Name:      "run.failure",
		Timestamp: time.Now(),
		Thrall:    &ThrallInfo{ID: "a", Hostname: "web01"},
		Run:       &RunInfo{ID: "r1", Status: "failure", Failed: 1},
	})

	n.mu.Lock()
	count := len(n.digestBuf)
	n.mu.Unlock()

	if count != 0 {
		t.Errorf("non-digest event should not be buffered, got %d", count)
	}
}

func TestDigest_FlushClearsBuffer(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Digest: &DigestConfig{
			Interval: time.Hour,
			Events:   map[string]bool{"run.success": true},
		},
	})

	n.mu.Lock()
	n.digestBuf = []Event{
		{Name: "run.success", Timestamp: time.Now(), Thrall: &ThrallInfo{Hostname: "web01"}},
		{Name: "run.success", Timestamp: time.Now(), Thrall: &ThrallInfo{Hostname: "web02"}},
	}
	n.mu.Unlock()

	// flushDigest will fail to deliver (no SMTP server) but should clear buffer.
	n.flushDigest()

	n.mu.Lock()
	count := len(n.digestBuf)
	n.mu.Unlock()

	if count != 0 {
		t.Errorf("expected buffer cleared after flush, got %d", count)
	}
}

func TestDigest_EmptyFlushIsNoop(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Digest: &DigestConfig{
			Interval: time.Hour,
			Events:   map[string]bool{"run.success": true},
		},
	})

	// Should not panic or send anything.
	n.flushDigest()
}

func TestDigest_RenderDigest(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})
	events := []Event{
		{Name: "run.success", Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), Thrall: &ThrallInfo{Hostname: "web01"}},
		{Name: "run.success", Timestamp: time.Date(2026, 3, 10, 12, 5, 0, 0, time.UTC), Thrall: &ThrallInfo{Hostname: "web02"}},
		{Name: "drift.detected", Timestamp: time.Date(2026, 3, 10, 12, 10, 0, 0, time.UTC), Thrall: &ThrallInfo{Hostname: "db01"}},
	}

	subject, body := n.renderDigest(events)

	if !strings.Contains(subject, "3 events") {
		t.Errorf("subject should contain event count: %s", subject)
	}
	if !strings.Contains(body, "run.success: 2") {
		t.Errorf("body should contain run.success count: %s", body)
	}
	if !strings.Contains(body, "drift.detected: 1") {
		t.Errorf("body should contain drift.detected count: %s", body)
	}
	if !strings.Contains(body, "web01") {
		t.Errorf("body should contain thrall hostnames: %s", body)
	}
}

func TestDigest_StartReturnsImmediatelyWhenDisabled(t *testing.T) {
	n := NewNotifier(Config{Host: "smtp.example.com"})

	// Should return immediately — no digest configured.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		n.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Good — returned immediately.
	case <-time.After(200 * time.Millisecond):
		t.Error("Start should return immediately when digest is not configured")
	}
}

func TestBuildMessage(t *testing.T) {
	msg := buildMessage(
		"hobnob@example.com",
		[]string{"admin@example.com", "ops@example.com"},
		"[Hobnob] Test",
		"Hello, world.",
	)

	s := string(msg)
	if !strings.Contains(s, "From: hobnob@example.com") {
		t.Error("missing From header")
	}
	if !strings.Contains(s, "To: admin@example.com, ops@example.com") {
		t.Error("missing To header")
	}
	if !strings.Contains(s, "Subject: [Hobnob] Test") {
		t.Error("missing Subject header")
	}
	if !strings.Contains(s, "Hello, world.") {
		t.Error("missing body")
	}
}

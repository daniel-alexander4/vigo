package smtp

import (
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

func TestEventFilter(t *testing.T) {
	n := NewNotifier(Config{
		Host:       "smtp.example.com",
		Recipients: []string{"admin@example.com"},
		Events:     map[string]bool{"run.failure": true},
	})

	// run.failure is allowed.
	if n.cfg.Events != nil && !n.cfg.Events["run.failure"] {
		t.Error("run.failure should be enabled")
	}

	// run.success is not in the filter.
	if n.cfg.Events != nil && n.cfg.Events["run.success"] {
		t.Error("run.success should not be enabled")
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

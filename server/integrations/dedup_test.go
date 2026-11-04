// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package integrations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// recordingTarget records every event it is asked to dispatch.
type recordingTarget struct {
	name string
	fail bool
	mu   sync.Mutex
	got  []Event
	wg   sync.WaitGroup
}

func (r *recordingTarget) Name() string         { return r.name }
func (r *recordingTarget) Accepts(_ Event) bool { return true }
func (r *recordingTarget) Dispatch(_ context.Context, e Event) error {
	defer r.wg.Done()
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	if r.fail {
		return errors.New("boom")
	}
	return nil
}

func (r *recordingTarget) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.got...)
}

// recordingTicketTarget is a recordingTarget that opens tickets.
type recordingTicketTarget struct{ recordingTarget }

func (r *recordingTicketTarget) opensTicket() {}

func newTestDispatcher(targets ...target) *Dispatcher {
	return &Dispatcher{
		targets:    targets,
		suppressor: newEventSuppressor(),
		tickets:    newTicketDedup(),
		inflight:   make(chan struct{}, dispatcherInflightCap),
	}
}

// dispatchWait dispatches and waits until rec has run expect more times.
func dispatchWait(t *testing.T, d *Dispatcher, rec *recordingTarget, expect int, e Event) {
	t.Helper()
	rec.wg.Add(expect)
	d.Dispatch(e)
	done := make(chan struct{})
	go func() { rec.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("dispatch of %s did not reach the target", e.Name)
	}
}

func TestDispatch_DoesNotMutateCallerDetails(t *testing.T) {
	rec := &recordingTarget{name: "rec"}
	d := newTestDispatcher(rec)
	callerMaps := make([]map[string]any, suppressionThreshold)
	for i := range callerMaps {
		callerMaps[i] = map[string]any{"k": "v"}
		dispatchWait(t, d, rec, 1, Event{Name: "run.failure", EnvoyID: fmt.Sprintf("e%d", i), Details: callerMaps[i]})
	}
	for i, m := range callerMaps {
		if len(m) != 1 {
			t.Fatalf("caller's Details map %d was mutated by Dispatch: %v", i, m)
		}
	}
	evs := rec.events()
	if last := evs[len(evs)-1]; last.Details["suppressed_count"] == nil {
		t.Fatal("grouped summary lost its suppressed_count detail")
	}
}

func TestDispatch_GroupedSummaryKeepsDedupKey(t *testing.T) {
	rec := &recordingTarget{name: "rec"}
	d := newTestDispatcher(rec)
	for i := 0; i < suppressionThreshold; i++ {
		dispatchWait(t, d, rec, 1, Event{Name: "envoy.offline", EnvoyID: fmt.Sprintf("e%d", i), Hostname: "h"})
	}
	evs := rec.events()
	last := evs[len(evs)-1]
	want := fmt.Sprintf("vigo:envoy.reachability:e%d", suppressionThreshold-1)
	if last.DedupKey != want {
		t.Fatalf("grouped summary dedup key = %q, want %q (stable across suppression)", last.DedupKey, want)
	}
}

func TestIncidentKey_ResolveMatchesTrigger(t *testing.T) {
	online := incidentKey(Event{Name: "envoy.online", EnvoyID: "e1"})
	for _, trig := range []string{"envoy.offline", "envoy.stale"} {
		if k := incidentKey(Event{Name: trig, EnvoyID: "e1"}); k != online {
			t.Errorf("incidentKey(%s) = %q, envoy.online = %q; resolve cannot match its trigger", trig, k, online)
		}
	}
	det := map[string]any{"configcrate": "c", "resource": "r"}
	if a, b := incidentKey(Event{Name: "resource.blocked", EnvoyID: "e1", Details: det}),
		incidentKey(Event{Name: "resource.unblocked", EnvoyID: "e2", Details: det}); a != b {
		t.Errorf("resource.blocked key %q != resource.unblocked key %q", a, b)
	}
	if a, b := incidentKey(Event{Name: "run.failure", EnvoyID: "e1"}), incidentKey(Event{Name: "run.failure", EnvoyID: "e2"}); a == b {
		t.Errorf("distinct envoys share incident key %q", a)
	}
}

func TestTicketDedup_OneTicketPerIncident(t *testing.T) {
	rec := &recordingTicketTarget{recordingTarget{name: "jira"}}
	d := newTestDispatcher(rec)
	ev := Event{Name: "run.failure", EnvoyID: "e1", Hostname: "h1"}

	dispatchWait(t, d, &rec.recordingTarget, 1, ev)
	d.Dispatch(ev) // same incident, inside the window: no second ticket
	time.Sleep(50 * time.Millisecond)
	if n := len(rec.events()); n != 1 {
		t.Fatalf("tickets opened for one incident = %d, want 1", n)
	}

	dispatchWait(t, d, &rec.recordingTarget, 1, Event{Name: "run.failure", EnvoyID: "e2", Hostname: "h2"})
	if n := len(rec.events()); n != 2 {
		t.Fatalf("a different envoy's incident was suppressed: %d tickets, want 2", n)
	}
}

func TestTicketDedup_ResolveReopens(t *testing.T) {
	rec := &recordingTicketTarget{recordingTarget{name: "jira"}}
	d := newTestDispatcher(rec)
	off := Event{Name: "envoy.offline", EnvoyID: "e1", Hostname: "h1"}
	dispatchWait(t, d, &rec.recordingTarget, 1, off)
	dispatchWait(t, d, &rec.recordingTarget, 1, Event{Name: "envoy.online", EnvoyID: "e1", Hostname: "h1"})
	dispatchWait(t, d, &rec.recordingTarget, 1, off) // recurrence after resolve opens a new ticket
	if n := len(rec.events()); n != 3 {
		t.Fatalf("events dispatched = %d, want 3 (open, resolve, reopen)", n)
	}
}

func TestTicketDedup_FailedOpenDoesNotSuppressRetry(t *testing.T) {
	rec := &recordingTicketTarget{recordingTarget{name: "jira", fail: true}}
	d := newTestDispatcher(rec)
	ev := Event{Name: "run.failure", EnvoyID: "e1"}
	dispatchWait(t, d, &rec.recordingTarget, 1, ev)
	// The release happens after Dispatch returns; let it land.
	time.Sleep(20 * time.Millisecond)
	dispatchWait(t, d, &rec.recordingTarget, 1, ev)
	if n := len(rec.events()); n != 2 {
		t.Fatalf("attempts = %d, want 2 (a failed open must be retried)", n)
	}
}

func TestTicketDedup_Bounded(t *testing.T) {
	td := newTicketDedup()
	for i := 0; i < ticketDedupMax+50; i++ {
		td.admit("jira", Event{Name: "run.failure", DedupKey: fmt.Sprintf("k%d", i)})
	}
	if n := len(td.opened); n > ticketDedupMax {
		t.Fatalf("ticket window holds %d keys, cap %d", n, ticketDedupMax)
	}
}

func TestTicketDedup_WindowExpires(t *testing.T) {
	td := newTicketDedup()
	now := time.Now()
	td.now = func() time.Time { return now }
	ev := Event{Name: "run.failure", DedupKey: "k"}
	if _, ok := td.admit("jira", ev); !ok {
		t.Fatal("first admit refused")
	}
	if _, ok := td.admit("jira", ev); ok {
		t.Fatal("second admit inside the window was allowed")
	}
	now = now.Add(ticketDedupWindow + time.Second)
	if _, ok := td.admit("jira", ev); !ok {
		t.Fatal("admit after the window expired was refused")
	}
}

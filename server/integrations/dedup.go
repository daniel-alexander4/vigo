// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// incidentClass names the incident an event belongs to. Events that
// describe the same underlying condition share a class, so they dedup
// into one incident or ticket and their resolve event matches:
//
//   - envoy.stale and envoy.offline fire together on the same edge and
//     are both resolved by envoy.online, so all three are "envoy.reachability".
//   - resource.unblocked resolves resource.blocked.
//
// Every other event is its own class.
func incidentClass(name string) string {
	switch name {
	case "envoy.stale", "envoy.offline", "envoy.online":
		return "envoy.reachability"
	case "resource.unblocked":
		return "resource.blocked"
	}
	return name
}

// incidentKey is the stable dedup key for an event: the PagerDuty
// dedup_key, the Opsgenie alias, the ServiceNow correlation_id, the
// Zendesk external_id, and the ticket-window key. Dispatch computes it
// once, before fleet-wide suppression rewrites the event, so a grouped
// summary keeps the key of the event it replaced.
func incidentKey(e Event) string {
	class := incidentClass(e.Name)
	// resource.blocked groups by error class across envoys, not per envoy.
	if class == "resource.blocked" {
		configcrate, _ := e.Details["configcrate"].(string)
		resource, _ := e.Details["resource"].(string)
		return fmt.Sprintf("vigo:%s:%s:%s", class, configcrate, resource)
	}
	// Security events: one incident per threat type.
	if strings.HasPrefix(e.Name, "security.") {
		return "vigo:" + class
	}
	// Fleet-wide events carry no envoy.
	if e.EnvoyID == "" {
		return "vigo:" + class
	}
	return fmt.Sprintf("vigo:%s:%s", class, e.EnvoyID)
}

// ticketTarget marks a target whose Dispatch opens a ticket or incident
// record in a system with no native dedup on create. The Dispatcher
// admits at most one open per incident key per ticketDedupWindow for
// these targets.
type ticketTarget interface {
	target
	opensTicket()
}

// ticketDedupWindow is how long an incident key, once a ticket was opened
// for it, suppresses further tickets for the same key on the same
// target. A resolve event clears the key early so a recurrence opens a
// fresh ticket.
const ticketDedupWindow = 24 * time.Hour

// ticketDedupMax bounds the window's memory. When full, expired keys are
// swept; if it is still full the oldest key is evicted.
const ticketDedupMax = 10000

// ticketDedup is a bounded in-memory recent-key window keyed by
// (target, incident key). In-memory only: a vigosrv restart forgets it,
// which costs at most one duplicate ticket per open condition.
type ticketDedup struct {
	mu     sync.Mutex
	opened map[string]time.Time
	now    func() time.Time
}

func newTicketDedup() *ticketDedup {
	return &ticketDedup{opened: make(map[string]time.Time), now: time.Now}
}

func ticketDedupKey(targetName string, e Event) string {
	if isResolveEvent(e.Name) {
		return targetName + "|resolve|" + e.DedupKey
	}
	return targetName + "|" + e.DedupKey
}

// admit reports whether the target may open a ticket for e, reserving
// the key if so. A resolve event clears the trigger key for the target.
// Call release if the dispatch then fails, so a failed open does not
// suppress the retry.
func (d *ticketDedup) admit(targetName string, e Event) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if isResolveEvent(e.Name) {
		delete(d.opened, targetName+"|"+e.DedupKey)
	}
	key := ticketDedupKey(targetName, e)
	if t, ok := d.opened[key]; ok && now.Sub(t) < ticketDedupWindow {
		return key, false
	}
	if len(d.opened) >= ticketDedupMax {
		d.evictLocked(now)
	}
	d.opened[key] = now
	return key, true
}

// release forgets a reservation made by admit.
func (d *ticketDedup) release(key string) {
	d.mu.Lock()
	delete(d.opened, key)
	d.mu.Unlock()
}

func (d *ticketDedup) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, t := range d.opened {
		if now.Sub(t) >= ticketDedupWindow {
			delete(d.opened, k)
			continue
		}
		if oldestKey == "" || t.Before(oldest) {
			oldestKey, oldest = k, t
		}
	}
	if len(d.opened) >= ticketDedupMax && oldestKey != "" {
		delete(d.opened, oldestKey)
	}
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package publish

import (
	"sync"
	"time"
)

// State holds operator-controlled mutable publish state. The zero
// value is fine (no freeze). Server-side callers construct one and
// pass it to Publish; CLI-side callers (separate process, no
// persistent freeze semantics) pass nil.
type State struct {
	mu          sync.Mutex
	freezeUntil *time.Time
}

// Frozen reports whether publish is currently blocked by a freeze.
// Nil-safe — a nil receiver always returns false (CLI bypass).
func (s *State) Frozen() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freezeUntil != nil && time.Now().Before(*s.freezeUntil)
}

// FreezeUntilTime returns the active freeze deadline, or nil if no
// freeze is in effect. Nil-safe.
func (s *State) FreezeUntilTime() *time.Time {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freezeUntil == nil {
		return nil
	}
	t := *s.freezeUntil
	return &t
}

// SetFreezeUntil installs a freeze deadline. Panics on nil receiver
// (the server holds a real *State; callers that don't want freeze
// semantics use a nil *State and never call this).
func (s *State) SetFreezeUntil(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freezeUntil = &t
}

// ClearFreeze removes any active freeze. Panics on nil receiver.
func (s *State) ClearFreeze() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freezeUntil = nil
}

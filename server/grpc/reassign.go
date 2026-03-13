// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import "sync"

// ReassignStore holds pending reassignment info for thralls that are being
// moved to a different spoke. The AdminService writes entries via the
// SetReassignment RPC, and the checkin handler pops them when the thrall
// next checks in.
type ReassignStore struct {
	mu sync.RWMutex
	m  map[string]ReassignInfo
}

// ReassignInfo describes where a thrall should reconnect after reassignment.
type ReassignInfo struct {
	TargetAddr  string
	NewThrallID string
}

// NewReassignStore creates an empty ReassignStore.
func NewReassignStore() *ReassignStore {
	return &ReassignStore{m: make(map[string]ReassignInfo)}
}

// Set records a pending reassignment for the given thrall.
func (s *ReassignStore) Set(thrallID string, info ReassignInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[thrallID] = info
}

// Pop retrieves and removes the reassignment info for a thrall.
// Returns the info and true if found, or zero value and false if not.
func (s *ReassignStore) Pop(thrallID string) (ReassignInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.m[thrallID]
	if ok {
		delete(s.m, thrallID)
	}
	return info, ok
}

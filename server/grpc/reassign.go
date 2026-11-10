// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import "sync"

// ReassignStore holds pending reassignment info for envoys that are being
// moved to a different bolt. The AdminService writes entries via the
// SetReassignment RPC, and the checkin handler pops them when the envoy
// next checks in.
type ReassignStore struct {
	mu sync.RWMutex
	m  map[string]ReassignInfo
}

// ReassignInfo describes where a envoy should reconnect after reassignment.
type ReassignInfo struct {
	TargetAddr string
	NewEnvoyID string
}

// NewReassignStore creates an empty ReassignStore.
func NewReassignStore() *ReassignStore {
	return &ReassignStore{m: make(map[string]ReassignInfo)}
}

// Set records a pending reassignment for the given envoy.
func (s *ReassignStore) Set(envoyID string, info ReassignInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[envoyID] = info
}

// Pop retrieves and removes the reassignment info for a envoy.
// Returns the info and true if found, or zero value and false if not.
//
// Reassignments are rare events but every CheckIn calls Pop, so the
// fast path runs under RLock: an empty store or a missing key returns
// without touching the write lock. Only an actual hit upgrades to Lock
// + delete, with a re-check inside the critical section in case a
// concurrent Pop/Set raced between RUnlock and Lock.
func (s *ReassignStore) Pop(envoyID string) (ReassignInfo, bool) {
	s.mu.RLock()
	if len(s.m) == 0 {
		s.mu.RUnlock()
		return ReassignInfo{}, false
	}
	_, present := s.m[envoyID]
	s.mu.RUnlock()
	if !present {
		return ReassignInfo{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.m[envoyID]
	if !ok {
		return ReassignInfo{}, false
	}
	delete(s.m, envoyID)
	return info, true
}

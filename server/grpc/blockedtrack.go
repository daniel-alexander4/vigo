// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"sync"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/convergence"
)

// blockedKey names a blocked resource the way the resource.blocked incident
// key does (integrations.incidentKey): configcrate + resource, fleet-wide.
type blockedKey struct {
	configcrate string
	resource    string
}

// blockedTracker remembers which resources each envoy last reported blocked,
// so the server can emit resource.unblocked — the resolve event for the
// fleet-wide resource.blocked incident — once a resource is blocked on no
// envoy any more. The zero value is ready to use.
//
// Cost: an envoy with no blocked resources, reporting a run with none,
// costs one map lookup. Work beyond that is proportional to the blocked
// resources involved, never to the run's size.
//
// In-memory only: a vigosrv restart forgets it, so an incident open across a
// restart is not auto-resolved and is closed by hand.
type blockedTracker struct {
	mu       sync.Mutex
	byEnvoy  map[string]map[blockedKey]struct{}
	envoyCnt map[blockedKey]int // envoys currently blocked on the key
}

// observe records one ingested run's blocked set for envoyID and returns the
// keys that are now blocked on no envoy.
func (t *blockedTracker) observe(envoyID string, results []*pb.ResourceRunResult, blockedCount int64) []blockedKey {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.byEnvoy[envoyID]
	if blockedCount == 0 && len(prev) == 0 {
		return nil
	}

	var cur map[blockedKey]struct{}
	if blockedCount > 0 {
		cur = make(map[blockedKey]struct{}, blockedCount)
		for _, r := range results {
			if r.GetActionTaken() == convergence.ActionTakenBlocked {
				cur[blockedKey{r.GetConfigcrateName(), r.GetResourceName()}] = struct{}{}
			}
		}
	}

	if t.envoyCnt == nil {
		t.envoyCnt = make(map[blockedKey]int)
		t.byEnvoy = make(map[string]map[blockedKey]struct{})
	}
	for k := range cur {
		if _, had := prev[k]; !had {
			t.envoyCnt[k]++
		}
	}
	var cleared []blockedKey
	for k := range prev {
		if _, still := cur[k]; still {
			continue
		}
		if t.release(k) {
			cleared = append(cleared, k)
		}
	}
	if len(cur) == 0 {
		delete(t.byEnvoy, envoyID)
	} else {
		t.byEnvoy[envoyID] = cur
	}
	return cleared
}

// forget drops a departed envoy (deleted, revoked, purged) and returns the
// keys that are now blocked on no envoy.
func (t *blockedTracker) forget(envoyID string) []blockedKey {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.byEnvoy[envoyID]
	delete(t.byEnvoy, envoyID)
	var cleared []blockedKey
	for k := range prev {
		if t.release(k) {
			cleared = append(cleared, k)
		}
	}
	return cleared
}

// release drops one envoy's hold on k; true when no envoy holds it any more.
// Caller holds t.mu.
func (t *blockedTracker) release(k blockedKey) bool {
	t.envoyCnt[k]--
	if t.envoyCnt[k] > 0 {
		return false
	}
	delete(t.envoyCnt, k)
	return true
}

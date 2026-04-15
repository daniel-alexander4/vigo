// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigogit

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ClaimTable is the in-memory store mapping a vigogit content-seed
// label to the deterministic set of envoys selected to mirror it.
// The table is rebuilt on server restart — it holds no state that
// cannot be reconstructed from the swarm manifest plus per-envoy
// `users` and `network` traits.
//
// The table is updated by the manifest watcher (landing in a follow-
// up commit) whenever a `vigogit-meta:*` seed is added or revoked,
// and by the trait ingester whenever an envoy's `users` or
// `network.hostname` traits change (a change may add or remove
// eligibility for a label).
//
// On each CheckIn, the gRPC handler calls [ClaimTable.Claims] for
// the requesting envoy and ships the returned list via
// `CheckInResponse.vigogit_claims`.
type ClaimTable struct {
	mu      sync.RWMutex
	sources map[string]*SourceState // key = full content label
}

// SourceState is the cached per-source selection.
type SourceState struct {
	// Label is the full content-seed label
	// `vigogit:<seeder>:<user>:<project>`.
	Label string

	// Sidecar is the parsed, validated sidecar for this source.
	// Never nil once Upsert has stored the source.
	Sidecar *Sidecar

	// SeederHost is the `<seeder>` segment of Label. Stored
	// separately for cheap access during selection and claim
	// lookups.
	SeederHost string

	// TargetUser is the `<user>` segment of Label.
	TargetUser string

	// Project is the `<project>` segment of Label.
	Project string

	// Mirrors is the deterministic ordered list of envoy IDs
	// selected as mirrors for this source. Its length is at most
	// Sidecar.MaxMirrors. The order is hash-ascending so ties are
	// broken deterministically by hostname.
	Mirrors []string

	// CandidateCount is the size of the candidate pool *before*
	// the cap was applied. UnderReplicated == (CandidateCount <
	// Sidecar.MaxMirrors).
	CandidateCount int

	// UnderReplicated is true when the candidate pool was smaller
	// than the sidecar's requested MaxMirrors. The server surfaces
	// this via the `vigogit.under_replicated` trait (landing in a
	// later commit) so operators can see and fix it.
	UnderReplicated bool
}

// Candidate is an envoy eligible to mirror a vigogit source. The
// caller is responsible for pre-filtering the candidate pool: an
// envoy should appear here only if its `users` trait includes the
// source's TargetUser and its hostname is NOT the SeederHost.
type Candidate struct {
	EnvoyID  string
	Hostname string
}

// NewClaimTable constructs an empty claim table.
func NewClaimTable() *ClaimTable {
	return &ClaimTable{sources: make(map[string]*SourceState)}
}

// Len returns the number of vigogit sources currently tracked.
func (ct *ClaimTable) Len() int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return len(ct.sources)
}

// Upsert records or updates a source. The caller supplies the
// parsed sidecar and the current candidate pool. Selection runs
// deterministically and returns the updated [SourceState].
//
// The candidate pool must already be filtered: each candidate is an
// envoy whose `users` trait includes the label's TargetUser and
// whose hostname is NOT the SeederHost. (The seeder itself is never
// eligible to mirror its own source — vigogit is about redundant
// copies, not self-seeding.)
//
// If the sidecar has `paused: true`, the source is still recorded
// (so revocations and state transitions are observable) but the
// Mirrors list is empty — no new claims are issued. Existing
// receiver materializations tied to a previous non-paused selection
// retention-expire through the normal swarm path.
func (ct *ClaimTable) Upsert(label string, sidecar *Sidecar, candidates []Candidate) (*SourceState, error) {
	seeder, user, project, err := parseContentLabel(label)
	if err != nil {
		return nil, err
	}
	if sidecar == nil {
		return nil, fmt.Errorf("vigogit: Upsert(%s): sidecar is nil", label)
	}

	state := &SourceState{
		Label:          label,
		Sidecar:        sidecar,
		SeederHost:     seeder,
		TargetUser:     user,
		Project:        project,
		CandidateCount: len(candidates),
	}

	if sidecar.Paused {
		// Paused sources keep a presence in the table so the
		// server can see that the source exists (and surface it in
		// the UI) but issue no claims. Receivers that already
		// materialized a mirror pre-pause will hold onto it until
		// the content seed retention-expires.
		state.Mirrors = nil
		state.UnderReplicated = false
	} else {
		state.Mirrors = selectMirrors(label, candidates, sidecar.MaxMirrors)
		state.UnderReplicated = len(candidates) < sidecar.MaxMirrors
	}

	ct.mu.Lock()
	ct.sources[label] = state
	ct.mu.Unlock()
	return state, nil
}

// Remove drops a source from the table. Calls for unknown labels are
// a no-op.
func (ct *ClaimTable) Remove(label string) {
	ct.mu.Lock()
	delete(ct.sources, label)
	ct.mu.Unlock()
}

// Claims returns the list of vigogit content labels this envoy has
// been selected to mirror. The list is stable between calls as long
// as the table isn't mutated, and the returned slice is a copy so
// the caller can mutate or retain it freely.
func (ct *ClaimTable) Claims(envoyID string) []string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	var out []string
	for label, s := range ct.sources {
		for _, id := range s.Mirrors {
			if id == envoyID {
				out = append(out, label)
				break
			}
		}
	}
	// Sort for stable delivery order — consumers on the agent
	// side diff successive lists and a nondeterministic order
	// would cause spurious "changed" events.
	sort.Strings(out)
	return out
}

// Get returns a defensive copy of the state for a single label, or
// nil if the label is not in the table.
func (ct *ClaimTable) Get(label string) *SourceState {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	s, ok := ct.sources[label]
	if !ok {
		return nil
	}
	return cloneState(s)
}

// Snapshot returns a defensive copy of every source state in the
// table. Intended for the `/vigogit` UI / debug endpoints.
func (ct *ClaimTable) Snapshot() map[string]*SourceState {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	out := make(map[string]*SourceState, len(ct.sources))
	for label, s := range ct.sources {
		out[label] = cloneState(s)
	}
	return out
}

// cloneState returns a deep copy of s so callers can hold onto it
// without racing the writer.
func cloneState(s *SourceState) *SourceState {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Sidecar != nil {
		sc := *s.Sidecar
		if len(s.Sidecar.Tags) > 0 {
			sc.Tags = append([]string(nil), s.Sidecar.Tags...)
		}
		if len(s.Sidecar.IncludeEnvoys) > 0 {
			sc.IncludeEnvoys = append([]string(nil), s.Sidecar.IncludeEnvoys...)
		}
		if len(s.Sidecar.ExcludeEnvoys) > 0 {
			sc.ExcludeEnvoys = append([]string(nil), s.Sidecar.ExcludeEnvoys...)
		}
		cp.Sidecar = &sc
	}
	if len(s.Mirrors) > 0 {
		cp.Mirrors = append([]string(nil), s.Mirrors...)
	}
	return &cp
}

// --- Deterministic selection --------------------------------------

// selectMirrors runs the deterministic hash-based selection for a
// single source. Each candidate is scored by
// sha256(label || "\x00" || hostname); candidates are sorted by
// ascending score with hostname as a stable tie-breaker, and the
// first `min(maxMirrors, len(candidates))` are returned as envoy
// IDs.
//
// Plan note: the original design doc specified BLAKE3. We use
// sha256 here to avoid taking on a new Go dependency (blake3 is not
// in the stdlib). The hash is purely for deterministic ordering, so
// any well-distributed hash satisfies the requirement. If the
// codebase ever grows a blake3 dep for another reason, swapping
// this is a one-line change but also a fleet-visible selection
// reshuffle.
func selectMirrors(label string, candidates []Candidate, maxMirrors int) []string {
	if len(candidates) == 0 || maxMirrors <= 0 {
		return nil
	}

	type scored struct {
		id    string
		host  string
		score [32]byte
	}
	out := make([]scored, len(candidates))
	for i, c := range candidates {
		out[i] = scored{
			id:    c.EnvoyID,
			host:  c.Hostname,
			score: scoreLabelHost(label, c.Hostname),
		}
	}

	sort.Slice(out, func(i, j int) bool {
		// Primary: ascending score.
		for k := 0; k < 32; k++ {
			if out[i].score[k] != out[j].score[k] {
				return out[i].score[k] < out[j].score[k]
			}
		}
		// Tie-breaker: hostname string order (stable,
		// deterministic, no reliance on envoy ID).
		if out[i].host != out[j].host {
			return out[i].host < out[j].host
		}
		return out[i].id < out[j].id
	})

	n := maxMirrors
	if n > len(out) {
		n = len(out)
	}
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = out[i].id
	}
	return ids
}

// scoreLabelHost computes the selection score for a (label, hostname)
// pair. Label and hostname are joined with a NUL byte so that a
// hostname starting with a prefix of another hostname cannot
// collide by string concatenation.
func scoreLabelHost(label, hostname string) [32]byte {
	h := sha256.New()
	// Feed the label length + label + separator + hostname so
	// field boundaries are unambiguous. (Attack vector: a label
	// and hostname that differ in where the split is but produce
	// the same concatenated input.) sha256.Write never errors.
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(label)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(label))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(hostname))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// --- Label parsing -------------------------------------------------

// parseContentLabel decomposes a full content-seed label of the form
// `vigogit:<seeder>:<user>:<project>` into its three segments. The
// `vigogit:` prefix must match exactly — meta labels
// (`vigogit-meta:...`) are rejected so a caller can't accidentally
// upsert a meta label into the content-keyed table.
func parseContentLabel(label string) (seeder, user, project string, err error) {
	const prefix = "vigogit:"
	if !strings.HasPrefix(label, prefix) {
		return "", "", "", fmt.Errorf(
			"vigogit: label %q is missing %q prefix", label, prefix)
	}
	if strings.HasPrefix(label, "vigogit-meta:") {
		return "", "", "", fmt.Errorf(
			"vigogit: label %q is a meta label, not a content label", label)
	}
	rest := strings.TrimPrefix(label, prefix)
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf(
			"vigogit: label %q must have three colon-separated segments after prefix",
			label)
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf(
			"vigogit: label %q has an empty segment", label)
	}
	return parts[0], parts[1], parts[2], nil
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package spanner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
)

// Spanner peer-equal enrollment routing + action propagation
// (ADR-026 + the spanner-redesign plan). This file holds the
// peer-equal routing + force-push + reassign + drain behaviours.
//
// The design decision (Dan-approved, ADR-026 simplification): actions
// are point-to-point commands routed by DIRECT peer gRPC over the
// already-gossiped admissions roster (spanner_bolts, incl. each bolt's
// pattern_claims). There is NO gossiped intent/ack channel, NO new
// CRDT table, NO proto change — every action reuses the surviving
// per-bolt RPCs (EnrollEnvoy / GetEnvoy / SetReassignment / ForcePush)
// and the Slice 2-ii-a PeerDialer.
//
// OwningBolt is the shared routing primitive: given a hostname it
// resolves which roster bolt owns it by evaluating each bolt's
// pattern_claims with the canonical ADR-002 hostcrate matcher
// (config.HostMatchesPatterns — first-match-wins, "-" deny-prefix).
// On overlap the oldest-admitted bolt wins, so every bolt computes the
// same deterministic answer without coordination.

// OwningBolt resolves which roster bolt owns a hostname. It loads the
// live admissions roster (revoked bolts excluded) and evaluates each
// bolt's pattern_claims against the hostname using the canonical
// ADR-002 hostcrate matcher (config.HostMatchesPatterns). A bolt whose
// claims positively match (an allow rule, not a "-" deny) owns the
// hostname.
//
// On overlap — two bolts both positively match — the bolt with the
// OLDEST admitted_at wins. The roster is identical on every bolt
// (CRDT-replicated), so this tiebreak is deterministic: every bolt in
// the spanner computes the same owner for a given hostname without
// coordination.
//
// Returns the owning bolt's pubkey-hex + hostname and true, or
// ("","",false) when no roster bolt claims the hostname (the caller
// keeps the envoy local). A nil SpannerAdmin returns no-match.
func (a *SpannerAdmin) OwningBolt(ctx context.Context, hostname string) (boltPubkeyHex, boltHostname string, found bool) {
	return a.owningBolt(ctx, hostname, "")
}

// OwningBoltExcluding is OwningBolt with one roster bolt — identified by
// its hostname — held out of the candidate set. The drain path uses it
// so an envoy on the bolt being drained resolves to a DIFFERENT bolt,
// never "reassigned" straight back to its own source.
func (a *SpannerAdmin) OwningBoltExcluding(ctx context.Context, hostname, excludeBoltHostname string) (boltPubkeyHex, boltHostname string, found bool) {
	return a.owningBolt(ctx, hostname, excludeBoltHostname)
}

// owningBolt is the shared resolver behind OwningBolt and
// OwningBoltExcluding. excludeBoltHostname, when non-empty, holds the
// named bolt out of the match.
func (a *SpannerAdmin) owningBolt(ctx context.Context, hostname, excludeBoltHostname string) (boltPubkeyHex, boltHostname string, found bool) {
	if a == nil {
		return "", "", false
	}
	roster, err := a.queries.ListLiveBolts(ctx)
	if err != nil {
		slog.Warn("spanner routing: listing live bolts failed", "error", err)
		return "", "", false
	}

	var (
		bestPubkey   string
		bestHostname string
		bestAdmitted int64
		matched      bool
	)
	for _, b := range roster {
		if excludeBoltHostname != "" && b.Hostname == excludeBoltHostname {
			continue
		}
		var claims []string
		if b.PatternClaims != "" {
			if uErr := json.Unmarshal([]byte(b.PatternClaims), &claims); uErr != nil {
				slog.Warn("spanner routing: bolt pattern_claims is not a JSON array; skipping",
					"bolt", b.Hostname, "error", uErr)
				continue
			}
		}
		if !config.HostMatchesPatterns(hostname, claims) {
			continue
		}
		// Overlap tiebreak — the oldest-admitted bolt wins. Deterministic
		// because every bolt holds the same CRDT-replicated roster.
		if !matched || b.AdmittedAt < bestAdmitted {
			bestPubkey = b.Pubkey
			bestHostname = b.Hostname
			bestAdmitted = b.AdmittedAt
			matched = true
		}
	}
	return bestPubkey, bestHostname, matched
}

// BoltHostname resolves a roster bolt's hostname from its pubkey-hex
// via the local spanner_bolts roster. Returns "" when no roster row
// matches. The peer-equal REST handlers use it to translate the
// operator-facing bolt id (pubkey-hex) into the hostname the
// action-propagation RPCs dial.
func (a *SpannerAdmin) BoltHostname(ctx context.Context, boltPubkeyHex string) string {
	if a == nil {
		return ""
	}
	return hostnameOfPeer(ctx, a, boltPubkeyHex)
}

// GRPCPort returns the gRPC port this SpannerAdmin dials peer bolts
// on. The enrollment-redirect path uses it to build the bolt_addr
// (host:port) handed to the agent.
func (a *SpannerAdmin) GRPCPort() string {
	if a == nil {
		return defaultBoltGRPCPort
	}
	return a.grpcPort
}

// OwnsLocally reports whether the given owning-bolt pubkey-hex is this
// bolt's own identity — i.e. OwningBolt resolved to the local bolt and
// no redirect is needed. A nil SpannerAdmin (standalone) owns nothing.
func (a *SpannerAdmin) OwnsLocally(boltPubkeyHex string) bool {
	return a != nil && boltPubkeyHex == a.ownPubkey
}

// --- peer-equal action propagation ---

// dialBolt opens a single admin connection to one bolt by hostname.
// Defaults to a real PeerDialer.Dial against host:grpcPort; a test
// injects a stub so the action-propagation paths can be exercised
// without an mTLS harness (mirrors the dialPeers injection point the
// fanout path uses).
func (a *SpannerAdmin) dialBolt(boltHostname string) (*PeerConn, error) {
	if a.dialBoltFn != nil {
		return a.dialBoltFn(boltHostname)
	}
	return a.dialer.Dial(boltHostname, boltHostname+":"+a.grpcPort, nil)
}

// EnrollOnPeerBolt creates an envoy record on a target bolt via a
// direct EnrollEnvoy admin gRPC call over the PeerDialer. boltHostname is the target
// bolt's hostname (from the roster); the gRPC port is the
// SpannerAdmin's configured peer port. certSPKI is the envoy's mTLS
// client-cert pin (hex SHA-256 of its SPKI) captured by this bolt; the
// target stores it so the routed envoy is pinned. Returns the envoy UUID the
// target bolt assigned.
func (a *SpannerAdmin) EnrollOnPeerBolt(ctx context.Context, boltHostname, hostname, ipAddress, pubkeyPEM, certSPKI string, force bool) (string, error) {
	pc, err := a.dialBolt(boltHostname)
	if err != nil {
		return "", fmt.Errorf("dialing target bolt %s: %w", boltHostname, err)
	}
	defer pc.Close()

	callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	resp, err := pc.Admin.EnrollEnvoy(callCtx, &pb.EnrollEnvoyRequest{
		Hostname:       hostname,
		IpAddress:      ipAddress,
		PubkeyPem:      pubkeyPEM,
		CertSpkiSha256: certSPKI,
		Force:          force,
	})
	if err != nil {
		return "", fmt.Errorf("EnrollEnvoy on bolt %s: %w", boltHostname, err)
	}
	return resp.EnvoyId, nil
}

// ForcePushToBolt sends a ForcePush to one bolt via a direct admin
// gRPC call over the PeerDialer — the owning-bolt routing path for a
// force-push targeted at an envoy a remote peer serves. Mirrors
// RunsFromBolt's single-peer-RPC shape. Returns the bolt's
// envoys-notified count.
func (a *SpannerAdmin) ForcePushToBolt(ctx context.Context, boltHostname, envoyID string) (int32, error) {
	pc, err := a.dialBolt(boltHostname)
	if err != nil {
		return 0, fmt.Errorf("dialing owning bolt %s: %w", boltHostname, err)
	}
	defer pc.Close()

	callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	resp, err := pc.Admin.ForcePush(callCtx, &pb.ForcePushRequest{EnvoyId: envoyID})
	if err != nil {
		return 0, fmt.Errorf("ForcePush on owning bolt %s: %w", boltHostname, err)
	}
	return resp.EnvoysNotified, nil
}

// DeleteEnvoyToBolt deletes an envoy on its owning bolt via a direct
// admin gRPC call over the PeerDialer — the owning-bolt routing path for
// a delete targeted at an envoy a remote peer serves. Mirrors
// ForcePushToBolt. Without this, a delete on the wrong bolt is a silent
// 0-row no-op (the envoys list is fleet-wide but the row lives on one bolt).
func (a *SpannerAdmin) DeleteEnvoyToBolt(ctx context.Context, boltHostname, envoyID string, skipSwarmCleanup bool) error {
	pc, err := a.dialBolt(boltHostname)
	if err != nil {
		return fmt.Errorf("dialing owning bolt %s: %w", boltHostname, err)
	}
	defer pc.Close()

	callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	if _, err := pc.Admin.DeleteEnvoy(callCtx, &pb.DeleteEnvoyRequest{Id: envoyID, SkipSwarmCleanup: skipSwarmCleanup}); err != nil {
		return fmt.Errorf("DeleteEnvoy on owning bolt %s: %w", boltHostname, err)
	}
	return nil
}

// RevokeEnvoyToBolt revokes an envoy on its owning bolt via a direct
// admin gRPC call over the PeerDialer. Mirrors DeleteEnvoyToBolt.
func (a *SpannerAdmin) RevokeEnvoyToBolt(ctx context.Context, boltHostname, envoyID string, skipSwarmCleanup bool) error {
	pc, err := a.dialBolt(boltHostname)
	if err != nil {
		return fmt.Errorf("dialing owning bolt %s: %w", boltHostname, err)
	}
	defer pc.Close()

	callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	if _, err := pc.Admin.RevokeEnvoy(callCtx, &pb.RevokeEnvoyRequest{Id: envoyID, SkipSwarmCleanup: skipSwarmCleanup}); err != nil {
		return fmt.Errorf("RevokeEnvoy on owning bolt %s: %w", boltHostname, err)
	}
	return nil
}

// PeerReassignResult is the outcome of a peer-equal envoy reassignment.
type PeerReassignResult struct {
	NewEnvoyID string `json:"new_envoy_id"`
	TargetAddr string `json:"target_addr"`
}

// ReassignEnvoy moves an envoy from one bolt to another, hub-free.
// Any bolt can orchestrate it — the three steps are direct peer admin
// RPCs over the PeerDialer:
//
//  1. source bolt GetEnvoy   — fetch the envoy's hostname, IP, pubkey
//     and cert pin.
//  2. target bolt EnrollEnvoy — create the envoy record on the target.
//  3. source bolt SetReassignment — mark the envoy so it reconnects to
//     the target on its next check-in.
//
// fromBoltHostname / toBoltHostname are roster hostnames. When
// toBoltHostname is "" the target is auto-resolved from
// OwningBolt(envoy.Hostname) (step 1). The returned TargetAddr is the
// target bolt's gRPC address (host:port) — the same value handed to
// the agent via SetReassignment.
func (a *SpannerAdmin) ReassignEnvoy(ctx context.Context, envoyID, fromBoltHostname, toBoltHostname string) (*PeerReassignResult, error) {
	if a == nil {
		return nil, fmt.Errorf("spanner reassign: no spanner admin")
	}

	// Step 1 — fetch the envoy from the source bolt.
	srcConn, err := a.dialBolt(fromBoltHostname)
	if err != nil {
		return nil, fmt.Errorf("dialing source bolt %s: %w", fromBoltHostname, err)
	}
	defer srcConn.Close()

	getCtx, getCancel := context.WithTimeout(ctx, fanOutTimeout)
	defer getCancel()
	getResp, err := srcConn.Admin.GetEnvoy(getCtx, &pb.GetEnvoyRequest{Id: envoyID})
	if err != nil {
		return nil, fmt.Errorf("getting envoy from source bolt %s: %w", fromBoltHostname, err)
	}
	envoy := getResp.Envoy
	if envoy == nil {
		return nil, fmt.Errorf("source bolt %s returned no envoy for id %s", fromBoltHostname, envoyID)
	}

	// Resolve the target bolt when not explicitly given — auto-route by
	// the envoy's hostname through OwningBolt (step 1's shared primitive).
	if toBoltHostname == "" {
		_, owner, ok := a.OwningBolt(ctx, envoy.Hostname)
		if !ok {
			return nil, fmt.Errorf("no target bolt claims hostname %s", envoy.Hostname)
		}
		toBoltHostname = owner
	}
	targetAddr := toBoltHostname + ":" + a.grpcPort

	// Step 2 — create the envoy record on the target bolt.
	// EnrollEnvoy requires the pubkey; carry the cert pin too so the
	// target pins the same mTLS identity the source did.
	if getResp.GetPubkeyPem() == "" {
		return nil, fmt.Errorf("source bolt %s returned no pubkey for envoy %s", fromBoltHostname, envoyID)
	}
	newID, err := a.EnrollOnPeerBolt(ctx, toBoltHostname, envoy.Hostname, envoy.IpAddress, getResp.GetPubkeyPem(), getResp.GetCertSpkiSha256(), true)
	if err != nil {
		return nil, fmt.Errorf("enrolling on target bolt %s: %w", toBoltHostname, err)
	}

	// Step 3 — mark the source bolt's envoy for reassignment so the
	// agent reconnects to the target on its next check-in.
	setCtx, setCancel := context.WithTimeout(ctx, fanOutTimeout)
	defer setCancel()
	if _, err := srcConn.Admin.SetReassignment(setCtx, &pb.SetReassignmentRequest{
		EnvoyId:    envoyID,
		TargetAddr: targetAddr,
		NewEnvoyId: newID,
	}); err != nil {
		return nil, fmt.Errorf("setting reassignment on source bolt %s: %w", fromBoltHostname, err)
	}

	slog.Info("spanner envoy reassigned (peer-equal)",
		"envoy_id", envoyID,
		"from_bolt", fromBoltHostname,
		"to_bolt", toBoltHostname,
		"new_envoy_id", newID,
	)
	return &PeerReassignResult{NewEnvoyID: newID, TargetAddr: targetAddr}, nil
}

// PeerDrainResult is the outcome of a peer-equal bolt drain.
type PeerDrainResult struct {
	Moved  int32    `json:"moved"`
	Failed int32    `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// DrainBolt moves every envoy off a source bolt onto other bolts,
// hub-free. It lists the source bolt's envoys via a direct ListEnvoys
// admin RPC, then reassigns each (ReassignEnvoy). When toBoltHostname
// is "" each envoy is auto-routed by hostname through OwningBolt;
// otherwise every envoy goes to that one bolt. Revoked envoys are
// skipped.
func (a *SpannerAdmin) DrainBolt(ctx context.Context, fromBoltHostname, toBoltHostname string) (*PeerDrainResult, error) {
	if a == nil {
		return nil, fmt.Errorf("spanner drain: no spanner admin")
	}

	srcConn, err := a.dialBolt(fromBoltHostname)
	if err != nil {
		return nil, fmt.Errorf("dialing source bolt %s: %w", fromBoltHostname, err)
	}
	listCtx, listCancel := context.WithTimeout(ctx, fanOutTimeout)
	listResp, err := srcConn.Admin.ListEnvoys(listCtx, &pb.ListEnvoysRequest{})
	listCancel()
	srcConn.Close()
	if err != nil {
		return nil, fmt.Errorf("listing envoys on source bolt %s: %w", fromBoltHostname, err)
	}

	result := &PeerDrainResult{}
	for _, e := range listResp.Envoys {
		if e.Revoked {
			continue
		}
		// Resolve the target. An explicit toBoltHostname passes through;
		// auto-route resolves the owning bolt EXCLUDING the bolt being
		// drained — otherwise an envoy whose oldest-admitted claimant is
		// the source bolt would "reassign" straight back to it, a silent
		// no-op drain. An envoy no other bolt claims is a drain failure.
		target := toBoltHostname
		if target == "" {
			_, owner, ok := a.OwningBoltExcluding(ctx, e.Hostname, fromBoltHostname)
			if !ok {
				result.Failed++
				result.Errors = append(result.Errors,
					fmt.Sprintf("drain %s: no other bolt claims hostname %s", e.Id, e.Hostname))
				continue
			}
			target = owner
		}
		if _, rErr := a.ReassignEnvoy(ctx, e.Id, fromBoltHostname, target); rErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("reassign %s: %v", e.Hostname, rErr))
			continue
		}
		result.Moved++
	}

	slog.Info("spanner bolt drained (peer-equal)",
		"from_bolt", fromBoltHostname, "moved", result.Moved, "failed", result.Failed)
	return result, nil
}

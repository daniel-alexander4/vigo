// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/cve"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleetpressure"
	"git.alexander4.io/vigo/server/testutil"
	"google.golang.org/grpc"
)

// Spanner Slice 2-ii-a — SpannerAdmin local-path + owning-bolt-routing
// tests. The fanout path (gRPC fan-out) is covered by the multi-bolt
// integration test in server/grpc, which has the mTLS test harness.

// newAdminFixture builds a SpannerAdmin over a freshly migrated DB,
// with a populated observability cache and local-bolt sources.
func newAdminFixture(t *testing.T) *SpannerAdmin {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "ownbolt", FallbackAdminLocal, "1530")
	return a
}

// TestAggregateEnvoysLocal folds the local bolt's roster together with
// every cached peer's roster.
func TestAggregateEnvoysLocal(t *testing.T) {
	a := newAdminFixture(t)
	a.SetLocalSources(
		func() ConvergenceCounters { return ConvergenceCounters{Total: 1, Converged: 1} },
		func() []EnvoyLite { return []EnvoyLite{{ID: "local-1", Hostname: "lh1"}} },
	)
	now := time.Now()
	a.observ.UpdateFull("peerA", []EnvoyLite{{ID: "a-1"}, {ID: "a-2"}}, ConvergenceCounters{Total: 2}, now)
	a.observ.UpdateFull("peerB", []EnvoyLite{{ID: "b-1"}}, ConvergenceCounters{Total: 1}, now.Add(-time.Minute))

	envoys, oldest := a.AggregateEnvoysLocal()
	if len(envoys) != 4 {
		t.Fatalf("aggregated envoy count = %d, want 4 (1 local + 3 peer)", len(envoys))
	}
	// The oldest contributing peer snapshot is peerB's.
	if !oldest.Equal(now.Add(-time.Minute)) {
		t.Errorf("oldest = %v, want %v", oldest, now.Add(-time.Minute))
	}
}

// TestAggregateConvergenceLocal sums the local bolt's counters with
// every cached peer's counters.
func TestAggregateConvergenceLocal(t *testing.T) {
	a := newAdminFixture(t)
	a.SetLocalSources(
		func() ConvergenceCounters {
			return ConvergenceCounters{Total: 10, Converged: 8, Failed: 2}
		},
		func() []EnvoyLite { return nil },
	)
	now := time.Now()
	a.observ.UpdateFull("peerA", nil, ConvergenceCounters{Total: 5, Converged: 5}, now)
	a.observ.UpdateFull("peerB", nil, ConvergenceCounters{Total: 3, Converged: 1, Degraded: 2}, now)

	counters, _ := a.AggregateConvergenceLocal()
	want := ConvergenceCounters{Total: 18, Converged: 14, Degraded: 2, Failed: 2}
	if counters != want {
		t.Errorf("aggregated counters = %+v, want %+v", counters, want)
	}
}

// TestAggregateConvergenceLocalNoPeers — a bolt with no cached peers
// reports only its own counters and a zero oldest-update.
func TestAggregateConvergenceLocalNoPeers(t *testing.T) {
	a := newAdminFixture(t)
	a.SetLocalSources(
		func() ConvergenceCounters { return ConvergenceCounters{Total: 4, Converged: 4} },
		func() []EnvoyLite { return nil },
	)
	counters, oldest := a.AggregateConvergenceLocal()
	if counters.Total != 4 || counters.Converged != 4 {
		t.Errorf("counters = %+v, want only the local bolt's", counters)
	}
	if !oldest.IsZero() {
		t.Error("oldest should be the zero time when no peers contribute")
	}
}

// TestOwningBoltHostname resolves an envoy to its serving peer bolt
// from the cached rosters, with the hostname read from the
// authenticated spanner_bolts roster (not the gossiped data).
func TestOwningBoltHostname(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "ownbolt", FallbackAdminLocal, "1530")

	// Seed a roster row for the peer so its hostname can be resolved.
	peerHex := hexKey(0x11)
	if err := q.InsertBolt(t.Context(), db.InsertBoltParams{
		Pubkey:        peerHex,
		Hostname:      "peer-one.example.com",
		AdmittedAt:    1,
		AdmittedBy:    peerHex,
		PatternClaims: "[]",
		Signature:     []byte("sig"),
	}); err != nil {
		t.Fatalf("InsertBolt: %v", err)
	}
	// The cache says peerHex serves envoy "e-remote".
	a.observ.UpdateFull(peerHex, []EnvoyLite{{ID: "e-remote", Hostname: "rh"}}, ConvergenceCounters{}, time.Now())

	pubkey, hostname, remote := a.OwningBoltHostname(context.Background(), "e-remote")
	if !remote {
		t.Fatal("OwningBoltHostname did not mark a cached peer's envoy as remote")
	}
	if pubkey != peerHex {
		t.Errorf("owning pubkey = %q, want %q", pubkey, peerHex)
	}
	if hostname != "peer-one.example.com" {
		t.Errorf("owning hostname = %q, want the roster value", hostname)
	}

	// An envoy not in any cached peer roster is not remote.
	if _, _, r := a.OwningBoltHostname(context.Background(), "e-local"); r {
		t.Error("a non-peer envoy was reported as remote")
	}
}

// --- fanout-path + multi-bolt integration ---

// stubAdminClient is a pb.VigoAdminClient that answers the three
// fleet-wide reads from canned per-bolt data. Every other RPC panics
// — the fanout path only calls these three.
type stubAdminClient struct {
	pb.VigoAdminClient
	envoys []*pb.Envoy
	runs   []*pb.Run
	conv   *pb.GetConvergenceSummaryResponse
}

func (s *stubAdminClient) ListEnvoys(_ context.Context, _ *pb.ListEnvoysRequest, _ ...grpc.CallOption) (*pb.ListEnvoysResponse, error) {
	return &pb.ListEnvoysResponse{Envoys: s.envoys}, nil
}

func (s *stubAdminClient) ListRuns(_ context.Context, _ *pb.ListRunsRequest, _ ...grpc.CallOption) (*pb.ListRunsResponse, error) {
	return &pb.ListRunsResponse{Runs: s.runs}, nil
}

func (s *stubAdminClient) GetConvergenceSummary(_ context.Context, _ *pb.GetConvergenceSummaryRequest, _ ...grpc.CallOption) (*pb.GetConvergenceSummaryResponse, error) {
	return s.conv, nil
}

// TestFanoutMultiBoltAggregation stands up a SpannerAdmin in fanout
// mode over two simulated peer bolts (no hub) and asserts the
// fleet-wide reads return the union of both bolts' data.
func TestFanoutMultiBoltAggregation(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "ownbolt", FallbackAdminFanout, "1530")

	boltA := &stubAdminClient{
		envoys: []*pb.Envoy{{Id: "a1", Hostname: "a1.host"}, {Id: "a2", Hostname: "a2.host"}},
		runs:   []*pb.Run{{Id: "ra1"}},
		conv:   &pb.GetConvergenceSummaryResponse{Total: 2, Converged: 2},
	}
	boltB := &stubAdminClient{
		envoys: []*pb.Envoy{{Id: "b1", Hostname: "b1.host"}},
		runs:   []*pb.Run{{Id: "rb1"}, {Id: "rb2"}},
		conv:   &pb.GetConvergenceSummaryResponse{Total: 3, Converged: 1, Failed: 2},
	}
	// Inject the two peer connections — bypasses the real dialer's
	// same-port assumption (two in-process servers can't share a port).
	a.dialPeers = func(_ context.Context) ([]*PeerConn, error) {
		return []*PeerConn{
			{ID: "boltA", Admin: boltA},
			{ID: "boltB", Admin: boltB},
		}, nil
	}

	envoys, err := a.AggregateEnvoysFanout(t.Context())
	if err != nil {
		t.Fatalf("AggregateEnvoysFanout: %v", err)
	}
	if len(envoys) != 3 {
		t.Errorf("fanout envoy count = %d, want 3 (2 from A + 1 from B)", len(envoys))
	}

	conv, err := a.AggregateConvergenceFanout(t.Context())
	if err != nil {
		t.Fatalf("AggregateConvergenceFanout: %v", err)
	}
	if conv.Total != 5 || conv.Converged != 3 || conv.Failed != 2 {
		t.Errorf("fanout convergence = %+v, want Total 5 / Converged 3 / Failed 2", conv)
	}

	runs, err := a.AggregateRunsFanout(t.Context(), "", 20)
	if err != nil {
		t.Fatalf("AggregateRunsFanout: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("fanout run count = %d, want 3", len(runs))
	}
}

// TestFanoutAndLocalShapeParity asserts the fanout and local paths
// produce the same fleet-wide answer when fed equivalent data — the
// operator may flip fallback_admin without the response changing
// shape. The local path reads the observability cache; the fanout
// path queries peers live. Here both see the same two-bolt fleet.
func TestFanoutAndLocalShapeParity(t *testing.T) {
	// Two peer bolts, plus the local bolt's own slice.
	localEnvoys := []EnvoyLite{{ID: "L1", Hostname: "l1"}}
	localConv := ConvergenceCounters{Total: 1, Converged: 1}
	peerAEnvoys := []EnvoyLite{{ID: "a1"}, {ID: "a2"}}
	peerAConv := ConvergenceCounters{Total: 2, Converged: 2}
	peerBEnvoys := []EnvoyLite{{ID: "b1"}}
	peerBConv := ConvergenceCounters{Total: 3, Converged: 1, Failed: 2}

	// --- local path ---
	local := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "ownbolt", FallbackAdminLocal, "1530")
	local.SetLocalSources(
		func() ConvergenceCounters { return localConv },
		func() []EnvoyLite { return localEnvoys },
	)
	now := time.Now()
	local.observ.UpdateFull("peerA", peerAEnvoys, peerAConv, now)
	local.observ.UpdateFull("peerB", peerBEnvoys, peerBConv, now)

	localE, _ := local.AggregateEnvoysLocal()
	localC, _ := local.AggregateConvergenceLocal()

	// --- fanout path ---
	fanout := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "ownbolt", FallbackAdminFanout, "1530")
	fanout.dialPeers = func(_ context.Context) ([]*PeerConn, error) {
		return []*PeerConn{
			{ID: "peerA", Admin: &stubAdminClient{
				conv: &pb.GetConvergenceSummaryResponse{Total: int32(peerAConv.Total), Converged: int32(peerAConv.Converged)},
			}},
			{ID: "peerB", Admin: &stubAdminClient{
				conv: &pb.GetConvergenceSummaryResponse{Total: int32(peerBConv.Total), Converged: int32(peerBConv.Converged), Failed: int32(peerBConv.Failed)},
			}},
		}, nil
	}
	fanoutC, err := fanout.AggregateConvergenceFanout(t.Context())
	if err != nil {
		t.Fatalf("AggregateConvergenceFanout: %v", err)
	}
	// fanout returns only the peer sums — the gRPC layer folds in the
	// local bolt's own counters. Do the same fold here.
	fanoutTotal := int(fanoutC.Total) + localConv.Total
	fanoutConverged := int(fanoutC.Converged) + localConv.Converged
	fanoutFailed := int(fanoutC.Failed) + localConv.Failed

	// Both paths must see the same fleet-wide totals.
	if localC.Total != fanoutTotal || localC.Converged != fanoutConverged || localC.Failed != fanoutFailed {
		t.Errorf("local vs fanout convergence parity: local=%+v fanout=(Total %d, Converged %d, Failed %d)",
			localC, fanoutTotal, fanoutConverged, fanoutFailed)
	}
	if localC.Total != 6 {
		t.Errorf("aggregate total = %d, want 6 (1 local + 2 + 3 peers)", localC.Total)
	}
	// Local path also sees every envoy (cache + local source).
	if len(localE) != 4 {
		t.Errorf("local aggregate envoy count = %d, want 4", len(localE))
	}
}

// TestFanoutFlag reflects the configured fallback_admin.
func TestFanoutFlag(t *testing.T) {
	local := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "x", FallbackAdminLocal, "")
	if local.Fanout() {
		t.Error("local mode reported Fanout() true")
	}
	fanout := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "x", FallbackAdminFanout, "")
	if !fanout.Fanout() {
		t.Error("fanout mode reported Fanout() false")
	}
	// Empty fallback_admin defaults to local.
	def := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "x", "", "")
	if def.Fanout() {
		t.Error("empty fallback_admin should default to local")
	}
}

func TestMergeSecuritySummaries(t *testing.T) {
	// Two bolts share CVE-2024-1 (low on bolt A, critical on bolt B → the
	// union must keep critical); CVE-2024-2 is unique to A, CVE-2024-3 to B.
	// Summing per-bolt unique counts (2+2=4) would over-count; the union is 3.
	// Lynis is an average: bolt A scored 1000 hosts summing 70000 (avg 70),
	// bolt B scored 10 hosts summing 900 (avg 90). The fleet avg must be
	// weighted by scored-host count — 70900/1010 ≈ 70 — not the 80 you'd get
	// averaging the two averages.
	boltA := SecuritySummary{
		UniqueCVEs:        []CVEIdent{{ID: "CVE-2024-1", Severity: "low"}, {ID: "CVE-2024-2", Severity: "high"}},
		CertsExpiringSoon: 3, CertsExpired: 1, RootkitWarnings: 2, RunningCVEsTotal: 10, RunningCVEsCritical: 1,
		LynisScoreSum: 70000, LynisScoredCount: 1000, ScannedHosts: 1000, TotalHosts: 1000, ClamavInfected: 4,
	}
	boltB := SecuritySummary{
		UniqueCVEs:        []CVEIdent{{ID: "CVE-2024-1", Severity: "critical"}, {ID: "CVE-2024-3", Severity: "medium"}},
		CertsExpiringSoon: 4, CertsExpired: 0, RootkitWarnings: 1, RunningCVEsTotal: 5, RunningCVEsCritical: 2,
		LynisScoreSum: 900, LynisScoredCount: 10, ScannedHosts: 10, TotalHosts: 12, ClamavInfected: 1,
	}

	got := MergeSecuritySummaries([]SecuritySummary{boltA, boltB})

	if got.LynisScoredCount != 1010 || got.LynisScoreSum != 70900 {
		t.Fatalf("lynis sum/count: got %d/%d, want 70900/1010", got.LynisScoreSum, got.LynisScoredCount)
	}
	if avg := got.LynisAvg(); avg != 70 {
		t.Errorf("fleet-weighted Lynis avg: got %d, want 70 (not the 80 of avg-of-avgs)", avg)
	}
	if got.ScannedHosts != 1010 || got.TotalHosts != 1012 || got.ClamavInfected != 5 {
		t.Errorf("scanner additive sums wrong: %+v", got)
	}

	if got.UniqueCVECount() != 3 {
		t.Errorf("unique CVE union: got %d, want 3 (CVE-2024-1 shared, must dedup)", got.UniqueCVECount())
	}
	// CVE-2024-1 must carry the max severity (critical), not low.
	var sev string
	for _, c := range got.UniqueCVEs {
		if c.ID == "CVE-2024-1" {
			sev = c.Severity
		}
	}
	if cve.Classify(sev) != cve.SevCritical {
		t.Errorf("shared CVE severity: got %q, want critical (max across bolts)", sev)
	}
	// Additive counts sum.
	if got.CertsExpiringSoon != 7 || got.CertsExpired != 1 || got.RootkitWarnings != 3 ||
		got.RunningCVEsTotal != 15 || got.RunningCVEsCritical != 3 {
		t.Errorf("additive sums wrong: %+v", got)
	}
}

func TestSecuritySummaryProtoRoundTrip(t *testing.T) {
	in := SecuritySummary{
		UniqueCVEs:        []CVEIdent{{ID: "CVE-2024-9", Severity: "high"}},
		CertsExpiringSoon: 2, CertsExpired: 1, CertsExpiringIn7: 1, CertsExpiringIn30: 2, CertHostsAffected: 3,
		RootkitWarnings: 4, RunningCVEsTotal: 6, RunningCVEsCritical: 1, RunningCVEsHigh: 2, RunningCVEsMedium: 2, RunningCVEsLow: 1, RunningCVEHostsAffected: 5,
		LynisScoreSum: 1234, LynisScoredCount: 17, LynisWarnings: 8, RootkitClean: 90, ClamavClean: 88, ClamavInfected: 2,
		AideClean: 80, AideChanged: 3, ScapScanned: 50, ScapClean: 47, ScapHighFail: 3, ScannedHosts: 95, TotalHosts: 100,
	}
	out := SecuritySummaryFromProto(SecuritySummaryToProto(in))
	if out.UniqueCVECount() != 1 || out.UniqueCVEs[0].ID != "CVE-2024-9" || out.UniqueCVEs[0].Severity != "high" {
		t.Errorf("CVE round-trip lost data: %+v", out.UniqueCVEs)
	}
	if out.CertsExpiringSoon != 2 || out.CertHostsAffected != 3 || out.RootkitWarnings != 4 ||
		out.RunningCVEsTotal != 6 || out.RunningCVEHostsAffected != 5 {
		t.Errorf("scalar round-trip mismatch: %+v", out)
	}
	if out.LynisScoreSum != 1234 || out.LynisScoredCount != 17 || out.LynisWarnings != 8 ||
		out.RootkitClean != 90 || out.ClamavClean != 88 || out.ClamavInfected != 2 ||
		out.AideClean != 80 || out.AideChanged != 3 || out.ScapScanned != 50 ||
		out.ScapClean != 47 || out.ScapHighFail != 3 || out.ScannedHosts != 95 || out.TotalHosts != 100 {
		t.Errorf("scanner-rollup round-trip mismatch: %+v", out)
	}
}

func TestMergeHealthSummaries(t *testing.T) {
	a := fleetpressure.FleetSummary{
		WorstSeverity: fleetpressure.Warn, DegradedCount: 3, MaxOpenFDs: 500, MaxOpenFDsHost: "a-host",
		HighFDPctCount: 1, HighFDPctExample: "a-fd", RecentOomCount: 0, TotalReporting: 100,
	}
	b := fleetpressure.FleetSummary{
		WorstSeverity: fleetpressure.Fail, DegradedCount: 2, MaxOpenFDs: 900, MaxOpenFDsHost: "b-host",
		HighFDPctCount: 2, RecentOomCount: 1, RecentOomExample: "b-oom", TotalReporting: 10,
	}
	got := MergeHealthSummaries([]fleetpressure.FleetSummary{a, b})
	if got.WorstSeverity != fleetpressure.Fail {
		t.Errorf("worst severity should win: got %v, want Fail", got.WorstSeverity)
	}
	if got.MaxOpenFDs != 900 || got.MaxOpenFDsHost != "b-host" {
		t.Errorf("max open-FDs host should win: got %d/%q, want 900/b-host", got.MaxOpenFDs, got.MaxOpenFDsHost)
	}
	if got.DegradedCount != 5 || got.HighFDPctCount != 3 || got.RecentOomCount != 1 || got.TotalReporting != 110 {
		t.Errorf("additive counts wrong: %+v", got)
	}
	if got.HighFDPctExample != "a-fd" || got.RecentOomExample != "b-oom" {
		t.Errorf("examples should take first non-empty: %+v", got)
	}
}

func TestHealthSummaryProtoRoundTrip(t *testing.T) {
	in := fleetpressure.FleetSummary{
		WorstSeverity: fleetpressure.Fail, DegradedCount: 7, MaxOpenFDs: 1234, MaxOpenFDsHost: "h", MaxOpenFDsID: "id",
		MaxOpenFDsSeverity: fleetpressure.Warn, HighFDPctCount: 4, HighFDPctExample: "x", RecentOomCount: 2,
		RecentOomExample: "o", TotalReporting: 50,
	}
	out := HealthSummaryFromProto(HealthSummaryToProto(in))
	if out != in {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestMergeRiskSummaries(t *testing.T) {
	// Bolt A: 1000 envoys, score sum 30000 (avg 30). Bolt B: 10 envoys,
	// score sum 900 (avg 90). The fleet mean must be weighted by envoy
	// count — 30900/1010 ≈ 30 — not the average of averages (60).
	a := RiskSummary{
		ScoreSum: 30000, ScoredCount: 1000, Critical: 1, High: 5, Medium: 10, Low: 984, MaxScore: 88,
		TopEnvoys:  []RiskTopEnvoy{{ID: "a1", Score: 88}, {ID: "a2", Score: 40}},
		TopFactors: []RiskFactorRollup{{Name: "cve", TotalPoints: 50, EnvoyCount: 5}, {Name: "drift", TotalPoints: 20, EnvoyCount: 2}},
	}
	b := RiskSummary{
		ScoreSum: 900, ScoredCount: 10, Critical: 4, High: 3, Medium: 2, Low: 1, MaxScore: 95,
		TopEnvoys:  []RiskTopEnvoy{{ID: "b1", Score: 95}, {ID: "b2", Score: 60}},
		TopFactors: []RiskFactorRollup{{Name: "cve", TotalPoints: 30, EnvoyCount: 3}, {Name: "certs", TotalPoints: 15, EnvoyCount: 1}},
	}

	got := MergeRiskSummaries([]RiskSummary{a, b})

	if got.ScoredCount != 1010 || got.ScoreSum != 30900 {
		t.Fatalf("merge sums: count=%d sum=%d, want 1010/30900", got.ScoredCount, got.ScoreSum)
	}
	if avg := got.AvgScore(); avg != 30 {
		t.Errorf("fleet-weighted avg: got %d, want 30 (not the 60 you'd get averaging averages)", avg)
	}
	if got.Critical != 5 || got.High != 8 || got.Medium != 12 || got.Low != 985 {
		t.Errorf("severity buckets not summed: %+v", got)
	}
	if got.MaxScore != 95 {
		t.Errorf("MaxScore should be the cross-bolt max: got %d, want 95", got.MaxScore)
	}
	// Top-N re-ranked globally by score desc: b1(95) > a1(88) > b2(60) > a2(40).
	if len(got.TopEnvoys) != 4 || got.TopEnvoys[0].ID != "b1" || got.TopEnvoys[1].ID != "a1" ||
		got.TopEnvoys[2].ID != "b2" || got.TopEnvoys[3].ID != "a2" {
		t.Errorf("top-N not re-ranked across bolts: %+v", got.TopEnvoys)
	}
	// Factors summed by name (cve appears on both bolts → 80 points / 8 envoys).
	var cvePts, cveCount int
	for _, f := range got.TopFactors {
		if f.Name == "cve" {
			cvePts, cveCount = f.TotalPoints, f.EnvoyCount
		}
	}
	if cvePts != 80 || cveCount != 8 {
		t.Errorf("shared factor not summed: cve got %d pts / %d envoys, want 80/8", cvePts, cveCount)
	}
	if got.TopFactors[0].Name != "cve" {
		t.Errorf("factors should sort by points desc, top is %q want cve", got.TopFactors[0].Name)
	}
}

func TestRiskSummaryProtoRoundTrip(t *testing.T) {
	in := RiskSummary{
		ScoreSum: 1234, ScoredCount: 56, Critical: 1, High: 2, Medium: 3, Low: 4, MaxScore: 91,
		TopEnvoys:  []RiskTopEnvoy{{ID: "e1", Hostname: "h1", Score: 91, Level: "critical", TopFactor: "cve"}},
		TopFactors: []RiskFactorRollup{{Name: "cve", TotalPoints: 50, EnvoyCount: 5}},
	}
	out := RiskSummaryFromProto(RiskSummaryToProto(in))
	if out.MaxScore != 91 || len(out.TopEnvoys) != 1 || out.TopEnvoys[0] != in.TopEnvoys[0] ||
		len(out.TopFactors) != 1 || out.TopFactors[0] != in.TopFactors[0] {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
	if out.ScoreSum != in.ScoreSum || out.ScoredCount != in.ScoredCount || out.Critical != in.Critical {
		t.Errorf("scalar round-trip mismatch: got %+v", out)
	}
}

func TestMergeComplianceSummaries(t *testing.T) {
	// Bolt A: many frameworks averaging 30% (PctSum 90 over 3). Bolt B: one
	// framework at 90% (PctSum 90 over 1). The fleet percent must re-mean
	// over the union of per-framework rows — (90+90)/(3+1) = 45 — NOT the
	// average of per-bolt averages ((30+90)/2 = 60). Buckets sum additively.
	a := ComplianceSummary{Satisfied: 30, Waived: 3, Partial: 4, NotSatisfied: 63, Total: 100, PctSum: 90, PctCount: 3}
	b := ComplianceSummary{Satisfied: 9, Waived: 0, Partial: 0, NotSatisfied: 1, Total: 10, PctSum: 90, PctCount: 1}

	got := MergeComplianceSummaries([]ComplianceSummary{a, b})

	if got.PctCount != 4 || got.PctSum != 180 {
		t.Fatalf("merge sums: pctCount=%d pctSum=%v, want 4/180", got.PctCount, got.PctSum)
	}
	if pct := got.Pct(); pct != 45 {
		t.Errorf("fleet re-meaned pct: got %v, want 45 (not the 60 you'd get averaging per-bolt averages)", pct)
	}
	if got.Satisfied != 39 || got.Waived != 3 || got.Partial != 4 || got.NotSatisfied != 64 || got.Total != 110 {
		t.Errorf("control buckets not summed: %+v", got)
	}
}

func TestComplianceSummaryProtoRoundTrip(t *testing.T) {
	in := ComplianceSummary{Satisfied: 12, Waived: 3, Partial: 4, NotSatisfied: 5, Total: 24, PctSum: 67.5, PctCount: 2}
	out := ComplianceSummaryFromProto(ComplianceSummaryToProto(in))
	if out != in {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

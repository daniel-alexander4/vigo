// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
	"github.com/google/uuid"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// hotpathSPKI / hotpathPin: the synthetic client cert every fixture presents
// and the pin its envoy enrolled.
var (
	hotpathSPKI = []byte("hotpath-test-client-spki")
	hotpathPin  = func() string { s := sha256.Sum256(hotpathSPKI); return hex.EncodeToString(s[:]) }()
)

// fromIP is a pinned mTLS peer context from source address ip.
func fromIP(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000},
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: hotpathSPKI}},
		}},
	})
}

type hotpathEnvoy struct {
	id   string
	priv ed25519.PrivateKey
}

// addPinnedEnvoy enrolls a fresh, pinned envoy in fi.
func addPinnedEnvoy(t *testing.T, fi *fleet.FleetIndex) hotpathEnvoy {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        id,
		Hostname:  "hp-" + id[:8],
		PubkeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
		CertSPKI:  hotpathPin,
	}, pub)
	return hotpathEnvoy{id: id, priv: priv}
}

// checkIn builds a CheckInRequest for id, signed by priv (nil priv → a
// garbage signature, the unsigned-flood shape).
func checkIn(id string, priv ed25519.PrivateKey) *pb.CheckInRequest {
	now := time.Now()
	fp := "fp"
	sig := []byte("not-a-signature-not-a-signature-not-a-signature-not-a-signature")
	if priv != nil {
		sig = ed25519.Sign(priv, append([]byte(fp), fmt.Sprintf("%d", now.Unix())...))
	}
	return &pb.CheckInRequest{EnvoyId: id, StateFingerprint: fp, Signature: sig, Timestamp: timestamppb.New(now)}
}

var checkInInfo = &grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_CheckIn_FullMethodName}

func passHandler(ctx context.Context, req any) (any, error) { return "ok", nil }

// TestCheckInUnsignedFloodLeavesVictimBucket: a flood of check-ins carrying a
// victim's envoy_id but no valid signature must not spend the victim's
// per-envoy bucket — the bucket is charged only after verification — so the
// victim's own signed check-in still succeeds afterwards.
func TestCheckInUnsignedFloodLeavesVictimBucket(t *testing.T) {
	fi := fleet.NewFleetIndex()
	victim := addPinnedEnvoy(t, fi)
	m := metrics.NewTestMetrics()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	// Per-envoy burst 2; IP failure budget high so the flood reaches verify.
	lim := NewCheckInLimiter(config.RateLimitSection{Enabled: true, CheckinPerEnvoy: 2, CheckinGlobal: 100000, CheckinFailuresPerIP: 100000}, m, stop)
	ic := SignatureVerifyInterceptor(nil, 5*time.Minute, fi, m, lim)

	for i := 0; i < 50; i++ {
		_, err := ic(fromIP("198.51.100.66"), checkIn(victim.id, nil), checkInInfo, passHandler)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("flood request %d: got %v, want Unauthenticated (bad signature)", i, err)
		}
	}
	if _, err := ic(fromIP("10.0.0.5"), checkIn(victim.id, victim.priv), checkInInfo, passHandler); err != nil {
		t.Fatalf("victim's signed check-in after an unsigned flood: %v (per-envoy bucket was drained by unverified requests)", err)
	}

	// Control: the per-envoy limit still bites on verified traffic.
	_, _ = ic(fromIP("10.0.0.5"), checkIn(victim.id, victim.priv), checkInInfo, passHandler)
	if _, err := ic(fromIP("10.0.0.5"), checkIn(victim.id, victim.priv), checkInInfo, passHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("third verified check-in with burst 2: got %v, want ResourceExhausted", err)
	}
}

// TestCheckInIPFailureBudget: an address that exhausts its failed-verification
// budget has further check-ins refused before verify (even validly signed
// ones), and successful check-ins from an address never spend it.
func TestCheckInIPFailureBudget(t *testing.T) {
	fi := fleet.NewFleetIndex()
	good := addPinnedEnvoy(t, fi)
	m := metrics.NewTestMetrics()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	lim := NewCheckInLimiter(config.RateLimitSection{Enabled: true, CheckinPerEnvoy: 100000, CheckinGlobal: 100000, CheckinFailuresPerIP: 3}, m, stop)
	ic := SignatureVerifyInterceptor(nil, 5*time.Minute, fi, m, lim)

	// Successes from the NAT address, well past the failure budget: never throttled.
	nat := fromIP("203.0.113.7")
	for i := 0; i < 20; i++ {
		if _, err := ic(nat, checkIn(good.id, good.priv), checkInInfo, passHandler); err != nil {
			t.Fatalf("success %d from shared address throttled: %v", i, err)
		}
	}

	// Three failures (bad signature, unknown id, bad signature) are answered
	// by verify; the budget is then spent.
	for i, req := range []*pb.CheckInRequest{checkIn(good.id, nil), checkIn(uuid.NewString(), nil), checkIn(good.id, nil)} {
		if _, err := ic(nat, req, checkInInfo, passHandler); status.Code(err) == codes.ResourceExhausted || err == nil {
			t.Fatalf("failure %d: got %v, want a verify refusal", i, err)
		}
	}
	if got := promtest.ToFloat64(m.CheckinIPThrottled); got != 0 {
		t.Fatalf("throttle counter = %v before the budget is exceeded", got)
	}
	_, err := ic(nat, checkIn(good.id, good.priv), checkInInfo, passHandler)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("check-in from an address over its failure budget: got %v, want ResourceExhausted", err)
	}
	if got := promtest.ToFloat64(m.CheckinIPThrottled); got != 1 {
		t.Fatalf("vigo_checkin_ip_throttled_total = %v, want 1", got)
	}
	// Another address is unaffected.
	if _, err := ic(fromIP("203.0.113.8"), checkIn(good.id, good.priv), checkInInfo, passHandler); err != nil {
		t.Fatalf("a different address was throttled: %v", err)
	}
}

// TestCheckInIPFailureMapBounded: failures from more distinct addresses than
// the cap never grow the per-IP map past it.
func TestCheckInIPFailureMapBounded(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	lim := NewCheckInLimiter(config.RateLimitSection{Enabled: true}, nil, stop)
	lim.ipMax = 16
	for i := 0; i < 500; i++ {
		lim.recordFailure(fmt.Sprintf("10.%d.%d.1", i/256, i%256))
	}
	if n := lim.ipEntries(); n > 16 {
		t.Fatalf("per-IP failure map holds %d entries, cap 16", n)
	}
	if lim.ipEntries() == 0 {
		t.Fatal("per-IP failure map is empty — failures are not being recorded")
	}
}

// TestCheckInLimiterEvictionStopsOnStop: the eviction goroutine exits when its
// stop channel closes, so tests and reloads don't leak one per limiter.
func TestCheckInLimiterEvictionStopsOnStop(t *testing.T) {
	// Earlier tests' limiters stop on t.Cleanup and their loops exit
	// asynchronously; let the count settle before taking the baseline.
	base := evictionLoops.Load()
	for stable, deadline := 0, time.Now().Add(time.Second); stable < 3 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		if n := evictionLoops.Load(); n == base {
			stable++
		} else {
			base, stable = n, 0
		}
	}
	stop := make(chan struct{})
	for i := 0; i < 20; i++ {
		_ = NewCheckInLimiter(config.RateLimitSection{Enabled: true}, nil, stop)
	}
	deadline := time.Now().Add(2 * time.Second)
	for evictionLoops.Load() < base+20 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := evictionLoops.Load(); n < base+20 {
		t.Fatalf("eviction loops = %d, want %d running", n, base+20)
	}
	close(stop)
	deadline = time.Now().Add(2 * time.Second)
	for evictionLoops.Load() > base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := evictionLoops.Load(); n != base {
		t.Fatalf("eviction loops = %d after stop, want %d", n, base)
	}
}

// TestCheckInGlobalLimitPreVerify: the global cap is charged before verify and
// passes non-check-in RPCs through.
func TestCheckInGlobalLimitPreVerify(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	ic := NewCheckInLimiter(config.RateLimitSection{Enabled: true, CheckinGlobal: 1}, nil, stop).UnaryInterceptor()
	if _, err := ic(context.Background(), &pb.CheckInRequest{}, checkInInfo, passHandler); err != nil {
		t.Fatalf("first check-in: %v", err)
	}
	if _, err := ic(context.Background(), &pb.CheckInRequest{}, checkInInfo, passHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second check-in over global cap 1/s: got %v, want ResourceExhausted", err)
	}
	other := &grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_Register_FullMethodName}
	for i := 0; i < 5; i++ {
		if _, err := ic(context.Background(), nil, other, passHandler); err != nil {
			t.Fatalf("non-check-in RPC rate limited: %v", err)
		}
	}
}

// countingDBTX wraps a real DB and counts every query issued through it.
type countingDBTX struct {
	inner *sql.DB
	n     atomic.Int64
}

func (c *countingDBTX) ExecContext(ctx context.Context, q string, a ...interface{}) (sql.Result, error) {
	c.n.Add(1)
	return c.inner.ExecContext(ctx, q, a...)
}
func (c *countingDBTX) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	c.n.Add(1)
	return c.inner.PrepareContext(ctx, q)
}
func (c *countingDBTX) QueryContext(ctx context.Context, q string, a ...interface{}) (*sql.Rows, error) {
	c.n.Add(1)
	return c.inner.QueryContext(ctx, q, a...)
}
func (c *countingDBTX) QueryRowContext(ctx context.Context, q string, a ...interface{}) *sql.Row {
	c.n.Add(1)
	return c.inner.QueryRowContext(ctx, q, a...)
}

// TestCheckInUnknownAndRevokedWithoutDBRead: with a fleet index, an unknown id
// is NotFound and a revoked id (held, or known only to the boot-time revoked
// set) is PermissionDenied with the existing texts — and none of it issues a
// single DB query. Every RPC that verifies through lookupEnvoyAndPubKey is
// covered.
func TestCheckInUnknownAndRevokedWithoutDBRead(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	counter := &countingDBTX{inner: sqlDB}
	queries := db.New(counter)
	m := metrics.NewTestMetrics()

	fi := fleet.NewFleetIndex()
	held := addPinnedEnvoy(t, fi)
	fi.SetRevoked(held.id, true)
	bootRevoked := uuid.NewString()
	fi.AddRevokedIDs([]string{bootRevoked})

	ic := SignatureVerifyInterceptor(queries, 5*time.Minute, fi, m, nil)
	ctx := fromIP("192.0.2.1")
	ts := fmt.Sprintf("%d", time.Now().Unix())
	reqs := func(id string) map[string]struct {
		info *grpc.UnaryServerInfo
		req  any
	} {
		return map[string]struct {
			info *grpc.UnaryServerInfo
			req  any
		}{
			"CheckIn":        {checkInInfo, checkIn(id, nil)},
			"ReportResult":   {&grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_ReportResult_FullMethodName}, &pb.ReportResultRequest{EnvoyId: id, RunId: "r", Timestamp: timestamppb.Now()}},
			"ReportTraits":   {&grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_ReportTraits_FullMethodName}, &pb.ReportTraitsRequest{EnvoyId: id, Timestamp: timestamppb.Now()}},
			"ResolveSecrets": {&grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_ResolveSecrets_FullMethodName}, &pb.ResolveSecretsRequest{EnvoyId: id, Timestamp: ts}},
			"Wake":           {&grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_Wake_FullMethodName}, &pb.WakeRequest{EnvoyId: id, Timestamp: ts}},
		}
	}
	cases := []struct {
		name, id string
		code     codes.Code
		msg      string
	}{
		{"unknown", uuid.NewString(), codes.NotFound, "unknown envoy: not enrolled — re-bootstrap this agent"},
		{"revoked held in index", held.id, codes.PermissionDenied, "envoy is revoked"},
		{"revoked known only to the revoked set", bootRevoked, codes.PermissionDenied, "envoy is revoked"},
	}
	for _, c := range cases {
		for rpc, r := range reqs(c.id) {
			t.Run(c.name+"/"+rpc, func(t *testing.T) {
				before := counter.n.Load()
				_, err := ic(ctx, r.req, r.info, passHandler)
				st := status.Convert(err)
				if st.Code() != c.code || st.Message() != c.msg {
					t.Fatalf("got %v, want %v %q", err, c.code, c.msg)
				}
				if n := counter.n.Load() - before; n != 0 {
					t.Fatalf("%d DB queries issued before verify, want 0", n)
				}
			})
		}
	}
}

// TestAdminRevokeAndDeleteUpdateRevokedSet: the admin revoke path marks the id
// in the fleet index's revoked set, and deleting the envoy clears it (a
// deleted envoy has no row, so it is unknown — NotFound — not revoked).
func TestAdminRevokeAndDeleteUpdateRevokedSet(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)
	ctx := context.Background()
	e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "adm-rs.example.com", IpAddress: "10.0.0.1", PubkeyPem: "pk"})
	if err != nil {
		t.Fatal(err)
	}
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: e.ID, Hostname: e.Hostname}, nil)
	svc := &AdminService{queries: queries, fleetIndex: fi}

	if _, err := svc.RevokeEnvoy(ctx, &pb.RevokeEnvoyRequest{Id: e.ID}); err != nil {
		t.Fatalf("RevokeEnvoy: %v", err)
	}
	if !fi.IsRevokedID(e.ID) {
		t.Fatal("admin RevokeEnvoy did not mark the id revoked in the fleet index")
	}
	if _, err := svc.DeleteEnvoy(ctx, &pb.DeleteEnvoyRequest{Id: e.ID}); err != nil {
		t.Fatalf("DeleteEnvoy: %v", err)
	}
	if fi.IsRevokedID(e.ID) {
		t.Fatal("admin DeleteEnvoy left the deleted id in the revoked set")
	}
}

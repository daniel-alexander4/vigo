// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"database/sql"

	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/api"
	"git.alexander4.io/vigo/server/audit"
	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/bolt"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/hardening"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
	"git.alexander4.io/vigo/server/swarm/gitbackmesh"
	"git.alexander4.io/vigo/server/swarm/lockboxmesh"
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
	"git.alexander4.io/vigo/server/swarmview"
	"git.alexander4.io/vigo/server/traits"
	"git.alexander4.io/vigo/ui"
)

// version is set at build time via -ldflags="-X main.version=$(cat VERSION)"
// (Makefile + Dockerfile.server). The "dev" default surfaces in `--version`
// output for un-ldflagged builds (`go run ./server`, ad-hoc `go build`) —
// honest and self-describing instead of a stale literal that drifts from
// the VERSION file. Matches the Makefile's `VERSION ?= $(shell cat VERSION
// 2>/dev/null || echo dev)` fallback exactly.
var version = "dev"

func main() {
	if err := run(); err != nil {
		var se *config.SecretErrors
		if errors.As(err, &se) {
			fmt.Fprint(os.Stderr, se.Format())
		} else {
			slog.Error("server failed", "error", err)
		}
		os.Exit(1)
	}
}

func run() error {
	if hasFlag("--version") || hasFlag("-v") {
		fmt.Println("vigosrv " + version)
		return nil
	}

	// --- Binary integrity check (gating) ---
	// The signature check is the only fail-closed startup gate. The
	// defense-in-depth detection checks (DetectDebugger, DetectInjection)
	// run inside NewServer after the audit writer is wired so their
	// findings reach Loki / Splunk / Sentinel via the integrations
	// dispatcher, not just stderr.
	if err := hardening.VerifyBinarySignature(); err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// pprof on loopback only — never exposed beyond the container, scraped via
	// `docker exec vigo curl 127.0.0.1:6060/debug/pprof/...`. vigosrv otherwise
	// has no DefaultServeMux users, so the pprof-package side-effect registrations
	// can't collide with application routes.
	//
	// Mutex + block profiling are off by default in Go; enable at conservative
	// sampling rates so contention shows up in profiles without measurable
	// steady-state overhead (1-in-10 contention events; blocks ≥10µs).
	runtime.SetMutexProfileFraction(10)
	runtime.SetBlockProfileRate(10000)
	go func() {
		srv := &http.Server{Addr: "127.0.0.1:6060", ReadHeaderTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("pprof listener exited", "error", err)
		}
	}()

	configFile := envOrDefault("VIGO_CONFIG_FILE", "server.yaml")

	s, err := NewServer(ctx, cancel, configFile)
	if err != nil {
		return err
	}
	defer s.Close()

	// SIGHUP handler for license reload. Lives in the binary wrapper —
	// signal handling is intentionally outside (*Server).Run so the
	// Phase C test harness can drive a *Server without inheriting the
	// process's signal disposition.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	go func() {
		for range sighupCh {
			slog.Info("received SIGHUP, reloading license...")
			if err := s.enforcer.Reload(); err != nil {
				slog.Error("license reload failed", "error", err)
			} else {
				st := s.enforcer.GetStatus()
				slog.Info("license reloaded", "stage", st.Stage.String())
			}
		}
	}()

	return s.Run(ctx)
}

// hasFlag checks if a flag is present in os.Args.
func hasFlag(flag string) bool {
	for _, arg := range os.Args[1:] {
		if arg == flag {
			return true
		}
	}
	return false
}

// envOrDefault returns the environment variable value or a default.
// initBundleSigner creates a BundleSigner by generating an ED25519 signing key.
// The key is ephemeral (generated per server start). Returns nil if key generation
// fails (signing is optional — bundles will be unsigned).
func initBundleSigner(cfg *config.ServerConfig) (*isgrpc.BundleSigner, uint32) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		slog.Error("failed to generate bundle signing key", "error", err)
		return nil, 0
	}

	signer := isgrpc.NewBundleSigner(priv)
	slog.Info("bundle signer initialized", "key_id", signer.KeyID())

	// Parse bundle_max_age from config (default 24h = 86400 seconds).
	maxAge := parseDuration(cfg.CheckIn.BundleMaxAge, 24*time.Hour)
	maxAgeSeconds := uint32(maxAge.Seconds())

	return signer, maxAgeSeconds
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stringOrDefault returns s if non-empty, otherwise def.
func stringOrDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// parseDuration parses a duration string, returning def if s is empty or invalid.
// parseRetention parses a retention string like "30d" or "720h" into a
// time.Duration. Supports "d" suffix for days. Empty or invalid input
// returns defaultRetention rather than disabling pruning — unbounded
// run/run_results growth is what disabled-by-default lets through.
const defaultRetention = 30 * 24 * time.Hour

func parseRetention(s string) time.Duration {
	if s == "" {
		return defaultRetention
	}
	// Support "Nd" shorthand for days.
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("invalid retention duration, using default", "value", s, "default", defaultRetention, "error", err)
		return defaultRetention
	}
	return d
}

// parsePoolqRetention is parseRetention's sibling for `swarm.poolq.retention`
// — returns 0 (not the 30-day db-row default) on empty/invalid input so
// `poolqmesh.NewAggregator` can substitute its own 7-day compile-time
// default rather than the wrong subsystem's value. Same "Nd" shorthand.
func parsePoolqRetention(s string) time.Duration {
	if s == "" {
		return 0
	}
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("invalid swarm.poolq.retention, using poolqmesh default", "value", s, "error", err)
		return 0
	}
	return d
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// applyRuntimeTuning configures Go GC and memory limits from the tuning section.
func applyRuntimeTuning(t config.TuningSection) {
	if t.GOGC != nil {
		old := debug.SetGCPercent(*t.GOGC)
		slog.Info("Go GC tuning applied", "gogc", *t.GOGC, "previous", old)
	}
	if t.MemoryLimit != "" {
		limit := parseMemoryLimit(t.MemoryLimit)
		if limit > 0 {
			old := debug.SetMemoryLimit(limit)
			slog.Info("Go memory limit applied", "limit", t.MemoryLimit, "bytes", limit, "previous", old)
		}
	}
}

// parseMemoryLimit parses a human-readable memory size (e.g. "4GiB", "512MiB",
// "1073741824") into bytes. Supports B, KiB, MiB, GiB, TiB suffixes.
func parseMemoryLimit(s string) int64 {
	if s == "" {
		return 0
	}

	type suffix struct {
		name string
		mult int64
	}
	suffixes := []suffix{
		{"TiB", 1 << 40},
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"B", 1},
	}

	for _, sf := range suffixes {
		if len(s) > len(sf.name) && s[len(s)-len(sf.name):] == sf.name {
			var n int64
			if _, err := fmt.Sscanf(s[:len(s)-len(sf.name)], "%d", &n); err == nil && n > 0 {
				return n * sf.mult
			}
		}
	}

	// Try plain integer (bytes).
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		return n
	}

	slog.Warn("invalid memory_limit, ignoring", "value", s)
	return 0
}

// metricsFleetAdapter wraps fleet.FleetIndex to satisfy metrics.FleetSource,
// breaking the metrics→fleet import cycle.
type metricsFleetAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *metricsFleetAdapter) ConvergenceSummary() metrics.ConvergenceSnapshot {
	s := a.fleetIndex.ConvergenceSummary()
	return metrics.ConvergenceSnapshot{
		Converged: s.Converged,
		Degraded:  s.Degraded,
		Failed:    s.Failed,
		NoData:    s.NoData,
		Changed:   s.Changed,
		Diverged:  s.Diverged,
		Offline:   s.Offline,
	}
}

func (a *metricsFleetAdapter) AllEnvoySnapshots() []metrics.EnvoySnapshot {
	all := a.fleetIndex.AllEnvoys()
	snapshots := make([]metrics.EnvoySnapshot, len(all))
	for i, es := range all {
		snapshots[i] = metrics.EnvoySnapshot{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
			ObservedCycle:     a.fleetIndex.EnvoyCycle(es.ID),
		}
	}
	return snapshots
}

// metricsSwarmAdapter satisfies metrics.SwarmSource by folding the
// swarmview substrate rollup, the four content-subsystem fleet
// aggregators, and the spanner bolt roster into fleet-total counts.
// The gitback/lockbox/longdrawer aggregators are constructed here and
// reused — each is a thin cache over the envoy_traits table, so the
// metrics path gets its own and never contends with the API handlers'
// caches; curator reuses the server's shared aggregator.
type metricsSwarmAdapter struct {
	queries       *db.Queries
	fleetIndex    *fleet.FleetIndex
	peerTracker   *swarm.PeerTracker
	manifestStore *swarm.ManifestStore
	gitback       *gitbackmesh.Aggregator
	lockbox       *lockboxmesh.Aggregator
	longdrawer    *longdrawermesh.Aggregator
	curator       *curatormesh.Aggregator
}

func newMetricsSwarmAdapter(s *Server) *metricsSwarmAdapter {
	return &metricsSwarmAdapter{
		queries:       s.queries,
		fleetIndex:    s.fleetIndex,
		peerTracker:   s.peerTracker,
		manifestStore: s.manifestStore,
		// nil puddle resolver — this adapter only reads project totals,
		// not the friendly-name display hint.
		gitback:    gitbackmesh.NewAggregator(s.queries, nil),
		lockbox:    lockboxmesh.NewAggregator(s.queries),
		longdrawer: longdrawermesh.NewAggregator(s.queries),
		curator:    s.curatorAgg,
	}
}

// meshInputs adapts the fleet roster to whichever content-subsystem
// EnvoyInput type mk returns — the four packages each declare their own
// identical {ID, Hostname, Revoked} shape.
func meshInputs[T any](envoys []fleet.EnvoyState, mk func(id, hostname string, revoked bool) T) []T {
	out := make([]T, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, mk(e.ID, e.Hostname, e.Revoked))
	}
	return out
}

func (a *metricsSwarmAdapter) SwarmSnapshot(ctx context.Context) metrics.SwarmSnapshot {
	var out metrics.SwarmSnapshot
	envoys := a.fleetIndex.AllEnvoys()

	// Substrate rollup — pure in-memory iteration over PeerTracker +
	// ManifestStore (swarmview nil-checks both, so a swarm-disabled
	// server just yields zeros).
	status := swarmview.SnapshotStatus(swarmview.Sources{
		Tracker:       a.peerTracker,
		ManifestStore: a.manifestStore,
		FleetSource: func() []swarmview.EnvoyInput {
			si := make([]swarmview.EnvoyInput, 0, len(envoys))
			for _, e := range envoys {
				si = append(si, swarmview.EnvoyInput{
					ID:       e.ID,
					Hostname: e.Hostname,
					LastSeen: e.LastSeen,
					Revoked:  e.Revoked,
					Offline:  e.Offline,
				})
			}
			return si
		},
	})
	out.SubstrateEnvoys = status.EnvoysSubstrateActive
	out.ManifestActive = status.ManifestEntriesActive
	out.FootprintBytes = status.TotalFootprintBytes

	// Content subsystems — each Snapshot() rebuilds from envoy_traits.
	if f, err := a.gitback.Snapshot(ctx, meshInputs(envoys, func(id, h string, r bool) gitbackmesh.EnvoyInput {
		return gitbackmesh.EnvoyInput{ID: id, Hostname: h, Revoked: r}
	})); err == nil {
		out.GitbackProjects = f.Totals.Projects
		out.GitbackEnvoys = f.Totals.Hosts
		out.GitbackDivergentRefs = f.Totals.DivergentRefs
	}
	if f, err := a.lockbox.Snapshot(ctx, meshInputs(envoys, func(id, h string, r bool) lockboxmesh.EnvoyInput {
		return lockboxmesh.EnvoyInput{ID: id, Hostname: h, Revoked: r}
	})); err == nil {
		out.LockboxUsers = f.Totals.Users
		out.LockboxFiles = f.Totals.Files
		out.LockboxDivergentFiles = f.Totals.Divergences
		out.LockboxFailedFiles = f.Totals.FailedFiles
		out.LockboxRecipientDrifts = f.Totals.RecipientDrifts
	}
	if f, err := a.longdrawer.Snapshot(ctx, meshInputs(envoys, func(id, h string, r bool) longdrawermesh.EnvoyInput {
		return longdrawermesh.EnvoyInput{ID: id, Hostname: h, Revoked: r}
	})); err == nil {
		out.LongdrawerUsers = f.Totals.Users
		out.LongdrawerFiles = f.Totals.Files
		out.LongdrawerDivergentFiles = f.Totals.Divergences
	}
	if a.curator != nil {
		if cat, err := a.curator.Snapshot(ctx, meshInputs(envoys, func(id, h string, r bool) curatormesh.EnvoyInput {
			return curatormesh.EnvoyInput{ID: id, Hostname: h, Revoked: r}
		})); err == nil {
			out.CuratorArtifacts = len(cat.Artifacts)
			out.CuratorPublishers = len(cat.Participants)
		}
	}

	// Spanner roster — active vs revoked bolts. An empty spanner_bolts
	// table (standalone server) yields zeros.
	if bolts, err := a.queries.ListBolts(ctx); err == nil {
		for _, b := range bolts {
			if b.RevokedAt.Valid {
				out.SpannerBoltsRevoked++
			} else {
				out.SpannerBoltsActive++
			}
		}
	}

	return out
}

// nvdCacheAdapter adapts db.Queries to the risk.NVDCache interface. The
// CWE list and references slice are stored as newline-joined strings in
// the schema's TEXT columns — keeps the schema flat and the queries
// generated by sqlc straightforward.
type nvdCacheAdapter struct {
	queries *db.Queries
}

func (a *nvdCacheAdapter) GetNVD(ctx context.Context, cveID string) (*risk.NVDDetail, error) {
	row, err := a.queries.GetCVECVSS(ctx, cveID)
	if err != nil {
		return nil, err
	}
	// Skip if stale (> 7 days). Caller treats a nil return as a cache
	// miss and triggers a fresh fetch.
	if time.Since(row.FetchedAt) > 7*24*time.Hour {
		return nil, nil
	}
	return &risk.NVDDetail{
		CVEID:       row.CveID,
		CVSSScore:   row.CvssScore,
		CVSSVector:  row.CvssVector,
		Severity:    row.Severity,
		Description: row.Description,
		CWE:         splitCacheField(row.Cwe),
		References:  splitCacheField(row.Refs),
		PublishedAt: row.PublishedAt,
	}, nil
}

func (a *nvdCacheAdapter) UpsertNVD(ctx context.Context, r risk.NVDDetail) error {
	return a.queries.UpsertCVECVSS(ctx, db.UpsertCVECVSSParams{
		CveID:       r.CVEID,
		CvssScore:   r.CVSSScore,
		CvssVector:  r.CVSSVector,
		Severity:    r.Severity,
		Description: r.Description,
		Cwe:         strings.Join(r.CWE, "\n"),
		Refs:        strings.Join(r.References, "\n"),
		PublishedAt: r.PublishedAt,
	})
}

// splitCacheField rebuilds a slice from the newline-joined cache form.
// Empty input yields a nil slice — distinguishes "no data" from "one
// empty entry".
func splitCacheField(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// cveLookupAdapter satisfies ai.CVELookup by delegating to the shared
// risk.NVDEnricher. Keeps the ai package free of a direct risk import;
// fields are copied across the boundary because each consumer owns its
// own projection of NVD data.
type cveLookupAdapter struct {
	enricher *risk.NVDEnricher
}

func (a *cveLookupAdapter) LookupCVE(ctx context.Context, cveID string) (*vigoai.CVEDetail, error) {
	d, err := a.enricher.Lookup(ctx, cveID)
	if err != nil || d == nil {
		return nil, err
	}
	return &vigoai.CVEDetail{
		CVEID:       d.CVEID,
		CVSSScore:   d.CVSSScore,
		CVSSVector:  d.CVSSVector,
		Severity:    d.Severity,
		Description: d.Description,
		CWE:         d.CWE,
		References:  d.References,
		PublishedAt: d.PublishedAt,
	}, nil
}

// collectFleetCVEIDs extracts unique CVE IDs from the unified
// security_scan.cves.N.id traits across all envoys.
func collectFleetCVEIDs(fleetIdx *fleet.FleetIndex) []string {
	seen := make(map[string]bool)
	for _, es := range fleetIdx.AllEnvoys() {
		if es.Revoked || es.Traits == nil {
			continue
		}
		for k, v := range es.Traits {
			if strings.HasPrefix(k, traits.PrefixCVEs) && strings.HasSuffix(k, "."+traits.CVEFieldID) && strings.HasPrefix(v, "CVE-") {
				seen[v] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// createAIProvider creates the LLM provider based on the AI config section.
// Returns an error if the provider cannot be initialized (e.g. Ollama unreachable).
func createAIProvider(ctx context.Context, cfg config.AISection) (vigoai.Provider, error) {
	switch cfg.Provider {
	case "ollama", "":
		p := vigoai.NewOllamaProvider(cfg.BaseURL)
		if err := p.EnsureModel(ctx, cfg.Model); err != nil {
			return nil, fmt.Errorf("ollama: %w", err)
		}
		return p, nil
	case "claude":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("claude provider requires ai.api_key")
		}
		return vigoai.NewClaudeProvider(cfg.APIKey), nil
	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("openai provider requires ai.api_key")
		}
		return vigoai.NewOpenAIProvider(cfg.APIKey), nil
	case "openai-compat":
		return vigoai.NewOpenAICompatProvider(cfg.BaseURL, cfg.APIKey), nil
	default:
		return nil, fmt.Errorf("unknown AI provider: %s", cfg.Provider)
	}
}

// createAuthProvider builds the appropriate auth provider from config.
// localAdminToken is the boot-minted root-only credential each TokenStore
// honors so server-host vigocli authenticates as admin.
func createAuthProvider(ctx context.Context, cfg *config.ServerConfig, sqlDB *sql.DB, queries *db.Queries, secretsProvider secrets.Provider, localAdminToken string, m *metrics.Metrics) (vigoauth.Provider, error) {
	method := cfg.Auth.Method
	if method == "" {
		method = "basic"
	}

	// Parse idle timeout (default 15m per HIPAA 164.312(a)(2)(iii)).
	var idleTimeout time.Duration
	if cfg.Auth.SessionIdleTimeout != "" {
		d, err := time.ParseDuration(cfg.Auth.SessionIdleTimeout)
		if err != nil {
			return nil, fmt.Errorf("parsing session_idle_timeout: %w", err)
		}
		idleTimeout = d
	}

	switch method {
	case "basic":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries, localAdminToken, m)
		return vigoauth.NewBasicProvider(sessions, userStore, tokenStore, secretsProvider), nil

	case "oidc":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries, localAdminToken, m)
		return vigoauth.NewOIDCProvider(ctx, vigoauth.OIDCConfig{
			Issuer:       cfg.Auth.OIDC.Issuer,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
		}, sessions, userStore, tokenStore)

	case "isowebauth":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries, localAdminToken, m)
		return vigoauth.NewIsowebauthProvider(vigoauth.IsowebauthConfig{
			Namespace: cfg.Auth.Isowebauth.Namespace,
		}, sessions, userStore, tokenStore)

	default:
		return nil, fmt.Errorf("unknown auth method: %q (must be basic, oidc, or isowebauth)", method)
	}
}

// buildPeerTLS creates a *tls.Config for the primary's outbound connections to peers.
// Uses the same server cert/key/CA as the main gRPC server (mutual TLS).
func buildPeerTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// cveSearchAdapter adapts *api.CVEHandler to ui.CVESearcher.
type cveSearchAdapter struct {
	handler *api.CVEHandler
}

func (a *cveSearchAdapter) SearchCVE(cveID string, runningOnly bool) (int, []ui.CVEMatch) {
	result := a.handler.Search(cveID, runningOnly)
	matches := make([]ui.CVEMatch, len(result.Hosts))
	for i, h := range result.Hosts {
		running := make([]ui.CVERunningBinaryRef, len(h.RunningBinaries))
		for j, rb := range h.RunningBinaries {
			running[j] = ui.CVERunningBinaryRef{
				BinaryPath:     rb.BinaryPath,
				Package:        rb.Package,
				PackageVersion: rb.PackageVersion,
				PidCount:       rb.PidCount,
				Deleted:        rb.Deleted,
			}
		}
		matches[i] = ui.CVEMatch{
			Hostname:        h.Hostname,
			Package:         h.Package,
			Installed:       h.Installed,
			Fixed:           h.Fixed,
			Severity:        h.Severity,
			Scanner:         h.Scanner,
			RunningBinaries: running,
		}
	}
	return result.TotalHosts, matches
}

// serveLicenseFile returns an http.HandlerFunc that serves a license file from the server root.
func serveLicenseFile(serverRoot, filename string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(serverRoot, filename))
	}
}

// healthCheckAdapter adapts *api.DoctorHandler to ui.HealthChecker.
type healthCheckAdapter struct {
	handler *api.DoctorHandler
}

func (a *healthCheckAdapter) RunChecks() (string, []ui.HealthCheck) {
	status, checks := a.handler.RunChecks()
	uiChecks := make([]ui.HealthCheck, len(checks))
	for i, c := range checks {
		uiChecks[i] = ui.HealthCheck{
			Name:    c.Name,
			Status:  string(c.Status),
			Scope:   string(c.Scope),
			Message: c.Message,
		}
	}
	return status, uiChecks
}

// traitQueryAdapter wraps grpc.AgentService to satisfy ui.TraitQueryDispatcher.
// Used by the security drawer's live-detail fetch and any future
// fragment handler that needs a single-envoy on-demand trait pull.
type traitQueryAdapter struct {
	agentSvc *isgrpc.AgentService
}

func (a *traitQueryAdapter) DispatchTraitQuery(ctx context.Context, traitPath, hostname string, timeout time.Duration) (string, ui.DispatchOutcome, error) {
	res, err := a.agentSvc.DispatchQuery(ctx, traitPath, hostname, timeout)
	if err != nil {
		return "", ui.DispatchTimeout, err
	}
	// First a responded-with-data entry wins.
	for _, entry := range res.Responded {
		if entry.Found {
			return entry.Value, ui.DispatchResponded, nil
		}
	}
	// No usable response. Classify the reason in priority order so the
	// caller can render a precise message: offline (most actionable) →
	// timeout (envoy was connected but never answered) → no-data (agent
	// answered with found=false, currently latent in the agent code path).
	switch {
	case len(res.Offline) > 0:
		return "", ui.DispatchOffline, nil
	case len(res.TimedOut) > 0:
		return "", ui.DispatchTimeout, nil
	default:
		return "", ui.DispatchNoData, nil
	}
}

// traitLookupAdapter wraps fleet.FleetIndex to satisfy ui.TraitLookup.
type traitLookupAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *traitLookupAdapter) EnvoyTrait(envoyID, key string) string {
	if state := a.fleetIndex.GetEnvoy(envoyID); state != nil {
		return state.Traits[key]
	}
	return ""
}

func (a *traitLookupAdapter) EnvoyTagsByHostname(hostname string) []string {
	if a.fleetIndex == nil || hostname == "" {
		return nil
	}
	for _, e := range a.fleetIndex.AllEnvoys() {
		if e.Hostname == hostname {
			if len(e.Tags) == 0 {
				return nil
			}
			out := make([]string, len(e.Tags))
			copy(out, e.Tags)
			return out
		}
	}
	return nil
}

// swarmHostnameAdapter wraps fleet.FleetIndex to satisfy ui.SwarmHostnameResolver.
type swarmHostnameAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *swarmHostnameAdapter) ResolveHostname(envoyID string) string {
	if state := a.fleetIndex.GetEnvoy(envoyID); state != nil {
		return state.Hostname
	}
	return ""
}

func (a *swarmHostnameAdapter) ResolveIP(ip string) string {
	for _, es := range a.fleetIndex.AllEnvoys() {
		if es.IP == ip {
			return es.Hostname
		}
	}
	return ""
}

func (a *swarmHostnameAdapter) MatchingEnvoys(target string) []string {
	var names []string
	for _, es := range a.fleetIndex.AllEnvoys() {
		if !es.Revoked && swarm.MatchTarget(es.Hostname, es.Traits, target) {
			names = append(names, es.Hostname)
		}
	}
	return names
}

// parseLivenessTTL parses the `swarm.manifest_liveness_ttl` config knob
// into a time.Duration. Empty / zero / negative input disables the
// liveness sweep (the pruner skips it). Default 24h matches the
// documented behavior on operators who omit the knob.
func parseLivenessTTL(s string) time.Duration {
	if s == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("swarm: invalid manifest_liveness_ttl, defaulting to 24h",
			"value", s, "error", err)
		return 24 * time.Hour
	}
	if d < 0 {
		return 0
	}
	return d
}

// sameStringSet reports whether `a` and `b` contain the same elements
// regardless of order or duplicates. Used by the publish hook to decide
// whether a subsystem's authorization patterns materially changed across
// a reload.
// formatLocalBoltPubkey returns the first-16-char hex of the
// loaded bolt identity's raw 32-byte pubkey, or "" if no
// identity is loaded. Surfaced in /api/v1/spanner/status as
// `local_bolt_pubkey` for operator cross-check on alarms.
func formatLocalBoltPubkey(id *bolt.Identity) string {
	if id == nil {
		return ""
	}
	pk := []byte(id.PublicKey)
	if len(pk) < 8 {
		return ""
	}
	const out = 16
	hexStr := make([]byte, 0, out)
	const digits = "0123456789abcdef"
	for _, b := range pk[:out/2] {
		hexStr = append(hexStr, digits[b>>4], digits[b&0xf])
	}
	return string(hexStr)
}

// boltSigningKeyOrNil returns the bolt identity's signing key
// or nil when the identity wasn't loaded (non-spanner mode, or
// non-local secrets backend). nil propagates through to
// spanner.NewReporter, which produces legacy unsigned reports —
// hubs accept those without pinning during mixed-version rollout.
func boltSigningKeyOrNil(id *bolt.Identity) ed25519.PrivateKey {
	if id == nil {
		return nil
	}
	return id.SigningKey
}

// fullBoltPubkeyHex returns the full lowercase-hex encoding of the
// loaded bolt identity's raw 32-byte pubkey, or "" if no identity is
// loaded. This is the canonical identifier stored in
// spanner_bolts.pubkey — the roster gossiper compares against it to
// skip its own snapshot. (formatLocalBoltPubkey truncates to 16 hex
// chars for operator display and must not be used for identity
// matching.)
func fullBoltPubkeyHex(id *bolt.Identity) string {
	if id == nil {
		return ""
	}
	return hex.EncodeToString(id.PublicKey)
}

// apiListenPort extracts the port from an api_listen address
// (host:port, e.g. ":8443" or "0.0.0.0:8443"). The spanner roster
// gossiper builds peer snapshot URLs as https://<host>:<port>/... on
// the same-port assumption. Falls back to "8443" — the documented
// default — when the address has no parseable port.
func apiListenPort(apiListen string) string {
	if _, port, err := net.SplitHostPort(apiListen); err == nil && port != "" {
		return port
	}
	return "8443"
}

// grpcListenPort extracts the port from a grpc_listen address
// (host:port, e.g. ":1530"). The spanner peer-equal fanout admin
// path (Slice 2-ii-a) dials each roster bolt's admin gRPC at
// <hostname>:<port> on the same-port assumption. Falls back to
// "1530" — the documented default — when the address has no
// parseable port.
func grpcListenPort(grpcListen string) string {
	if _, port, err := net.SplitHostPort(grpcListen); err == nil && port != "" {
		return port
	}
	return "1530"
}

// lookupStockpileProjectID returns the hex project_id of the
// ops-puddle's stockpile gitback project on this vigosrv, by
// walking <opsPuddleDir>/.vigo-gitback/<id>/project.json files
// and matching on handle == "stockpile". Returns ("", false) when
// no stockpile project state exists locally — startup uses this
// to skip the watcher until the operator runs
// `vigocli spanner stockpile init`.
func lookupStockpileProjectID(opsPuddleDir string) (string, bool) {
	gitbackDir := filepath.Join(opsPuddleDir, ".vigo-gitback")
	entries, err := os.ReadDir(gitbackDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		configPath := filepath.Join(gitbackDir, e.Name(), "project.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		var cfg struct {
			Handle string `json:"handle"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.Handle == "stockpile" {
			return e.Name(), true
		}
	}
	return "", false
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		// Length mismatch is sufficient when callers don't pass dups —
		// they don't here, so the cheap check is correct.
		seen := make(map[string]struct{}, len(a))
		for _, s := range a {
			seen[s] = struct{}{}
		}
		for _, s := range b {
			if _, ok := seen[s]; !ok {
				return false
			}
		}
		// Same elements but different cardinalities — treat as changed.
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := seen[s]; !ok {
			return false
		}
	}
	return true
}

// recordSwarmPatternsChange emits a tamper-evident audit-chain entry
// whenever a swarm authorization pattern list changes. `substrate` is
// "swarm" for the top-level pattern, or one of
// "puddle"/"gitback"/"longdrawer"/"filecast" for the per-subsystem
// patterns. The summary states the pattern delta; the payload carries
// the full new list so a reviewer can reconstruct post-change state
// without joining log streams. Failures are logged at WARN — the audit
// chain is best-effort from the publish hook (the server can't roll
// back a config publish that already landed on disk just because a
// chain insert failed).
func recordSwarmPatternsChange(ctx context.Context, w *audit.Writer, substrate string, oldList, newList []string) {
	if w == nil {
		return
	}
	eventType := "swarm.enabled"
	if substrate != "swarm" {
		eventType = fmt.Sprintf("swarm.%s.enabled", substrate)
	}
	added, removed := diffStringSets(oldList, newList)
	summary := blacklistChangeSummary(added, removed)
	payload := fmt.Sprintf(`{"substrate":%q,"added":%s,"removed":%s,"new_list":%s}`,
		substrate, jsonStringArray(added), jsonStringArray(removed), jsonStringArray(newList))
	if err := w.Record(ctx, eventType, "config.reload", "config", summary, payload); err != nil {
		slog.Warn("audit: failed to record swarm patterns change",
			"substrate", substrate, "added", added, "removed", removed, "error", err)
	}
}

// diffStringSets returns (added, removed) — elements present in next but
// not prev, and elements present in prev but not next. Stable order is the
// order of appearance in the respective list.
func diffStringSets(prev, next []string) (added, removed []string) {
	prevSet := make(map[string]struct{}, len(prev))
	for _, s := range prev {
		prevSet[s] = struct{}{}
	}
	nextSet := make(map[string]struct{}, len(next))
	for _, s := range next {
		nextSet[s] = struct{}{}
	}
	for _, s := range next {
		if _, ok := prevSet[s]; !ok {
			added = append(added, s)
		}
	}
	for _, s := range prev {
		if _, ok := nextSet[s]; !ok {
			removed = append(removed, s)
		}
	}
	return added, removed
}

func blacklistChangeSummary(added, removed []string) string {
	switch {
	case len(added) > 0 && len(removed) > 0:
		return fmt.Sprintf("added: %s; removed: %s",
			strings.Join(added, ", "), strings.Join(removed, ", "))
	case len(added) > 0:
		return "added: " + strings.Join(added, ", ")
	case len(removed) > 0:
		return "removed: " + strings.Join(removed, ", ")
	default:
		return "no change"
	}
}

func jsonStringArray(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

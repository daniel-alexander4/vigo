// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/hardening"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/steorra"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/traits"
	"git.alexander4.io/vigo/ui"
)

// version is set at build time via -ldflags="-X main.version=1.2.3".
// The default should match the VERSION file at the repo root.
var version = "0.1.3"

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

	// --- Binary integrity and anti-tamper checks ---
	hardening.DetectDebugger()
	hardening.DetectInjection()
	if err := hardening.VerifyBinarySignature(); err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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

// uiFleetSourceAdapter adapts *fleet.FleetIndex to ui.FleetSource so
// the dashboard convergence pills go through the same in-memory
// counters as the metrics collector — eliminating the previous drift
// between FleetCache (rebuilt every ~10s from the DB) and FleetIndex.
type uiFleetSourceAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *uiFleetSourceAdapter) ConvergenceSummary() ui.FleetSummary {
	c := a.fleetIndex.ConvergenceSummary()
	return ui.FleetSummary{
		Total:     c.Total,
		Converged: c.Converged,
		Degraded:  c.Degraded,
		Failed:    c.Failed,
		NoData:    c.NoData,
		Changed:   c.Changed,
		Diverged:  c.Diverged,
		Offline:   c.Offline,
	}
}

// EnvoyCycle resolves an envoy's smoothed observed inter-CheckIn
// cadence from the live FleetIndex. Used by the FleetCache so per-envoy
// staleness thresholds reflect agent-reported cadence rather than
// configured interval alone.
func (a *uiFleetSourceAdapter) EnvoyCycle(id string) time.Duration {
	return a.fleetIndex.EnvoyCycle(id)
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
func createAuthProvider(ctx context.Context, cfg *config.ServerConfig, sqlDB *sql.DB, queries *db.Queries, secretsProvider secrets.Provider) (vigoauth.Provider, error) {
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
		tokenStore := vigoauth.NewTokenStore(queries)
		return vigoauth.NewBasicProvider(sessions, userStore, tokenStore, secretsProvider), nil

	case "oidc":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries)
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
		tokenStore := vigoauth.NewTokenStore(queries)
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
// formatLocalSteorraPubkey returns the first-16-char hex of the
// loaded steorra identity's raw 32-byte pubkey, or "" if no
// identity is loaded. Surfaced in /api/v1/tungol/status as
// `local_steorra_pubkey` for operator cross-check on alarms.
func formatLocalSteorraPubkey(id *steorra.Identity) string {
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

// steorraSigningKeyOrNil returns the steorra identity's signing key
// or nil when the identity wasn't loaded (non-tungol mode, or
// non-local secrets backend). nil propagates through to
// tungol.NewReporter, which produces legacy unsigned reports —
// hubs accept those without pinning during mixed-version rollout.
func steorraSigningKeyOrNil(id *steorra.Identity) ed25519.PrivateKey {
	if id == nil {
		return nil
	}
	return id.SigningKey
}

// lookupStockpileProjectID returns the hex project_id of the
// ops-puddle's stockpile gitback project on this vigosrv, by
// walking <opsPuddleDir>/.vigo-gitback/<id>/project.json files
// and matching on handle == "stockpile". Returns ("", false) when
// no stockpile project state exists locally — startup uses this
// to skip the watcher until the operator runs
// `vigocli tungol stockpile init`.
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


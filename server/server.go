// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	licfmt "git.alexander4.io/tessera/license/products/vigo"
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/api"
	"git.alexander4.io/vigo/server/audit"
	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/bolt"
	"git.alexander4.io/vigo/server/bootstrap"
	"git.alexander4.io/vigo/server/ca"
	"git.alexander4.io/vigo/server/compliancedocs"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/curators3"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/grc"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/hardening"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/publish"
	"git.alexander4.io/vigo/server/puddle"
	"git.alexander4.io/vigo/server/registry"
	"git.alexander4.io/vigo/server/risk"
	vigoscrier "git.alexander4.io/vigo/server/scrier"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/stockpilewatch"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
	"git.alexander4.io/vigo/server/swarm/gitbackmesh"
	"git.alexander4.io/vigo/server/swarm/poolqmesh"
	"git.alexander4.io/vigo/server/task"
	"git.alexander4.io/vigo/server/tickets"
	"git.alexander4.io/vigo/server/workflows"
	"git.alexander4.io/vigo/ui"
)

// Server is one assembled vigosrv instance. It owns the per-process state
// every subsystem needs to operate — config, paths, identity keys, secrets
// provider, prometheus collectors, and (as Phase B progresses) the
// database, fleet index, hub, gateways, etc.
//
// Construction is split from lifecycle:
//
//   - NewServer(ctx, configFile) performs construction only. No signal
//     handlers, no os.Exit, no background goroutines. The caller installs
//     signal handlers and runs hardening checks before calling NewServer.
//   - (*Server).Run(ctx) starts background goroutines and blocks until
//     ctx is cancelled. Returns nil on clean shutdown.
//   - (*Server).Close() tears down state in reverse construction order.
//     Safe to defer immediately after NewServer returns (idempotent).
//
// Per-instance shape so multiple servers can coexist in one Go test
// process (the Phase 2 spanner harness wedge). Every field that would
// otherwise live as a package global was refactored to a *Server field
// in Phase A; see commit d5c0b4e4 + the audit at
// /tmp/vigo-server-package-state-audit-2026-05-18.md for the catalog.
type Server struct {
	// --- Prometheus (per-instance registry + collector set) ---
	promReg   *prometheus.Registry
	metrics   *metrics.Metrics
	aiMetrics *vigoai.Metrics

	// --- Config ---
	configFile string // path passed to NewServer
	serverRoot string // filepath.Dir(configFile)
	// localAdminToken is the root-only credential (serverRoot/cli-admin-token,
	// 0600) that lets vigocli on the server host authenticate as admin —
	// minted at boot, see auth.LoadOrCreateLocalAdminToken.
	localAdminToken string
	cfg             *config.ServerConfig // full config (second pass with secrets resolved)
	secretsCfg      secrets.Config       // first-pass secrets backend config

	// --- Resolved paths (env var > cfg.Paths.* > built-in default) ---
	migrationsDir   string
	traitsDir       string
	docsDir         string
	taskDir         string
	workflowsDir    string
	agentDir        string
	licenseDir      string
	licenseGraceDir string

	// --- Identity / secrets ---
	provider     secrets.Provider
	puddleID     *puddle.Identity // non-nil only on local secrets backend
	boltID       *bolt.Identity   // non-nil only when mode != standalone AND local backend
	ticketsStore *tickets.Store   // non-nil only on local secrets backend; rotated by Run()
	mode         config.SpannerMode

	// --- Database ---
	db          *sql.DB // SQLite: single-writer pool. Postgres: the pool.
	readDB      *sql.DB // SQLite read pool (nil for Postgres)
	isPostgres  bool
	queries     *db.Queries
	auditWriter *audit.Writer

	// --- License ---
	licenseMode string
	enforcer    *license.Enforcer
	// cancel is the process-lifecycle cancel func captured from
	// signal.NotifyContext in main(). The license enforcer's
	// stop-service callback invokes it on terminal license failure
	// (downgrade past grace, expiry past grace, fingerprint mismatch).
	// Construction-only; Run does not own it.
	cancel context.CancelFunc

	// --- Auth ---
	authProvider vigoauth.Provider
	authMethod   string

	// --- Registry + initial subsystems ---
	reg          *registry.Registry
	publishState *publish.State
	configStore  *config.ConfigStore

	// --- Tuning ---
	baseInterval     time.Duration
	offlineThreshold time.Duration

	// --- Tuning parsed durations (computed once at construction) ---
	sigWindow        time.Duration
	lastSeenFlush    time.Duration
	metricsInterval  time.Duration
	traitsScannerTTL time.Duration
	maxConnAge       time.Duration
	maxConnIdle      time.Duration
	kaTime           time.Duration
	kaTimeout        time.Duration

	// --- Run + integrations + task store ---
	// (Workflow engine stays in run() for now — workflows.NewEngine
	// requires the agentSvc TaskRunner which is constructed in chunk 4.)
	runStore     db.RunStore
	runBatcher   *fleet.RunBatcher
	integrations *integrations.Dispatcher
	taskStore    *task.Store

	// --- gRPC bundle signer (ephemeral key, regenerated per process start) ---
	bundleSigner *isgrpc.BundleSigner
	bundleMaxAge uint32 // seconds

	// --- Agent binary distribution (per-arch versions for auto-update) ---
	agentVersions   map[string]string // GOOS/GOARCH → version string
	agentVersion    string            // server-arch version (UI display fallback)
	fallbackServers []string          // from cfg.Peers.Servers, returned to agents on registration

	// --- Risk / NVD CVE enrichment (nil when cfg.Risk.NVD.Enabled is false) ---
	nvdEnricher *risk.NVDEnricher

	// --- Swarm substrate (nil when role==spoke OR hostname does not match cfg.Swarm.Enabled) ---
	swarmEnabled  bool
	swarmSeedDir  string
	peerTracker   *swarm.PeerTracker
	manifestStore *swarm.ManifestStore
	curatorAgg    *curatormesh.Aggregator
	poolqAgg      *poolqmesh.Aggregator

	// --- Stockpile watcher (nil in standalone mode or when the ops-puddle
	// stockpile project has not been founded yet). ---
	stockpileWatcher *stockpilewatch.Watcher

	// --- Peer replication (nil when cfg.Peers.Servers is empty).
	// peerService is registered on the gRPC server in NewServer; peerTLS
	// is shared with the primary-side push goroutine. peerPrimary is
	// assigned in Run when cfg.Peers.Role == "primary" and stopped in
	// Close. ---
	peerService *peer.Service
	peerTLS     *tls.Config
	peerPrimary *peer.Primary

	// --- Forward-declared for closure access (assigned during NewServer
	// construction below). envoyCounter (the license enforcer's count
	// callback) captures these by reading through the *Server. Both stay
	// nil through the license-enforcer assignment and are populated
	// during the fleet-rehydrate + spanner-admin stages farther down
	// NewServer.
	spannerAdmin *spanner.SpannerAdmin
	fleetIndex   *fleet.FleetIndex

	// --- gRPC server + services. Constructed in chunk 4. peerService is
	// registered on grpcSrv after construction; the actual grpcSrv.Serve
	// goroutine still starts from run() in chunk 5. ---
	grpcSrv     *grpc.Server
	certCache   *isgrpc.CertCache
	traitsBatch *db.TraitsBatcher
	agentSvc    *isgrpc.AgentService
	adminSvc    *isgrpc.AdminService

	// --- Spanner roster gossiper (Slice 2-i-a; nil in standalone mode).
	// Pulls every peer's /snapshot on the Tier-2 timer and CRDT-merges
	// it. The `go spannerGossip.Run` goroutine starts from run(). ---
	spannerGossip *spanner.Gossiper

	// --- Workflow engine. Construction + Load + RecoverInterrupted +
	// agentSvc.SetWorkflowTrigger wiring all live here. ---
	workflowEngine *workflows.Engine

	// --- AI assistant (nil unless cfg.AI.Enabled AND provider init
	// succeeded). aiProvider is held so chunk 5 can Ollama-warm in a
	// goroutine post-construction. ---
	aiProvider vigoai.Provider
	assistant  *vigoai.Assistant
	aiHandler  *api.AIHandler

	// --- Scrier (nil unless cfg.Scrier.Enabled). ---
	scrierHandler *vigoscrier.Handler

	// --- REST gateway. uiHandler is captured for closure / post-construction
	// wiring. reportHandler is captured for the daily compliance snapshot
	// job which routes through the unified scorer. ---
	gwMux         *http.ServeMux
	uiHandler     *ui.Handler
	reportHandler *api.ReportHandler

	// --- GRC evidence pusher (nil unless cfg.GRC.Integrations is
	// non-empty). The `.Start(ctx)` goroutine stays in chunk 5. ---
	grcPusher *grc.Pusher

	// --- Compliance docs store (nil under non-local secrets backends;
	// REST routes return 503 in that case). ---
	complianceDocsStore *compliancedocs.Store

	// --- HTTP server: pure struct construction. The
	// `httpSrv.ListenAndServeTLS` goroutine stays in chunk 5. ---
	httpSrv *http.Server
	// s3Srv is the opt-in curator S3-compatible API listener (ADR-027);
	// nil unless swarm.curator.s3_port is set and the `local` backend runs.
	s3Srv *http.Server
}

// NewServer constructs a *Server from configFile. Performs no I/O beyond
// reading the config + opening the database + materializing identity
// keys on first boot; does not open network listeners, install signal
// handlers, or start background goroutines. Caller must defer
// (*Server).Close().
//
// The ctx is held by sub-constructors that may need to abort early
// (e.g. database PingContext during a slow Postgres handshake). It is
// not the same as the ctx passed to Run — Run takes the process
// lifecycle context.
//
// The cancel func is the process-lifecycle cancel captured from
// signal.NotifyContext in main(); the license enforcer's stop-service
// callback invokes it when the license fails terminally. NewServer
// stores it on the Server so the enforcer construction can wire it,
// but does not invoke it directly.
func NewServer(ctx context.Context, cancel context.CancelFunc, configFile string) (*Server, error) {
	s := &Server{
		configFile: configFile,
		serverRoot: filepath.Dir(configFile),
		cancel:     cancel,
	}

	// Per-instance prometheus registry + collector set. Two *Server
	// instances in one Go test process never share collector state —
	// each builds its own *Metrics against a per-instance *Registry.
	s.promReg = prometheus.NewRegistry()
	s.metrics = metrics.NewMetrics(s.promReg)
	s.aiMetrics = vigoai.NewMetrics(s.promReg)

	// Load secrets backend config (first pass — no secret resolution).
	secretsCfg, err := config.LoadSecretsConfig(configFile)
	if err != nil {
		return nil, fmt.Errorf("loading secrets config: %w", err)
	}
	s.secretsCfg = secretsCfg

	// Initialize secrets provider from config.
	s.provider, err = secrets.NewFromConfig(secretsCfg)
	if err != nil {
		return nil, fmt.Errorf("initializing secrets provider: %w", err)
	}
	slog.Info("secrets provider initialized", "backend", secretsCfg.Backend)

	// Auto-generate self-signed TLS certs on first start (local backend only).
	if local, ok := s.provider.(*secrets.LocalProvider); ok {
		tlsDir := filepath.Join(s.serverRoot, "tls")
		extraSANs := config.LoadServerCertSANs(configFile)
		if err := secrets.EnsureTLSCerts(tlsDir, extraSANs...); err != nil {
			return nil, fmt.Errorf("auto-generating TLS certificates: %w", err)
		}

		// Service-account puddle identity (ADR-025 Slice 1). Generated on
		// first boot under /srv/vigo/puddle/ and unwrapped via HKDF over
		// the secrets master key — no operator unlock required. Held in
		// memory for sibling subsystems that sign as the vigosrv puddle
		// (gitback push of the ops-puddle's stockpile project, friendly-
		// name claim, etc.). Isopass and other non-local backends cannot
		// host the service-account puddle in v1; vigosrv runs without one
		// in that mode.
		s.puddleID, err = puddle.EnsureIdentity(local, puddle.DefaultDir)
		if err != nil {
			return nil, fmt.Errorf("service-account puddle: %w", err)
		}
		slog.Info("service-account puddle ready",
			"pubkey", s.puddleID.PublicKeyText,
			"dir", puddle.DefaultDir,
		)

		// Persistent TLS 1.3 session-ticket keys. Without persistence Go's
		// crypto/tls auto-generates per-process keys, so every vigosrv
		// restart invalidates every cached PSK ticket in the fleet and
		// forces a full handshake on the next CheckIn from every envoy.
		// Same HKDF root as puddle/bolt, distinct purpose so the
		// derivations don't cross. Keys are rotated daily in Run().
		s.ticketsStore, err = tickets.EnsureStore(local, tickets.DefaultDir)
		if err != nil {
			return nil, fmt.Errorf("tls ticket-key store: %w", err)
		}
		slog.Info("tls ticket-key store ready", "dir", tickets.DefaultDir, "keys", len(s.ticketsStore.Keys()))
	}

	// Load full server config with secret resolution (second pass).
	s.cfg, err = config.LoadServerConfig(configFile, s.provider, secretsCfg.Backend)
	if err != nil {
		return nil, fmt.Errorf("loading server config: %w", err)
	}

	// Resolve directory paths: env var > server.yaml paths: > default.
	s.migrationsDir = envOrDefault("VIGO_MIGRATIONS_DIR", stringOrDefault(s.cfg.Paths.Migrations, "migrations"))
	s.traitsDir = envOrDefault("VIGO_CUSTOM_TRAITS_DIR", stringOrDefault(s.cfg.Paths.CustomTraits, "custom-traits"))
	s.docsDir = envOrDefault("VIGO_DOCS_DIR", stringOrDefault(s.cfg.Paths.Docs, "/seed/docs"))
	s.taskDir = envOrDefault("VIGO_TASK_DIR", stringOrDefault(s.cfg.Paths.Tasks, filepath.Join(publish.DefaultLiveDir, "tasks")))
	s.workflowsDir = envOrDefault("VIGO_WORKFLOWS_DIR", stringOrDefault(s.cfg.Paths.Workflows, filepath.Join(publish.DefaultLiveDir, "workflows")))
	s.agentDir = envOrDefault("VIGO_AGENT_DIR", stringOrDefault(s.cfg.Paths.AgentDist, "dist/agent"))
	s.licenseDir = stringOrDefault(s.cfg.Paths.LicenseDir, licfmt.DefaultLicenseDir)
	s.licenseGraceDir = s.licenseDir

	// Determine spanner mode.
	s.mode = s.cfg.Spanner.Mode
	if s.mode == "" {
		s.mode = config.ModeStandalone
	}
	if err := s.cfg.Spanner.Validate(); err != nil {
		return nil, err
	}

	// Phase 1 (ADR-spanner): per-bolt Ed25519 identity. Generated on
	// first boot under /srv/vigo/bolt/, wrapped via HKDF purpose
	// `vigo-bolt-wrap-v1` over the secrets master key. Used by the
	// reporter to sign every announcement so peers can verify + pin
	// (spanner_id, bolt_pubkey). Only required in spanner mode +
	// local secrets backend; isopass and other backends run without a
	// bolt identity (legacy unsigned reports, accepted without
	// pinning).
	if s.mode != config.ModeStandalone {
		if local, ok := s.provider.(*secrets.LocalProvider); ok {
			id, err := bolt.EnsureIdentity(local, bolt.DefaultDir)
			if err != nil {
				return nil, fmt.Errorf("bolt identity: %w", err)
			}
			s.boltID = id
			slog.Info("bolt identity ready",
				"pubkey", id.PublicKeyText,
				"spanner_id", s.cfg.Spanner.SpannerID,
				"dir", bolt.DefaultDir,
			)
		} else {
			slog.Warn("bolt identity skipped — non-local secrets backend; reports will be unsigned (mixed-version-compatible)",
				"backend", secretsCfg.Backend,
			)
		}
	}

	slog.Info("server config loaded",
		"mode", s.mode,
		"grpc_listen", s.cfg.Server.GRPCListen,
		"api_listen", s.cfg.Server.APIListen,
	)

	// Apply Go runtime tuning early (before allocations ramp up).
	applyRuntimeTuning(s.cfg.Tuning)

	// --- Database ---

	// Determine database driver.
	dbDriver := s.cfg.Database.Driver
	if dbDriver == "" {
		dbDriver = "sqlite"
	}
	s.isPostgres = dbDriver == "postgres"

	// Open database connection(s).
	if s.isPostgres {
		s.db, err = sql.Open("postgres", s.cfg.Database.DSN)
		if err != nil {
			return nil, fmt.Errorf("opening database (%s): %w", dbDriver, err)
		}
		// Bound the pool so a write storm can't overrun Postgres's
		// server-side max_connections. Pure-Go lib/pq won't OS-thread-exhaust
		// the way the CGO SQLite driver does, but an unbounded client pool
		// still exhausts the server.
		pgN := s.cfg.Database.MaxOpenConns
		if pgN <= 0 {
			pgN = 25
		}
		s.db.SetMaxOpenConns(pgN)
		s.db.SetMaxIdleConns(pgN)
	} else {
		// SQLite: two pools to the same file. The write pool holds a single
		// connection so writes serialize on it — no writer-lock collisions and
		// no CGO thread explosion (excess writers park on the connection queue,
		// which costs a goroutine, not a pinned OS thread). The read pool is
		// bounded but concurrent; WAL permits readers alongside the one writer.
		// Per-connection pragmas travel in the DSN so EVERY pooled connection
		// gets them — the previous single ExecContext only ever configured one
		// pooled connection, leaving the rest without busy_timeout/foreign_keys.
		dsn := sqliteDSN(s.cfg.Database.DSN)
		s.db, err = sql.Open("sqlite3", dsn) // write pool
		if err != nil {
			return nil, fmt.Errorf("opening database (write pool): %w", err)
		}
		s.db.SetMaxOpenConns(1)
		s.db.SetMaxIdleConns(1)

		s.readDB, err = sql.Open("sqlite3", dsn) // read pool
		if err != nil {
			return nil, fmt.Errorf("opening database (read pool): %w", err)
		}
		readN := s.cfg.Database.MaxOpenConns
		if readN <= 0 {
			readN = runtime.NumCPU()
			if readN < 4 {
				readN = 4
			}
		}
		s.readDB.SetMaxOpenConns(readN)
		s.readDB.SetMaxIdleConns(readN)

		// auto_vacuum is a persistent DB-level setting, not per-connection, so
		// it's applied once on the write handle rather than via the DSN.
		if _, err := s.db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			return nil, fmt.Errorf("setting auto_vacuum: %w", err)
		}
	}

	if err := s.db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	slog.Info("database connected", "driver", dbDriver)

	// Run migrations (use driver-specific migration directory for Postgres).
	migDir := s.migrationsDir
	if s.isPostgres {
		migDir = filepath.Join(s.migrationsDir, "postgres")
	}
	absPath, err := filepath.Abs(migDir)
	if err != nil {
		return nil, fmt.Errorf("resolving migrations path: %w", err)
	}
	var migDSN string
	if s.isPostgres {
		migDSN = s.cfg.Database.DSN
	} else {
		migDSN = "sqlite3://" + s.cfg.Database.DSN
	}
	mig, err := migrate.New("file://"+absPath, migDSN)
	if err != nil {
		return nil, fmt.Errorf("creating migrate instance: %w", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	srcErr, dbErr := mig.Close()
	if srcErr != nil {
		return nil, fmt.Errorf("closing migrate source: %w", srcErr)
	}
	if dbErr != nil {
		return nil, fmt.Errorf("closing migrate db: %w", dbErr)
	}
	slog.Info("migrations complete")

	// Initialize query layer early — needed for license envoy count.
	// Postgres: wrap the single pool with the placeholder rewriter so
	// sqlc-generated `?` queries work ($1, $2, ...). SQLite: route reads to
	// the read pool and writes to the single-writer pool via SplitDB.
	var dbtx db.DBTX
	if s.isPostgres {
		dbtx = &db.PgDBTX{DB: s.db}
	} else {
		dbtx = db.NewSplitDB(s.readDB, s.db)
	}
	s.queries = db.New(dbtx)

	// Initialize tamper-evident audit writer.
	s.auditWriter = audit.NewWriter(s.queries)

	// --- License ---

	// Load and validate license.
	//
	// license.mode controls counting:
	//   "local" (default): count enrolled nodes on this server only.
	//   "aggregate": sum node counts across the whole spanner. Every
	//                peer-equal bolt enforces independently off the
	//                gossip-replicated roster — there is no designated
	//                enforcer (ADR-026 peer-equal model).
	s.licenseMode = s.cfg.License.Mode
	if s.licenseMode == "" {
		s.licenseMode = "local"
	}

	// envoyCounter closes over *Server. s.spannerAdmin and s.fleetIndex
	// stay nil through the license-enforcer assignment and are populated
	// later in NewServer (fleet-rehydrate + spanner-admin stages). The
	// enforcement loop runs on a 1-hour ticker, so the real values are
	// in place well before the first call.
	envoyCounter := func(ctx context.Context) (int, error) {
		// Aggregate mode: sum the whole spanner. AggregateEnvoysLocal
		// folds the local fleet index together with every gossiped peer
		// roster, so each bolt computes the same fleet-wide count.
		if s.licenseMode == "aggregate" && s.spannerAdmin != nil {
			envoys, _ := s.spannerAdmin.AggregateEnvoysLocal()
			total := 0
			for _, e := range envoys {
				if !e.Revoked {
					total++
				}
			}
			return total, nil
		}

		// Local mode (default): the in-memory FleetIndex is the source of
		// truth. It's seeded from the DB at startup via Rehydrate, and
		// every enrollment/revocation goes through the registry under its
		// serialization mutex which keeps the index in sync. Reading it
		// here is O(1) and can be called from the hot path without a
		// ListEnvoys scan.
		if s.fleetIndex == nil {
			return 0, nil // pre-rehydrate — gate runs later
		}
		return s.fleetIndex.ActiveCount(), nil
	}

	s.enforcer, err = license.NewEnforcer(s.licenseDir, s.licenseGraceDir, s.cfg.Server.TLS.CAFile, envoyCounter, func() { s.cancel() }, s.metrics)
	if err != nil {
		return nil, fmt.Errorf("license: %w", err)
	}

	// Spanner requires a paid license — reject spanner mode on free tier.
	if s.mode != config.ModeStandalone && s.enforcer.IsFreeLicense() {
		return nil, fmt.Errorf("spanner mode requires a paid license — the built-in free license does not support spanner")
	}

	// --- Auth ---

	// Mint (or load) the local-admin token before building the auth
	// provider — it gates server-host vigocli, replacing source-IP peer auth.
	s.localAdminToken, err = vigoauth.LoadOrCreateLocalAdminToken(filepath.Join(s.serverRoot, "cli-admin-token"))
	if err != nil {
		return nil, fmt.Errorf("initializing local-admin token: %w", err)
	}

	// Initialize auth provider.
	s.authProvider, err = createAuthProvider(ctx, s.cfg, s.db, s.queries, s.provider, s.localAdminToken, s.metrics)
	if err != nil {
		return nil, fmt.Errorf("initializing auth: %w", err)
	}
	s.authMethod = s.authProvider.Method()
	slog.Info("auth provider initialized", "method", s.authMethod)

	// Wire audit writer into auth provider for login/logout/failed-auth logging.
	switch p := s.authProvider.(type) {
	case *vigoauth.BasicProvider:
		p.SetAuditWriter(s.auditWriter)
	case *vigoauth.OIDCProvider:
		p.SetAuditWriter(s.auditWriter)
	case *vigoauth.IsowebauthProvider:
		p.SetAuditWriter(s.auditWriter)
	}

	// --- Registry + initial subsystems ---

	s.reg = registry.NewRegistry(s.queries)

	// publishState holds the per-process mutable freeze deadline.
	// Constructed once and threaded into ConfigHandler via NewGateway.
	s.publishState = &publish.State{}

	// Warn if .live directory has overly permissive permissions.
	if warning := publish.CheckLivePermissions(publish.DefaultLiveDir); warning != "" {
		slog.Warn(warning)
	}

	s.configStore, err = config.NewConfigStore(publish.DefaultLiveDir, s.provider, false)
	if err != nil {
		return nil, fmt.Errorf("config load failed at startup: %w — fix %s and restart (typically: vigocli config publish)", err, publish.DefaultLiveDir)
	}
	s.configStore.SetBackend(s.secretsCfg.Backend)
	s.configStore.SetServerYamlPath(configFile)
	s.configStore.SetEnabledStandards(s.cfg.Compliance.Standards)
	s.configStore.SetApprovedSources(s.cfg.Compliance.ApprovedSources)
	s.configStore.SetMetrics(s.metrics)

	// --- CheckIn tuning ---

	// Parse base interval for compliance stale threshold.
	s.baseInterval = 5 * time.Minute // default
	if s.cfg.CheckIn.Interval != "" {
		if parsed, err := time.ParseDuration(s.cfg.CheckIn.Interval); err == nil {
			s.baseInterval = parsed
		}
	}

	// Optional staleness threshold override. When zero, the per-envoy
	// threshold defaults to 2.5 × max(configured_interval, observed_cycle)
	// where observed_cycle is the agent's smoothed inter-CheckIn cadence.
	// When set, this literal duration takes effect across every
	// reachability surface (Offline pill, risk scorer, doctor API,
	// stale-envoy notifier, fleet rehydrator). Threaded as an explicit
	// parameter through every consumer rather than via a package global,
	// so multiple servers in one process (spanner harness) keep
	// independent thresholds.
	if s.cfg.CheckIn.OfflineThreshold != "" {
		if parsed, err := time.ParseDuration(s.cfg.CheckIn.OfflineThreshold); err == nil && parsed > 0 {
			s.offlineThreshold = parsed
		} else if err != nil {
			slog.Warn("invalid checkin.offline_threshold; falling back to per-envoy 2.5× cadence",
				"value", s.cfg.CheckIn.OfflineThreshold, "error", err)
		}
	}

	// TTL for resolved fleet: cross-node config lookups (default 30s).
	if s.cfg.CheckIn.FleetLookupTTL != "" {
		if parsed, err := time.ParseDuration(s.cfg.CheckIn.FleetLookupTTL); err == nil && parsed > 0 {
			s.configStore.SetFleetLookupTTL(parsed)
		} else if err != nil {
			slog.Warn("invalid checkin.fleet_lookup_ttl; using default 30s",
				"value", s.cfg.CheckIn.FleetLookupTTL, "error", err)
		}
	}

	// One-shot summary of the cadence + staleness wiring so future
	// "why does the envoys page disagree with reality?" investigations
	// are one journal grep away. Threshold is per-envoy at runtime
	// (2.5 × max(configured_interval, observed_cycle)); the value
	// logged here is the bootstrap floor when no agent has reported a
	// cadence yet.
	slog.Info("freshness wiring",
		"checkin.interval", s.baseInterval,
		"checkin.offline_threshold_override", s.cfg.CheckIn.OfflineThreshold,
		"freshness.bootstrap_threshold", freshness.StaleThresholdFromCycle(s.baseInterval, 0, s.offlineThreshold),
		"freshness.max_reported_cycle", freshness.MaxReportedCycle,
	)

	// --- Tuning durations (all optional; zero values use the listed defaults) ---
	s.sigWindow = parseDuration(s.cfg.Tuning.SignatureWindow, 0)
	s.lastSeenFlush = parseDuration(s.cfg.Tuning.LastSeenFlush, 10*time.Second)
	s.metricsInterval = parseDuration(s.cfg.Tuning.MetricsInterval, 30*time.Second)
	s.traitsScannerTTL = parseDuration(s.cfg.Tuning.TraitsScannerTTL, 0)
	s.maxConnAge = parseDuration(s.cfg.Tuning.MaxConnectionAge, 0)
	s.maxConnIdle = parseDuration(s.cfg.Tuning.MaxConnectionIdle, 0)
	s.kaTime = parseDuration(s.cfg.Tuning.KeepaliveTime, 0)
	s.kaTimeout = parseDuration(s.cfg.Tuning.KeepaliveTimeout, 0)

	// --- Run-store backend ---
	switch s.cfg.Tuning.RunStore {
	case "memory":
		s.runStore = db.NewMemRunStore(s.cfg.Tuning.RunStoreCapacity)
		slog.Info("run store backend: memory", "capacity", s.cfg.Tuning.RunStoreCapacity)
	default:
		s.runStore = db.NewDBRunStore(s.queries, s.db)
		slog.Info("run store backend: database")
	}

	// --- Integrations dispatcher (native integrations only — no SMTP, no generic webhooks) ---
	s.integrations = integrations.New(s.cfg.Integrations)
	if s.integrations.Enabled() {
		slog.Info("integrations dispatcher enabled", "targets", s.integrations.Count())
	}

	// Wire config reload failure events into the integrations dispatcher.
	s.configStore.SetOnReloadFailure(func(err error) {
		s.integrations.Dispatch(integrations.Event{
			Name:      "config.reload.failure",
			Timestamp: time.Now().UTC(),
			Summary:   "config reload failed: " + err.Error(),
			Details:   map[string]any{"error": err.Error()},
		})
	})

	// Wire audit-trail rows into the integrations dispatcher so every
	// hash-chained entry also reaches Loki / Splunk / Datadog / Sentinel /
	// etc. as an `audit.<eventType>` event. The audit DB remains the
	// source of truth; this is a fan-out for external observability.
	s.auditWriter.OnRecord = func(eventType, actor, source, summary, payload string) {
		s.integrations.Dispatch(integrations.Event{
			Name:      "audit." + eventType,
			Timestamp: time.Now().UTC(),
			Summary:   summary,
			Details: map[string]any{
				"actor":   actor,
				"source":  source,
				"payload": payload,
			},
		})
	}

	// --- Defense-in-depth hardening detection ---
	// Runs once at startup, after the audit writer + integrations
	// dispatcher are wired so findings fan out to Loki / Splunk /
	// Sentinel / etc. uniformly with every other audit event. Timing
	// is observational, not preventive: tracers attach at process
	// spawn, so a check at boot+200ms sees the same state as a check
	// at boot+0ms. VerifyBinarySignature stays in main.go as the only
	// fail-closed gate; these are warn-only forensic checks.
	s.emitStartupHardeningFindings(ctx)

	// --- Task definitions ---
	s.taskStore = task.NewStore()
	if err := s.taskStore.Load(s.taskDir); err != nil {
		return nil, fmt.Errorf("loading task definitions: %w", err)
	}
	slog.Info("task definitions loaded", "count", len(s.taskStore.List()), "dir", s.taskDir)

	// --- Fleet index: create + rehydrate. Populates s.fleetIndex so the
	// license enforcer's envoyCounter closure (constructed above) reads
	// the seeded index instead of the nil it saw at first assignment. ---
	s.fleetIndex = fleet.NewFleetIndex()
	if err := fleet.Rehydrate(ctx, s.fleetIndex, s.queries, s.runStore, s.baseInterval, s.offlineThreshold, s.configStore.ApprovedSources()); err != nil {
		return nil, fmt.Errorf("rehydrating fleet index: %w", err)
	}

	// Wire the enrollment gate + fleet index into the registry. These
	// collaborators make registry.Register's license check atomic with
	// the DB insert and the FleetIndex update, closing the TOCTOU race
	// and the bootstrap/admin bypass paths.
	s.reg.SetFleetIndex(s.fleetIndex)
	s.reg.SetEnrollmentGate(s.enforcer)

	// Sync envoy tags from hostcrate. Tags are derived from the hostcrate
	// (single source of truth) and kept only in FleetIndex; syncing at
	// startup ensures targeting works before any envoy has checked in.
	if s.configStore != nil {
		for _, ts := range s.fleetIndex.AllEnvoys() {
			if _, cfg := s.configStore.Match(ts.Hostname); cfg != nil {
				s.fleetIndex.SetTags(ts.ID, cfg.Tags)
			}
		}
	}

	// --- Async run batcher for delta events. Started by Run(). ---
	s.runBatcher = fleet.NewRunBatcher(s.runStore)

	// --- Bundle signer for compiled promises (ephemeral key per process start). ---
	s.bundleSigner, s.bundleMaxAge = initBundleSigner(s.cfg)

	// --- Per-arch agent binary versions for auto-update comparison.
	// Resolution order per arch: sidecar `<binary>.version` file →
	// server-arch runtime probe. Cross-arch binaries without sidecars
	// yield empty entries, which disable auto-update for that arch
	// (safer than guessing the wrong version and causing cross-arch
	// update loops — see tshoot/log.md). ---
	s.agentVersions = bootstrap.DetectAgentVersions(s.agentDir)
	s.agentVersion = s.agentVersions[runtime.GOOS+"/"+runtime.GOARCH]
	if s.agentVersion == "" {
		s.agentVersion = version // fallback to server version for UI display only
	}
	slog.Info("agent version for auto-update (server arch)", "agent_version", s.agentVersion)

	// --- Fallback server addresses from peer config (returned to agents on registration). ---
	for _, pe := range s.cfg.Peers.Servers {
		s.fallbackServers = append(s.fallbackServers, pe.Addr)
	}

	// --- Peer replication (when peers section is configured). The gRPC
	// registration + the primary-side push goroutine still live in
	// run() because both depend on objects that come together in
	// chunk 4 (grpcSrv + the lifecycle context). NewService and the
	// shared peerTLS land here so the construction has a single home. ---
	if len(s.cfg.Peers.Servers) > 0 {
		peerRole := s.cfg.Peers.Role
		if peerRole == "" {
			peerRole = "primary"
		}

		s.peerService = peer.NewService(s.serverRoot, peerRole, func() {
			if err := s.configStore.Reload(); err != nil {
				slog.Error("peer-triggered config reload failed", "error", err)
			}
		})
		s.peerService.SetAuditWriter(s.auditWriter)

		peerTLS, peerTLSErr := buildPeerTLS(s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile, s.cfg.Server.TLS.CAFile)
		if peerTLSErr != nil {
			return nil, fmt.Errorf("building peer TLS config: %w", peerTLSErr)
		}
		s.peerTLS = peerTLS

		var peerAddrs []string
		for _, entry := range s.cfg.Peers.Servers {
			peerAddrs = append(peerAddrs, entry.Addr)
		}
		s.peerService.SetPeers(peerAddrs, peerTLS)
	}

	// --- NVD CVE enrichment (read-side construction only; the
	// `go nvdEnricher.Run(ctx, ...)` goroutine still starts from run()). ---
	if s.cfg.Risk.NVD.Enabled {
		nvdCache := &nvdCacheAdapter{queries: s.queries}
		s.nvdEnricher = risk.NewNVDEnricher(s.cfg.Risk.NVD.APIKey, nvdCache)
	}

	// --- Workflow engine: construction is deferred to run() / chunk 4
	// because workflows.NewEngine takes the agentSvc TaskRunner as a
	// mandatory argument and agentSvc is produced by isgrpc.NewServer
	// (chunk 4 territory). Load + RecoverInterrupted stay paired with
	// construction there. ---

	// --- Stockpile watcher (ADR-025 Slice 3): watches the ops-puddle's
	// stockpile gitback bare repo for ref changes and triggers
	// configStore.Reload(). Lookup is best-effort — if the stockpile
	// project has not been founded yet (vigocli spanner stockpile init)
	// the watcher stays nil and a future SIGHUP / restart picks it up.
	// Spanner-mode only; standalone keeps the local-write + SIGHUP shape. ---
	if s.mode != config.ModeStandalone {
		if pid, ok := lookupStockpileProjectID(puddle.OpsDefaultDir); ok {
			s.stockpileWatcher = stockpilewatch.New(
				"/var/lib/vigo/swarm/gitback/projects",
				pid,
				publish.DefaultLiveDir,
				s.configStore,
			)
		} else {
			slog.Debug("stockpile project not founded yet; watcher skipped (run `vigocli spanner stockpile init` to bootstrap)")
		}
	}

	// --- Swarm substrate: peer-to-peer content distribution. The substrate
	// runs on this server iff the server-as-envoy hostname matches the
	// swarm pattern list — the canonical assumption is the server hosts a
	// local agent under cfg.Server.Hostname (CLAUDE.md). An empty pattern
	// list disables the substrate entirely. Every spanner bolt is a
	// full vigosrv, so the substrate runs in standalone and spanner mode
	// alike. Only the bare substrate (peerTracker, manifestStore, curator
	// aggregator) lands here; the API handlers that wire up to gwMux + the
	// `go runCuratorFeed` reactor stay in run() and chunk 4.
	// ManifestStore.Stop() is called from (*Server).Close(). ---
	if config.HostMatchesPatterns(s.cfg.Server.Hostname, s.cfg.Swarm.Enabled) {
		seedDir := s.cfg.Swarm.SeedDir
		if seedDir == "" {
			seedDir = "/var/lib/vigo/swarm/seed"
		}
		if err := os.MkdirAll(seedDir, 0755); err != nil {
			return nil, fmt.Errorf("creating swarm seed dir: %w", err)
		}
		s.swarmSeedDir = seedDir

		staleThreshold := s.baseInterval * 2
		if staleThreshold < 60*time.Second {
			staleThreshold = 60 * time.Second
		}
		s.peerTracker = swarm.NewPeerTracker(staleThreshold)
		s.peerTracker.SetActiveChecker(s.fleetIndex.IsActive)

		// Manifest persistence sits next to seed/, so a server restart
		// preserves intent (revocations + the active set agents are
		// allowed to refresh via gossip). Without this, an empty
		// post-restart manifest gets re-poisoned by stale `swarm_seeds`
		// traits within seconds (root cause of the 2026-04-24
		// incident). Filename is `server-manifest.json` to stay clear
		// of the local agent's own `manifest.json` at the same path —
		// the two share the dir via the swarm bind-mount but use
		// independent on-disk schemas.
		manifestPath := filepath.Join(filepath.Dir(seedDir), "server-manifest.json")
		manifestStore, err := swarm.NewPersistentManifestStore(manifestPath, time.Second)
		if err != nil {
			return nil, fmt.Errorf("loading swarm manifest: %w", err)
		}
		s.manifestStore = manifestStore
		slog.Info("swarm manifest persistence enabled", "path", manifestPath)

		// Curator (ADR-024): read-side aggregator. The feed reactor +
		// handler-route wiring still live in run() / chunk 4.
		s.curatorAgg = curatormesh.NewAggregator(s.queries)
		// Poolq (ADR-029): read-side aggregator. Same shape as curatorAgg;
		// retention comes from server.yaml `swarm.poolq.retention` and is
		// hot-swappable via `SetRetention` on a config reload. A zero or
		// unparseable retention string falls back to `poolqmesh.DefaultRetention`
		// (7 days, per ADR-029).
		s.poolqAgg = poolqmesh.NewAggregator(s.queries, parsePoolqRetention(s.cfg.Swarm.Poolq.Retention))
		s.swarmEnabled = true
	}

	// --- gRPC server + services (chunk 4). All goroutine starts
	// (grpcSrv.Serve, traitsBatch.Start, peer primary push, etc.) stay
	// in run() / chunk 5. ---
	grpcSrv, certCache, traitsBatch, agentSvc, adminSvc, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:                   s.cfg.Server.TLS.CertFile,
		KeyFile:                    s.cfg.Server.TLS.KeyFile,
		CAFile:                     s.cfg.Server.TLS.CAFile,
		GRPCPort:                   s.cfg.Server.GRPCListen,
		Registry:                   s.reg,
		ConfigStore:                s.configStore,
		Queries:                    s.queries,
		RunStore:                   s.runStore,
		Interval:                   s.baseInterval,
		OfflineThreshold:           s.offlineThreshold,
		JitterPercent:              int32(s.cfg.CheckIn.JitterPercent),
		MaxConcurrentCheckIns:      s.cfg.CheckIn.MaxConcurrent,
		MaxConcurrentReportResults: s.cfg.CheckIn.ReportMaxConcurrent,
		MaxConnections:             s.cfg.CheckIn.MaxConnections,
		Integrations:               s.integrations,
		TraitsDir:                  s.traitsDir,
		SignatureWindow:            s.sigWindow,
		TraitsScannerTTL:           s.traitsScannerTTL,
		MaxConcurrentStreams:       s.cfg.Tuning.MaxConcurrentStreams,
		GRPCReadBuffer:             s.cfg.Tuning.GRPCReadBuffer,
		GRPCWriteBuffer:            s.cfg.Tuning.GRPCWriteBuffer,
		MaxConnectionAge:           s.maxConnAge,
		MaxConnectionIdle:          s.maxConnIdle,
		KeepaliveTime:              s.kaTime,
		KeepaliveTimeout:           s.kaTimeout,
		StreamSendBuffer:           s.cfg.Tuning.StreamSendBuffer,
		LicenseEnforcer:            s.enforcer,
		TaskStore:                  s.taskStore,
		FleetIndex:                 s.fleetIndex,
		RunBatcher:                 s.runBatcher,
		BundleSigner:               s.bundleSigner,
		BundleMaxAge:               s.bundleMaxAge,
		FallbackServers:            s.fallbackServers,
		AuditWriter:                s.auditWriter,
		ServerVersions:             s.agentVersions,
		GlobalObserveOnly:          s.cfg.CheckIn.ObserveOnly,
		Metrics:                    s.metrics,
		TicketsStore:               s.ticketsStore,
		AgentBinAvailable: func() bool {
			for _, goos := range []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "illumos", "windows"} {
				for _, goarch := range []string{"amd64", "arm64"} {
					if _, err := os.Stat(bootstrap.BinaryPath(s.agentDir, goos, goarch)); err == nil {
						return true
					}
				}
			}
			return false
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating gRPC server: %w", err)
	}
	s.grpcSrv = grpcSrv
	s.certCache = certCache
	s.traitsBatch = traitsBatch
	s.agentSvc = agentSvc
	s.adminSvc = adminSvc

	// Peer replication: register the gRPC service if a peerService was
	// constructed earlier. Primary-side push goroutine stays in run().
	if s.peerService != nil {
		peerRole := s.cfg.Peers.Role
		if peerRole == "" {
			peerRole = "primary"
		}
		pb.RegisterVigoPeerServer(s.grpcSrv, s.peerService)
		slog.Info("peer replication enabled", "role", peerRole, "peers", len(s.cfg.Peers.Servers))
	}

	// --- Workflow engine. Load + RecoverInterrupted are one-shot sync
	// calls so they fold into NewServer; the engine is wired into
	// agentSvc so auto-triggered workflows fire on trait changes. ---
	s.workflowEngine = workflows.NewEngine(s.queries, s.agentSvc, s.metrics)
	if err := s.workflowEngine.Load(s.workflowsDir); err != nil {
		return nil, fmt.Errorf("loading workflows: %w", err)
	}
	slog.Info("workflows loaded", "count", len(s.workflowEngine.List()), "dir", s.workflowsDir)

	// Mark interrupted workflows as failed on startup.
	if err := workflows.RecoverInterrupted(ctx, s.queries); err != nil {
		slog.Warn("failed to recover interrupted workflows", "error", err)
	}

	// Wire workflow engine for auto-triggered workflows on trait changes.
	s.agentSvc.SetWorkflowTrigger(s.workflowEngine)

	// Wire the cross-envoy lockbox cleanup callback fired by
	// AdminService.RevokeEnvoy / DeleteEnvoy. Dispatches one
	// `_apply-evict --hostname <h> --all-users` task to every envoy.
	s.adminSvc.SetSwarmCleanup(func(ctx context.Context, hostname string) error {
		cmd := fmt.Sprintf("vigo swarm lockbox _apply-evict --hostname %s --all-users", hostname)
		_, err := s.agentSvc.DispatchPush(ctx, isgrpc.DispatchParams{
			Command:     cmd,
			TargetSpec:  "*",
			Operator:    "envoy.retire",
			TimeoutSecs: 60,
		})
		return err
	})

	// Wire stream_edit guardrails.
	s.agentSvc.SetStreamEditConfig(s.cfg.StreamEdit)
	if !s.cfg.StreamEdit.StreamEditEnabled() {
		slog.Warn("stream_edit globally disabled — all stream_edit attributes will be stripped from policy bundles")
	}

	// --- Scrier remote access (optional — disabled by default). ---
	if s.cfg.Scrier.Enabled {
		tm := isgrpc.NewTunnelManager(
			s.cfg.Scrier.ScrierGuacdAddr(),
			s.cfg.Scrier.ScrierProxyHostname(),
			s.cfg.Scrier.ScrierAllowedPorts(),
			s.cfg.Scrier.ScrierMaxSessions(),
			s.agentSvc.StreamMgr(),
			s.fleetIndex,
		)
		s.agentSvc.SetTunnelManager(tm)
		s.scrierHandler = vigoscrier.NewHandler(tm, s.fleetIndex, s.queries, version, s.baseInterval, s.offlineThreshold)
		s.scrierHandler.SetAuditWriter(s.auditWriter)
		slog.Info("scrier remote access enabled",
			"guacd", s.cfg.Scrier.ScrierGuacdAddr(),
			"max_sessions", s.cfg.Scrier.ScrierMaxSessions(),
			"allowed_ports", s.cfg.Scrier.ScrierAllowedPorts(),
		)
	}

	// --- AI assistant (optional — disabled by default). The Ollama
	// pre-warm goroutine stays in run() / chunk 5. ---
	if s.cfg.AI.Enabled {
		if err := vigoai.ValidateConfig(s.cfg.AI); err != nil {
			return nil, fmt.Errorf("ai config: %w", err)
		}
		aiProvider, aiErr := createAIProvider(ctx, s.cfg.AI)
		if aiErr != nil {
			slog.Warn("AI assistant disabled: provider init failed", "error", aiErr)
		} else {
			s.aiProvider = aiProvider
			s.assistant = vigoai.NewAssistant(aiProvider, s.queries, s.configStore, s.runStore, func(envoyID string) time.Duration {
				if s.fleetIndex == nil {
					return 0
				}
				return s.fleetIndex.EnvoyCycle(envoyID)
			}, s.baseInterval, s.offlineThreshold, s.cfg.AI, s.docsDir, s.aiMetrics)
			if s.nvdEnricher != nil {
				s.assistant.SetCVELookup(&cveLookupAdapter{enricher: s.nvdEnricher})
			}
			s.aiHandler = api.NewAIHandler(s.assistant)
			slog.Info("AI assistant enabled", "provider", aiProvider.Name(), "model", s.cfg.AI.Model)
		}
	}

	// --- REST handlers (shared across standalone and spanner modes). ---
	licenseHandler := api.NewLicenseHandler(s.enforcer)
	doctorHandler := api.NewDoctorHandler(api.DoctorConfig{
		SQLDB:            s.db,
		CertFile:         s.cfg.Server.TLS.CertFile,
		KeyFile:          s.cfg.Server.TLS.KeyFile,
		Enforcer:         s.enforcer,
		ConfigStore:      s.configStore,
		Queries:          s.queries,
		Interval:         s.baseInterval,
		OfflineThreshold: s.offlineThreshold,
		Integrations:     s.integrations.Enabled(),
		Mode:             s.mode,
		SecretsBackend:   s.secretsCfg.Backend,
		CycleLookup: func(envoyID string) time.Duration {
			if s.fleetIndex == nil {
				return 0
			}
			return s.fleetIndex.EnvoyCycle(envoyID)
		},
	})
	// Seed the doctor handler's swarm-config view so the
	// swarm_pattern_order check has data on the first /doctor call.
	doctorHandler.SetSwarmConfig(s.cfg.Swarm)

	spannerHandler := api.NewSpannerHandler(s.mode, s.cfg.Spanner.SpannerID, formatLocalBoltPubkey(s.boltID))
	spannerHandler.SetAuditWriter(s.auditWriter)
	opsPuddleHandler := api.NewSpannerOpsPuddleHandler(s.provider, puddle.OpsDefaultDir, s.auditWriter)
	// Spanner Slice 2A.2 — membership ceremony (ADR-026). Founds a
	// spanner, mints join invites, admits joining bolts. Needs the
	// local bolt identity to sign admission rows + invite bundles.
	spannerAdmissionHandler := api.NewSpannerAdmissionHandler(s.db, s.queries, s.boltID, s.cfg.Spanner.SpannerID, s.cfg.Server.Hostname, s.cfg.Spanner.Patterns, s.auditWriter, nil)
	// Slice 2-ii-a: the in-memory fleet index backs the signed
	// /fleet-snapshot endpoint a peer's gossiper pulls.
	spannerAdmissionHandler.SetFleetIndex(s.fleetIndex)

	// Spanner Slice 2-i-a + 2-i-b — roster-replication gossiper
	// (ADR-026). On every non-standalone bolt: Tier-2 pulls each peer's
	// /snapshot on the 5m timer and CRDT-merges it; Tier-1 (2-i-b)
	// multicasts a signed roster digest every 30s and fires a Tier-2
	// pull early on a digest mismatch. The `go spannerGossip.Run`
	// goroutine (all three sub-goroutines) starts from run().
	if s.mode != config.ModeStandalone {
		tier2 := parseDuration(s.cfg.Spanner.Snapshot.Tier2Interval, 5*time.Minute)
		tier1 := parseDuration(s.cfg.Spanner.Snapshot.Tier1Interval, 30*time.Second)
		mcastGroup := s.cfg.Spanner.Transport.MulticastGroup
		if mcastGroup == "" {
			mcastGroup = "224.0.0.45"
		}
		mcastPort := s.cfg.Spanner.Transport.MulticastPort
		if mcastPort == 0 {
			mcastPort = 1534
		}
		s.spannerGossip = spanner.NewGossiper(
			s.db,
			s.queries,
			fullBoltPubkeyHex(s.boltID),
			apiListenPort(s.cfg.Server.APIListen),
			tier2,
			s.cfg.Spanner.SpannerID,
			boltSigningKeyOrNil(s.boltID),
			mcastGroup,
			mcastPort,
			tier1,
			nil,
		)
		// Slice 2-ii-a: the local-counters source for outbound Tier-1
		// digests — peers get 30s-fresh convergence counters without
		// waiting for the 5m Tier-2 fleet-snapshot pull.
		if s.fleetIndex != nil {
			s.spannerGossip.SetLocalCounters(func() spanner.ConvergenceCounters {
				return api.FleetCountersToSpanner(s.fleetIndex.ConvergenceSummary())
			})
		}

		// Slice 2-ii-a: the peer-equal admin-read driver. Shares the
		// Gossiper's observability cache; the fanout path dials peers
		// through a PeerDialer. Attached to the AdminService so the
		// three fleet-wide reads route through it instead of the
		// pure-local path.
		peerDialer, err := spanner.NewPeerDialer(s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile, s.cfg.Server.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("building spanner peer dialer: %w", err)
		}
		s.spannerAdmin = spanner.NewSpannerAdmin(
			s.queries,
			s.spannerGossip.ObservabilityCache(),
			peerDialer,
			fullBoltPubkeyHex(s.boltID),
			s.cfg.Spanner.FallbackAdmin,
			grpcListenPort(s.cfg.Server.GRPCListen),
		)
		if s.fleetIndex != nil {
			s.spannerAdmin.SetLocalSources(
				func() spanner.ConvergenceCounters {
					return api.FleetCountersToSpanner(s.fleetIndex.ConvergenceSummary())
				},
				func() []spanner.EnvoyLite {
					return api.FleetEnvoysToLite(s.fleetIndex.AllEnvoys())
				},
			)
		}
		s.adminSvc.SetSpannerAdmin(s.spannerAdmin)
		s.agentSvc.SetSpannerAdmin(s.spannerAdmin)
		spannerHandler.SetSpannerAdmin(s.spannerAdmin)
	}

	taskHandler := api.NewTaskHandler(s.queries, s.agentSvc, s.cfg.Task.RequireDefinition)
	taskHandler.SetAuditWriter(s.auditWriter)
	queryHandler := api.NewQueryHandler(s.agentSvc)
	exportHandler := api.NewExportHandler(s.queries, s.fleetIndex, s.configStore, s.cfg.Export)
	s.reportHandler = api.NewReportHandler(s.queries, s.fleetIndex, s.configStore, version)
	s.reportHandler.SetAuditWriter(s.auditWriter)
	cveHandler := api.NewCVEHandler(s.fleetIndex)
	peerHandler := api.NewPeerHandler(s.peerService)

	// Auth-related API handlers.
	userStore := vigoauth.NewUserStore(s.queries)
	// ADR-030 Slice 2-iii-c: in spanner mode, wire the BoltSigner so
	// every user mutation populates origin_bolt + signature. Standalone
	// (boltID nil) leaves the signer unset — writes proceed unchanged
	// and the snapshot serve filters out unsigned rows. Wire BOTH the
	// REST-handler's userStore AND the auth provider's userStore — the
	// provider's instance handles OIDC autoprovisioning + similar
	// signer-relevant writes.
	if s.boltID != nil {
		signer := spanner.NewBoltSigner(s.boltID)
		userStore.SetSigner(signer)
		switch p := s.authProvider.(type) {
		case *vigoauth.BasicProvider:
			p.UserStore().SetSigner(signer)
		case *vigoauth.OIDCProvider:
			p.UserStore().SetSigner(signer)
		case *vigoauth.IsowebauthProvider:
			p.UserStore().SetSigner(signer)
		}
		// ADR-030 Slice 2-iii-f: re-sign legacy unsigned rows so they
		// become eligible for snapshot replication. Synchronous,
		// bounded (one UPDATE per legacy live row). updated_at_ms stays
		// 0 so concurrent multi-bolt re-signs resolve deterministically
		// via the lex-min-origin tiebreaker (matches ADR-030's promise).
		ctx := context.Background()
		if n, err := userStore.ResignLegacyRows(ctx); err != nil {
			slog.Warn("spanner: re-signing legacy user rows failed", "error", err)
		} else if n > 0 {
			slog.Info("spanner: re-signed legacy user rows", "count", n)
			if s.auditWriter != nil {
				if aErr := s.auditWriter.Record(ctx, "webusers.legacy_resigned",
					hex.EncodeToString(s.boltID.PublicKey), "spanner_startup",
					fmt.Sprintf("re-signed %d legacy user row(s) at startup", n), ""); aErr != nil {
					slog.Warn("spanner: audit record for legacy_resigned failed", "error", aErr)
				}
			}
		}
	}
	// ADR-030 Slice 2-iii-f: doctor handler needs the userStore for
	// its webusers_replication check. Wired late because doctor is
	// constructed before userStore (which depends on the auth provider).
	doctorHandler.SetUserStore(userStore)
	tokenStore := vigoauth.NewTokenStore(s.queries, s.localAdminToken, s.metrics)
	usersHandler := api.NewUsersHandler(userStore, s.authMethod)
	usersHandler.SetAuditWriter(s.auditWriter)
	usersHandler.SetSecretsProvider(s.provider)
	usersHandler.SetAdminValidation(s.queries, s.cfg.Server.Hostname)
	// Wire the session manager so role changes and user deletes
	// destroy the affected user's cached sessions immediately rather
	// than waiting on the idle-timeout window.
	var sessionMgr *vigoauth.SessionManager
	switch p := s.authProvider.(type) {
	case *vigoauth.BasicProvider:
		sessionMgr = p.Sessions()
	case *vigoauth.OIDCProvider:
		sessionMgr = p.Sessions()
	case *vigoauth.IsowebauthProvider:
		sessionMgr = p.Sessions()
	}
	if sessionMgr != nil {
		usersHandler.SetSessions(sessionMgr)
	}
	// ADR-030 Slice 2-iii-d: the gossiper's webusers merge fires
	// session-destroy + audit on accepted role-change/tombstone effects.
	// Wired post-NewGossiper because the session manager only exists
	// after the auth provider is built. nil-safe on either side
	// (standalone has no gossiper; tests may have no audit writer).
	if s.spannerGossip != nil {
		if sessionMgr != nil {
			s.spannerGossip.SetSessions(sessionMgr)
		}
		s.spannerGossip.SetAuditWriter(s.auditWriter)
	}
	tokensHandler := api.NewTokensHandler(tokenStore, userStore, s.authMethod)
	tokensHandler.SetAuditWriter(s.auditWriter)
	// Curator S3 API (ADR-027) credential management. The store is wired
	// below only under the `local` secrets backend (SetStore); the routes
	// 503 otherwise.
	s3CredentialsHandler := api.NewS3CredentialsHandler(nil)
	s3CredentialsHandler.SetAuditWriter(s.auditWriter)

	// Admin-user posture check at startup. The bootstrap `admin` row is no
	// longer auto-seeded — admin webusers are explicitly created via
	// `vigocli webusers create <yourname> --role admin` and must map 1:1
	// with a human OS user on the server host.
	if users, err := userStore.List(ctx); err == nil {
		adminCount := 0
		hasLegacyAdmin := false
		for _, u := range users {
			if u.Role == "admin" {
				adminCount++
			}
			if u.Username == "admin" {
				hasLegacyAdmin = true
			}
		}
		if adminCount == 0 {
			slog.Warn("no admin web users — create one with `vigocli webusers create <yourname> --role admin` from the server host (requires a matching human OS user)")
		}
		if hasLegacyAdmin {
			slog.Warn("legacy `admin` web user present from pre-0.53.0 install — create a per-operator admin (vigocli webusers create <yourname> --role admin) and delete this row when ready (vigocli webusers delete admin)")
		}
	}

	workflowHandler := api.NewWorkflowHandler(s.queries, s.workflowEngine)
	workflowHandler.SetAuditWriter(s.auditWriter)

	// --- GRC evidence pusher (nil unless GRC integrations configured).
	// The `.Start(ctx)` goroutine stays in run() / chunk 5. ---
	if len(s.cfg.GRC.Integrations) > 0 {
		s.grcPusher = grc.NewPusher(s.cfg.GRC.Integrations, s.fleetIndex, s.configStore)
	}

	// --- REST gateway construction. Every vigosrv — standalone or a
	// peer-equal spanner bolt — serves the full gateway: UI + bootstrap
	// + swarm + the admin API. ---
	if s.isPostgres {
		dbDriver = "postgres"
	} else {
		dbDriver = "sqlite"
	}
	{
		// Load CA for bootstrap CSR signing (optional — only if ca_key_file is configured).
		var gwOpts []api.GatewayOption
		if s.cfg.Server.TLS.CAKeyFile != "" {
			certAuthority, caErr := ca.Load(s.cfg.Server.TLS.CAFile, s.cfg.Server.TLS.CAKeyFile)
			if caErr != nil {
				return nil, fmt.Errorf("loading CA for bootstrap: %w", caErr)
			}
			slog.Info("bootstrap CA loaded, CSR signing enabled")

			certValidity := 365 * 24 * time.Hour
			if s.cfg.Bootstrap.CertValidity != "" {
				if parsed, pErr := time.ParseDuration(s.cfg.Bootstrap.CertValidity); pErr == nil {
					certValidity = parsed
				}
			}

			gwOpts = append(gwOpts, func(bh *bootstrap.Handler) {
				bh.SetCA(certAuthority)
				bh.SetRegistry(s.reg)
				bh.SetTrustedPatterns(s.cfg.Bootstrap.TrustedPatterns)
				bh.SetCertValidity(certValidity)
				bh.SetIntegrations(s.integrations)
				bh.SetFleetIndex(s.fleetIndex)
				bh.SetLicenseEnforcer(s.enforcer)
			})

			if len(s.cfg.Bootstrap.TrustedPatterns) > 0 {
				for _, tp := range s.cfg.Bootstrap.TrustedPatterns {
					slog.Info("trusted enrollment pattern", "pattern", tp.Pattern, "cidrs", tp.CIDRs)
				}
			}
		}

		// Compliance docs store: operator-uploaded artifacts for
		// documented-scope controls. Only LocalProvider exposes the
		// Encrypt/Decrypt envelope today; under isopass or any other
		// backend the store stays nil and the REST routes return 503.
		if lp, ok := s.provider.(*secrets.LocalProvider); ok {
			docsCfg := s.cfg.Compliance.Docs
			cdStore, cdErr := compliancedocs.New(
				docsCfg.ComplianceDocsDir(),
				s.queries,
				lp,
				compliancedocs.Caps{
					MaxDocBytes:          docsCfg.ComplianceMaxDocBytes(),
					MaxPerFrameworkBytes: docsCfg.ComplianceMaxPerFrameworkBytes(),
					MaxTotalBytes:        docsCfg.ComplianceMaxTotalBytes(),
				},
				docsCfg.ComplianceMimeAllowlist(),
			)
			if cdErr != nil {
				slog.Warn("compliance docs store unavailable", "error", cdErr)
			} else {
				s.complianceDocsStore = cdStore
				s.reportHandler.SetDocsResolver(cdStore)
				slog.Info("compliance docs store ready",
					"dir", docsCfg.ComplianceDocsDir(),
					"max_doc_bytes", docsCfg.ComplianceMaxDocBytes())
			}

			// Curator S3 API SigV4 credential store (ADR-027) — derive-don't-
			// store, rooted in the LocalProvider master key. local-backend-only.
			s3CredentialsHandler.SetStore(curators3.NewCredStore(s.queries, lp))
			slog.Info("curator S3 credential store ready")
		}

		gwMux, uiHandler, configHandler, gwErr := api.NewGateway(ctx, s.cfg.Server.GRPCListen, s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile, s.cfg.Server.TLS.CAFile, s.queries, s.configStore, s.baseInterval, s.offlineThreshold, s.cfg.Server.APIListen, s.agentDir, version, s.docsDir, s.assistant, s.spannerAdmin, s.runStore, s.fleetIndex, s.authMethod, s.enforcer, s.cfg.Publish, s.cfg.Branding, s.peerService, s.auditWriter, s.complianceDocsStore, s.publishState, s.promReg, s.metrics, gwOpts...)
		if gwErr != nil {
			return nil, fmt.Errorf("creating REST gateway: %w", gwErr)
		}
		s.gwMux = gwMux
		s.uiHandler = uiHandler

		// Wire the compliance snapshotter so successful config publishes
		// refresh compliance_history alongside the daily background tick —
		// the dashboard standards-summary reads that table.
		configHandler.SetComplianceSnapshotter(s.reportHandler.PersistComplianceSnapshot)

		// Wire branding, hostname, observe-only, and CVE searcher into UI handler.
		uiHandler.SetAgentVersion(s.agentVersion)
		uiHandler.SetBranding(s.cfg.Branding)
		uiHandler.SetGrafana(s.cfg.Grafana)
		uiHandler.SetTraitQueryDispatcher(&traitQueryAdapter{agentSvc: s.agentSvc})
		uiHandler.SetServerHostname(s.cfg.Server.Hostname)
		uiHandler.SetServerConfigPath(s.configFile)
		uiHandler.SetGlobalObserveOnly(s.cfg.CheckIn.ObserveOnly)
		uiHandler.SetCVESearcher(&cveSearchAdapter{cveHandler})
		uiHandler.SetHealthChecker(&healthCheckAdapter{doctorHandler})

		// Wire report builder into UI handler for HTMX fragment rendering.
		uiHandler.SetReportBuilder(&ui.ReportBuilder{
			Executive: func(r *http.Request) (any, error) {
				return s.reportHandler.BuildExecutiveReport(r)
			},
			Framework: func(r *http.Request, slug string) (any, error) {
				return s.reportHandler.BuildFrameworkReport(r, slug)
			},
			EnvoyFrameworkStatus: s.reportHandler.ComputeEnvoyFrameworkStatus,
			EnvoyFrameworkDetail: s.reportHandler.ComputeEnvoyFrameworkDetail,
			AuditStatus: func(r *http.Request) ui.AuditChainStatus {
				st := s.reportHandler.AuditStatus(r.Context())
				return ui.AuditChainStatus{
					Valid:       st.Valid,
					EntryCount:  st.EntryCount,
					FirstBroken: st.FirstBroken,
					Error:       st.Error,
				}
			},
		})
		uiHandler.SetIntegrationsConfig(s.cfg.Integrations, s.cfg.GRC)
		uiHandler.SetTraitLookup(&traitLookupAdapter{fleetIndex: s.fleetIndex})

		licenseHandler.RegisterRoutes(gwMux)
		doctorHandler.RegisterRoutes(gwMux)
		spannerHandler.RegisterRoutes(gwMux)
		spannerAdmissionHandler.RegisterRoutes(gwMux)
		opsPuddleHandler.RegisterRoutes(gwMux)
		peerHandler.RegisterRoutes(gwMux)
		taskHandler.RegisterRoutes(gwMux)
		queryHandler.RegisterRoutes(gwMux)
		workflowHandler.RegisterRoutes(gwMux)
		exportHandler.RegisterRoutes(gwMux)
		s.reportHandler.RegisterRoutes(gwMux)
		cveHandler.RegisterRoutes(gwMux)
		usersHandler.RegisterRoutes(gwMux)
		tokensHandler.RegisterRoutes(gwMux)
		s3CredentialsHandler.RegisterRoutes(gwMux)
		// Fleet pubkey distribution. Lives outside the swarm-enabled gate
		// so agents can bootstrap their gitback peer-pubkey TOFU cache
		// regardless of whether THIS server has the swarm substrate
		// enabled — the endpoint just reads FleetIndex, which is always
		// populated.
		api.NewFleetPubkeysHandler(s.fleetIndex).RegisterRoutes(gwMux)

		// Swarm: peer-to-peer content distribution. The substrate runs on
		// this server iff the server-as-envoy hostname matches the swarm
		// pattern list. Handler wiring lives here; the curator feed
		// reactor goroutine + the swarm retention pruner goroutine stay
		// in run() / chunk 5.
		if s.swarmEnabled {
			seedDir := s.swarmSeedDir
			peerTracker := s.peerTracker
			manifestStore := s.manifestStore
			lodeHandler := api.NewLodeHandler(seedDir, peerTracker, s.fleetIndex, manifestStore, s.queries)
			lodeHandler.SetForcePusher(s.agentSvc)
			lodeHandler.SetSwarmConfig(s.cfg.Swarm)
			lodeHandler.SetAuditWriter(s.auditWriter)
			lodeHandler.RegisterRoutes(gwMux)
			swarmAPIHandler := api.NewSwarmHandler(peerTracker, manifestStore, s.fleetIndex)
			swarmAPIHandler.SetCleanupDispatcher(s.agentSvc)
			swarmAPIHandler.SetAuditWriter(s.auditWriter)
			swarmAPIHandler.RegisterRoutes(gwMux)
			uiHandler.SetSwarmAPI(swarmAPIHandler)
			s.agentSvc.SetSwarm(peerTracker, manifestStore)
			s.agentSvc.SetSwarmConfig(s.cfg.Swarm)
			uiHandler.SetSwarm(peerTracker, &swarmHostnameAdapter{fleetIndex: s.fleetIndex}, s.cfg.Swarm)
			slog.Info("swarm enabled", "seed_dir", seedDir, "port", s.cfg.Swarm.Port)

			// Curator (ADR-024): the read side. REST handler serves
			// resolve/blob (public — artifacts are fleet-readable; the
			// publish side is what's gated), fleet.read operator views,
			// and admin block/unblock moderation backstop. Curator feed
			// reactor goroutine stays in run() / chunk 5.
			blobsDir := filepath.Join(filepath.Dir(seedDir), ".blobs")
			curatorHandler := api.NewCuratorHandler(s.curatorAgg, s.queries, s.fleetIndex, blobsDir)
			curatorHandler.SetAuditWriter(s.auditWriter)
			curatorHandler.RegisterRoutes(gwMux)
			uiHandler.SetCuratorFleet(curatorHandler)

			// Poolq (ADR-029) — read-only fleet log endpoints. The agent +
			// vigocli + admin UI all consume the same routes; per-topic
			// block/unblock admin moderation lands in Slice 5.
			poolqHandler := api.NewPoolqHandler(s.poolqAgg, s.queries, s.fleetIndex)
			poolqHandler.SetAuditWriter(s.auditWriter)
			poolqHandler.RegisterRoutes(gwMux)
			uiHandler.SetPoolqFleet(poolqHandler)

			// Curator S3 API (ADR-027): an opt-in HTTPS listener exposing
			// curator artifacts to non-envoy S3 clients. Requires an
			// `s3_port` and the `local` secrets backend (the SigV4 secret
			// derivation roots in the master key) — off, with a clear log
			// line, otherwise.
			if port := s.cfg.Swarm.Curator.S3Port; port != 0 {
				if lp, ok := s.provider.(*secrets.LocalProvider); ok {
					s3Server := curators3.NewServer(
						curators3.NewVerifier(curators3.NewCredStore(s.queries, lp)),
						curatorHandler,
						gitbackmesh.NewAggregator(s.queries),
						s.fleetIndex,
						blobsDir,
						s.metrics,
					)
					s.s3Srv = &http.Server{
						Addr:    fmt.Sprintf(":%d", port),
						Handler: s3Server.Handler(),
					}
					slog.Info("curator S3 API enabled", "port", port)
				} else {
					slog.Warn("curator S3 API configured but disabled — requires the `local` secrets backend", "port", port)
				}
			}

			// Track pattern lists across reloads so we can audit operator
			// edits to each subsystem's authorization. Patterns are what
			// the operator actually edited in YAML.
			lastSwarmPatterns := append([]string(nil), s.cfg.Swarm.Enabled...)
			lastPuddlePatterns := append([]string(nil), s.cfg.Swarm.Puddle.Enabled...)
			lastGitbackPatterns := append([]string(nil), s.cfg.Swarm.Gitback.Enabled...)
			lastLongdrawerPatterns := append([]string(nil), s.cfg.Swarm.Longdrawer.Enabled...)
			lastFilecastPatterns := append([]string(nil), s.cfg.Swarm.Filecast.Enabled...)

			s.configStore.SetOnReloadSuccess(func() {
				// Re-read server.yaml directly — ConfigStore.Reload() only
				// refreshes worksite YAML, not server.yaml.
				newCfg, err := config.LoadServerConfig(s.configFile, s.provider, s.secretsCfg.Backend)
				if err != nil {
					slog.Warn("config reload: failed to re-read server.yaml for swarm section", "error", err)
					return
				}
				s.agentSvc.SetSwarmConfig(newCfg.Swarm)
				uiHandler.SetSwarm(peerTracker, &swarmHostnameAdapter{fleetIndex: s.fleetIndex}, newCfg.Swarm)
				lodeHandler.SetSwarmConfig(newCfg.Swarm)
				doctorHandler.SetSwarmConfig(newCfg.Swarm)

				if !sameStringSet(lastSwarmPatterns, newCfg.Swarm.Enabled) {
					recordSwarmPatternsChange(ctx, s.auditWriter, "swarm", lastSwarmPatterns, newCfg.Swarm.Enabled)
					lastSwarmPatterns = append([]string(nil), newCfg.Swarm.Enabled...)
				}
				if !sameStringSet(lastPuddlePatterns, newCfg.Swarm.Puddle.Enabled) {
					recordSwarmPatternsChange(ctx, s.auditWriter, "puddle", lastPuddlePatterns, newCfg.Swarm.Puddle.Enabled)
					lastPuddlePatterns = append([]string(nil), newCfg.Swarm.Puddle.Enabled...)
				}
				if !sameStringSet(lastGitbackPatterns, newCfg.Swarm.Gitback.Enabled) {
					recordSwarmPatternsChange(ctx, s.auditWriter, "gitback", lastGitbackPatterns, newCfg.Swarm.Gitback.Enabled)
					lastGitbackPatterns = append([]string(nil), newCfg.Swarm.Gitback.Enabled...)
				}
				if !sameStringSet(lastLongdrawerPatterns, newCfg.Swarm.Longdrawer.Enabled) {
					recordSwarmPatternsChange(ctx, s.auditWriter, "longdrawer", lastLongdrawerPatterns, newCfg.Swarm.Longdrawer.Enabled)
					lastLongdrawerPatterns = append([]string(nil), newCfg.Swarm.Longdrawer.Enabled...)
				}
				if !sameStringSet(lastFilecastPatterns, newCfg.Swarm.Filecast.Enabled) {
					recordSwarmPatternsChange(ctx, s.auditWriter, "filecast", lastFilecastPatterns, newCfg.Swarm.Filecast.Enabled)
					lastFilecastPatterns = append([]string(nil), newCfg.Swarm.Filecast.Enabled...)
				}
			})
		}

		if s.aiHandler != nil {
			s.aiHandler.RegisterRoutes(gwMux)
		}
		dbHandler := api.NewDatabaseHandler(s.db, s.queries, dbDriver, s.cfg.Database.DSN)
		dbHandler.SetAuditWriter(s.auditWriter)
		dbHandler.RegisterRoutes(gwMux)
		s.authProvider.RegisterRoutes(gwMux)
		if s.scrierHandler != nil {
			s.scrierHandler.RegisterRoutes(gwMux)
			uiHandler.SetScrierEnabled(true)
		}
		// Serve license files so agents can download them during bootstrap.
		gwMux.HandleFunc("GET /license/LICENSE", serveLicenseFile(s.serverRoot, "LICENSE"))
		gwMux.HandleFunc("GET /license/COMMERCIAL-TERMS.md", serveLicenseFile(s.serverRoot, "COMMERCIAL-TERMS.md"))
		if !s.swarmEnabled && s.cfg.Swarm.Curator.S3Port != 0 {
			slog.Warn("curator S3 API configured but disabled — this server does not run the curator substrate (swarm.enabled does not match this host)",
				"port", s.cfg.Swarm.Curator.S3Port)
		}
		s.httpSrv = &http.Server{
			Addr:    s.cfg.Server.APIListen,
			Handler: s.authProvider.Middleware(api.MaxBodyMiddleware(gwMux)),
		}
	}

	return s, nil
}

// Run is implemented in server_run.go alongside its goroutine-start
// helpers. The split keeps server.go focused on construction
// (NewServer) + teardown (Close) without bloating past the readable
// line-count budget.

// emitStartupHardeningFindings runs the defense-in-depth detectors
// (DetectDebugger, DetectInjection) and records each finding as a
// `security.hardening_warning` audit event. The audit writer's
// OnRecord hook fans the event out to the integrations dispatcher
// (Loki / Splunk / Sentinel / etc.) so operators relying on SIEM
// forwarding see the same signal journald consumers get from slog.
//
// VerifyBinarySignature stays in main.go as the only fail-closed gate
// — these calls are observational. Errors recording a finding are
// logged but never block startup (defense in depth must not become a
// startup gate; that's what the binary signature is for).
func (s *Server) emitStartupHardeningFindings(ctx context.Context) {
	findings := append(hardening.DetectDebugger(), hardening.DetectInjection()...)
	for _, f := range findings {
		var payload string
		if len(f.Detail) > 0 {
			b, _ := json.Marshal(f.Detail)
			payload = string(b)
		}
		if err := s.auditWriter.Record(ctx, "security.hardening_warning", "system", "hardening", f.Summary, payload); err != nil {
			slog.Error("recording hardening finding", "error", err, "kind", f.Kind)
		}
	}
}

// Close tears down server state in reverse construction order.
// Idempotent — safe to call even on a partially-constructed *Server
// (e.g. when NewServer returned mid-assembly).
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	if s.peerPrimary != nil {
		s.peerPrimary.Stop()
	}
	if s.manifestStore != nil {
		s.manifestStore.Stop()
	}
	// Workflow engine BEFORE the DB — Close() drains every in-flight
	// executeWorkflow goroutine, all of which hold *db.Queries handles
	// onto s.db. If we close s.db first, the still-running goroutine
	// nil-derefs at the next DispatchPush.
	if s.workflowEngine != nil {
		s.workflowEngine.Close()
	}
	if s.db != nil {
		_ = s.db.Close()
	}
	if s.readDB != nil {
		_ = s.readDB.Close()
	}
	if s.provider != nil {
		_ = s.provider.Close()
	}
	return nil
}

// sqliteDSN appends the per-connection pragmas Vigo requires to a SQLite DSN.
// mattn/go-sqlite3 applies these to every connection it opens, so both pools
// get foreign keys, WAL, the 5s busy timeout, etc. — unlike a one-shot
// `PRAGMA` ExecContext, which only configures whichever pooled connection it
// happened to run on. auto_vacuum is omitted here (persistent, DB-level —
// applied once on the write handle).
func sqliteDSN(base string) string {
	sep := "?"
	for i := 0; i < len(base); i++ {
		if base[i] == '?' {
			sep = "&"
			break
		}
	}
	return base + sep + "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL&_cache_size=-64000&_temp_store=MEMORY"
}

// Package selfmanage provides embedded agent functionality, allowing the
// hobnob server to manage its own host without running a separate hob process.
//
// On startup, if self.hob exists and is enabled, the server self-enrolls as a
// node and runs resources defined in self.hob on a configurable interval.
// Results are recorded in the runs table like any other node.
package selfmanage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"gopkg.in/yaml.v3"

	"github.com/hobnob/hobnob/agent/modules"
	"github.com/hobnob/hobnob/agent/modules/pkgmgr"
	"github.com/hobnob/hobnob/agent/nails"
	"github.com/hobnob/hobnob/agent/runner"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
)

// Config is the parsed self.hob file.
type Config struct {
	Enabled   bool           `yaml:"enabled"`
	Interval  string         `yaml:"interval"`
	Resources []ResourceDef  `yaml:"resources"`
}

// ResourceDef is a resource entry in self.hob. Uses inline YAML to capture
// all type-specific attributes (path, content, owner, mode, etc.).
type ResourceDef struct {
	Name       string         `yaml:"name"`
	Type       string         `yaml:"type"`
	Attributes map[string]any `yaml:",inline"`
}

// Load reads and parses self.hob from path. Returns nil (not an error) if
// the file does not exist.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &cfg, nil
}

// Manager runs the embedded self-management loop.
type Manager struct {
	cfg       *Config
	queries   *db.Queries
	nodeID    uuid.UUID
	hostname  string
	executors map[string]modules.Executor
	interval  time.Duration
}

// NewManager creates a Manager and self-enrolls the server as a node.
// It detects the OS package manager and builds executors for resource
// application. The server is enrolled in the nodes table if not already
// present — no token or RPC ceremony needed since we own the database.
func NewManager(ctx context.Context, cfg *Config, queries *db.Queries) (*Manager, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("getting hostname: %w", err)
	}

	// Self-enroll: insert into nodes table if not already present.
	nodeID, err := selfEnroll(ctx, queries, hostname)
	if err != nil {
		return nil, fmt.Errorf("self-enrolling: %w", err)
	}

	interval := 30 * time.Minute
	if cfg.Interval != "" {
		if parsed, parseErr := time.ParseDuration(cfg.Interval); parseErr == nil {
			interval = parsed
		}
	}

	executors := buildExecutors()

	return &Manager{
		cfg:       cfg,
		queries:   queries,
		nodeID:    nodeID,
		hostname:  hostname,
		executors: executors,
		interval:  interval,
	}, nil
}

// Run starts the self-management loop. It runs once immediately, then on each
// tick of the configured interval. Blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	slog.Info("self-management started",
		"hostname", m.hostname,
		"node_id", m.nodeID,
		"interval", m.interval,
		"resources", len(m.cfg.Resources),
	)

	m.runOnce(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.runOnce(ctx)
		}
	}
}

// runOnce executes one self-management cycle: collect nails, run resources,
// and record results in the database.
func (m *Manager) runOnce(ctx context.Context) {
	// Collect nails so the server node appears with facts in the UI.
	nailsMap, err := nails.CollectAll(ctx, nails.DefaultCollectors())
	if err != nil {
		slog.Error("self-manage: collecting nails", "error", err)
		nailsMap = make(map[string]any)
	}

	// Store nails for this node.
	m.storeNails(ctx, nailsMap)

	// Convert self.hob resources to proto modules.
	protoModules := m.buildModules()

	// Create run record.
	run, err := m.queries.CreateRun(ctx, db.CreateRunParams{
		NodeID:      m.nodeID,
		Status:      "running",
		ModuleCount: int32(len(protoModules[0].Resources)),
	})
	if err != nil {
		slog.Error("self-manage: creating run", "error", err)
		return
	}

	// Execute resources.
	r := runner.NewRunner(m.executors, nailsMap, false, "")
	results, runErr := r.Run(ctx, protoModules)
	if runErr != nil {
		slog.Error("self-manage: running resources", "error", runErr)
	}

	// Record results and finish the run.
	m.recordResults(ctx, run.ID, results)

	// Update last_seen so compliance status stays current.
	_ = m.queries.UpdateLastSeen(ctx, m.nodeID)
}

// selfEnroll inserts the server into the nodes table if not already present.
func selfEnroll(ctx context.Context, queries *db.Queries, hostname string) (uuid.UUID, error) {
	node, err := queries.GetNodeByHostname(ctx, hostname)
	if err == nil {
		slog.Info("self-manage: already enrolled", "node_id", node.ID, "hostname", hostname)
		return node.ID, nil
	}

	node, err = queries.CreateNode(ctx, db.CreateNodeParams{
		Hostname:  hostname,
		IpAddress: "127.0.0.1",
		PubkeyPem: "embedded:self-managed",
	})
	if err != nil {
		return uuid.Nil, err
	}

	slog.Info("self-manage: enrolled server as node",
		"node_id", node.ID,
		"hostname", hostname,
	)
	return node.ID, nil
}

// buildModules wraps self.hob resources into a single proto Module named "self".
func (m *Manager) buildModules() []*pb.Module {
	mod := &pb.Module{Name: "self"}
	for _, res := range m.cfg.Resources {
		mod.Resources = append(mod.Resources, &pb.Resource{
			Name:   res.Name,
			Type:   res.Type,
			Params: attrsToParams(res.Attributes),
		})
	}
	return []*pb.Module{mod}
}

// recordResults stores per-resource outcomes and finishes the run.
func (m *Manager) recordResults(ctx context.Context, runID uuid.UUID, results []modules.ResourceResult) {
	var changed, failed, skipped int32
	for _, res := range results {
		errMsg := pgtype.Text{}
		if res.Error != nil {
			errMsg = pgtype.Text{String: res.Error.Error(), Valid: true}
			failed++
		}
		if res.Changed {
			changed++
		}
		if res.Action == "none" && !res.Changed {
			skipped++
		}

		_, err := m.queries.CreateRunResult(ctx, db.CreateRunResultParams{
			RunID:        runID,
			ModuleName:   res.ModuleName,
			ResourceType: res.ResourceType,
			ResourceName: res.ResourceName,
			ActionTaken:  string(res.Action),
			Changed:      res.Changed,
			ErrorMessage: errMsg,
			DurationMs:   res.DurationMs,
		})
		if err != nil {
			slog.Error("self-manage: storing result", "resource", res.ResourceName, "error", err)
		}
	}

	status := "success"
	if failed > 0 {
		status = "failure"
	}

	err := m.queries.FinishRun(ctx, db.FinishRunParams{
		ID:           runID,
		Status:       status,
		ChangedCount: changed,
		FailedCount:  failed,
		SkippedCount: skipped,
	})
	if err != nil {
		slog.Error("self-manage: finishing run", "error", err)
	}

	slog.Info("self-manage: cycle complete",
		"status", status,
		"changed", changed,
		"failed", failed,
		"skipped", skipped,
	)
}

// storeNails persists collected nails so the server node shows facts in the UI.
func (m *Manager) storeNails(ctx context.Context, nailsMap map[string]any) {
	nailsJSON, err := json.Marshal(nailsMap)
	if err != nil {
		slog.Error("self-manage: marshaling nails", "error", err)
		return
	}
	_, err = m.queries.InsertNodeNails(ctx, db.InsertNodeNailsParams{
		NodeID: m.nodeID,
		Nails:  nailsJSON,
	})
	if err != nil {
		slog.Error("self-manage: storing nails", "error", err)
	}
	// Prune old entries (keep last 10).
	_ = m.queries.PruneNodeNails(ctx, m.nodeID)
}

// attrsToParams converts YAML attribute values to the string map expected by
// proto Resource.Params. Quote mode values must be used in self.hob to
// preserve octal notation (e.g. mode: "0750", not mode: 0750).
func attrsToParams(attrs map[string]any) map[string]string {
	params := make(map[string]string, len(attrs))
	for k, v := range attrs {
		params[k] = fmt.Sprintf("%v", v)
	}
	return params
}

// buildExecutors creates the executor map. Same as the agent's buildExecutors
// but without snapshot support (server doesn't need rollback for self-managed
// resources).
func buildExecutors() map[string]modules.Executor {
	executors := map[string]modules.Executor{
		"file":    &modules.FileExecutor{},
		"service": &modules.ServiceExecutor{},
		"user":    &modules.UserExecutor{},
		"exec":    &modules.ExecExecutor{},
		"archive": &modules.ArchiveExecutor{},
		"cron":    &modules.CronExecutor{},
	}

	osFamily, osDistro := detectOS()

	pm, err := pkgmgr.Detect(osFamily, osDistro)
	if err == nil {
		executors["package"] = &modules.PackageExecutor{PkgMgr: pm}
	}

	if rm := pkgmgr.DetectRepoManager(osFamily, osDistro); rm != nil {
		executors["repository"] = &modules.RepositoryExecutor{RepoMgr: rm}
	}

	return executors
}

func detectOS() (family, distro string) {
	c := &nails.OSCollector{}
	facts, err := c.Collect(context.Background())
	if err != nil {
		return runtime.GOOS, ""
	}
	family, _ = facts["family"].(string)
	distro, _ = facts["distro"].(string)
	return family, distro
}

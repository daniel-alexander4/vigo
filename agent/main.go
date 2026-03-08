package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/hobnob/hobnob/agent/auth"
	"github.com/hobnob/hobnob/agent/checkin"
	"github.com/hobnob/hobnob/agent/nails"
	"github.com/hobnob/hobnob/agent/modules"
	"github.com/hobnob/hobnob/agent/modules/pkgmgr"
	"github.com/hobnob/hobnob/agent/snapshots"
	"github.com/spf13/cobra"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

var (
	dataDir  string
	server   string
	caFile   string
	certFile string
	keyFile  string
	apiAddr  string
	dryRun   bool
	logLevel string
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// setupLogger configures the global slog logger based on --log-level.
func setupLogger() {
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// loadClientID reads the client UUID from <data-dir>/client.uuid.
func loadClientID() (string, error) {
	data, err := os.ReadFile(dataDir + "/client.uuid")
	if err != nil {
		return "", fmt.Errorf("read client UUID from %s/client.uuid: %w (have you run 'hob register'?)", dataDir, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// saveClientID writes the client UUID to <data-dir>/client.uuid.
func saveClientID(id string) error {
	return os.WriteFile(dataDir+"/client.uuid", []byte(id+"\n"), 0600)
}

// detectPackageManager uses OS nails to detect the platform package manager.
func detectPackageManager() (pkgmgr.PackageManager, error) {
	// Quick OS fact lookup for package manager detection.
	osCollector := &nails.OSCollector{}
	osFacts, err := osCollector.Collect(context.Background())
	if err != nil {
		// Fallback: use runtime.GOOS to determine family.
		family := runtime.GOOS
		return pkgmgr.Detect(family, "")
	}

	family, _ := osFacts["family"].(string)
	distro, _ := osFacts["distro"].(string)
	return pkgmgr.Detect(family, distro)
}

// buildExecutors creates the executor map with all resource type handlers.
func buildExecutors(snapshotMgr *snapshots.Manager) map[string]modules.Executor {
	executors := map[string]modules.Executor{
		"file": &modules.FileExecutor{
			SnapshotMgr: snapshotMgr,
		},
		"service":    &modules.ServiceExecutor{},
		"user":       &modules.UserExecutor{},
		"exec":       &modules.ExecExecutor{},
		"archive":    &modules.ArchiveExecutor{},
	}

	// Auto-detect package manager. Non-fatal if detection fails.
	pm, err := detectPackageManager()
	if err != nil {
		slog.Warn("package manager detection failed, package resources will error",
			"error", err,
		)
	} else {
		executors["package"] = &modules.PackageExecutor{PkgMgr: pm}
	}

	// Auto-detect repo manager. Non-fatal if unavailable (some distros
	// like Alpine don't support managed repos).
	osFamily, osDistro := detectOSInfo()
	if rm := pkgmgr.DetectRepoManager(osFamily, osDistro); rm != nil {
		executors["repository"] = &modules.RepositoryExecutor{RepoMgr: rm}
	}

	return executors
}

// detectOSInfo returns the OS family and distro for repo manager detection.
func detectOSInfo() (family, distro string) {
	osCollector := &nails.OSCollector{}
	osFacts, err := osCollector.Collect(context.Background())
	if err != nil {
		return runtime.GOOS, ""
	}
	family, _ = osFacts["family"].(string)
	distro, _ = osFacts["distro"].(string)
	return family, distro
}

// newGRPCClient creates a gRPC client with the configured TLS settings.
func newGRPCClient(clientID string, privateKey []byte) (*checkin.Client, error) {
	return checkin.NewClient(server, caFile, certFile, keyFile, clientID, privateKey)
}

// signalContext returns a context that is cancelled on SIGINT or SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			slog.Info("received signal, shutting down", "signal", sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

var rootCmd = &cobra.Command{
	Use:   "hob",
	Short: "Hobnob configuration management agent",
	Long:  "Agent daemon that pulls desired state from the Hobnob server and applies idempotent resource changes.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		// Ensure data directory exists.
		if err := os.MkdirAll(dataDir, 0700); err != nil {
			return fmt.Errorf("create data directory %s: %w", dataDir, err)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := slog.Default()

		// Load keypair.
		_, priv, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		// Load client UUID.
		clientID, err := loadClientID()
		if err != nil {
			return err
		}

		// Create gRPC client.
		client, err := newGRPCClient(clientID, priv)
		if err != nil {
			return fmt.Errorf("create gRPC client: %w", err)
		}
		defer client.Close()

		// Initialize snapshot manager.
		snapshotMgr := &snapshots.Manager{
			BaseDir:    "/var/lib/hobnob/snapshots",
			MaxPerPath: 5,
		}

		// Build executors.
		executors := buildExecutors(snapshotMgr)

		logger.Info("starting daemon loop",
			"data_dir", dataDir,
			"server", server,
			"api_addr", apiAddr,
			"version", version,
			"dry_run", dryRun,
		)

		// Set up signal-aware context.
		ctx, cancel := signalContext()
		defer cancel()

		// Configure self-update.
		updateCfg := &checkin.UpdateConfig{
			APIAddr:        apiAddr,
			CurrentVersion: version,
		}

		// Start the daemon loop with custom traits cache.
		traitsCacheDir := filepath.Join(dataDir, "traits")
		return checkin.Loop(ctx, client, executors, snapshotMgr, updateCfg, traitsCacheDir)
	},
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a single convergence cycle and exit",
	Long:  "Performs one check-in, executes modules, reports results, and exits. Use --module to apply a single module.",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := slog.Default()

		module, _ := cmd.Flags().GetString("module")

		// Load keypair.
		_, priv, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		// Load client UUID.
		clientID, err := loadClientID()
		if err != nil {
			return err
		}

		// Create gRPC client.
		client, err := newGRPCClient(clientID, priv)
		if err != nil {
			return fmt.Errorf("create gRPC client: %w", err)
		}
		defer client.Close()

		// Initialize snapshot manager.
		snapshotMgr := &snapshots.Manager{
			BaseDir:    "/var/lib/hobnob/snapshots",
			MaxPerPath: 5,
		}

		// Build executors.
		executors := buildExecutors(snapshotMgr)

		// Collect nails.
		ctx := context.Background()
		nailsMap, err := nails.CollectAll(ctx, nails.DefaultCollectors())
		if err != nil {
			logger.Warn("fact collection failed", "error", err)
			nailsMap = make(map[string]any)
		}

		// Report nails.
		if err := client.ReportNails(ctx, nailsMap); err != nil {
			logger.Warn("failed to report nails", "error", err)
		}

		// Run once.
		runResult, err := checkin.RunOnce(ctx, client, executors, nailsMap, dryRun, module)
		if err != nil {
			return fmt.Errorf("convergence run failed: %w", err)
		}

		// Print summary.
		hasError := false
		for _, r := range runResult.Results {
			status := string(r.Action)
			if r.Error != nil {
				status = "ERROR: " + r.Error.Error()
				hasError = true
			}
			logger.Info("resource result",
				"module", r.ModuleName,
				"type", r.ResourceType,
				"name", r.ResourceName,
				"action", status,
				"changed", r.Changed,
			)
		}

		if hasError {
			return fmt.Errorf("one or more resources failed")
		}

		return nil
	},
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register this agent with the Hobnob server",
	Long:  "Generates a keypair (if needed) and registers with the server using a one-time token.",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := slog.Default()

		token, _ := cmd.Flags().GetString("token")

		// Load or generate keypair.
		pub, priv, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		pubPEM, err := auth.PublicKeyPEM(pub)
		if err != nil {
			return fmt.Errorf("export public key PEM: %w", err)
		}

		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("get hostname: %w", err)
		}

		// Get primary IP address.
		ipAddr := getOutboundIP()

		logger.Info("registering with server",
			"server", server,
			"hostname", hostname,
			"ip", ipAddr,
			"data_dir", dataDir,
		)

		// Create gRPC client (no client UUID yet, use empty string).
		client, err := checkin.NewClient(server, caFile, certFile, keyFile, "", priv)
		if err != nil {
			return fmt.Errorf("create gRPC client: %w", err)
		}
		defer client.Close()

		// Call Register RPC.
		resp, err := client.Register(context.Background(), hostname, ipAddr, pubPEM, token)
		if err != nil {
			return fmt.Errorf("registration failed: %w", err)
		}

		if !resp.Success {
			return fmt.Errorf("registration rejected: %s", resp.Message)
		}

		// Save client UUID.
		if err := saveClientID(resp.ClientId); err != nil {
			return fmt.Errorf("save client UUID: %w", err)
		}

		logger.Info("registration successful",
			"client_id", resp.ClientId,
			"message", resp.Message,
		)

		return nil
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the agent version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version)
	},
}

// getOutboundIP returns the preferred outbound IP of this machine.
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", "/etc/hobnob", "directory for agent data (keypair, UUID)")
	rootCmd.PersistentFlags().StringVar(&server, "server", "localhost:9090", "server gRPC address")
	rootCmd.PersistentFlags().StringVar(&caFile, "ca-file", "", "CA certificate file for server verification")
	rootCmd.PersistentFlags().StringVar(&certFile, "cert-file", "", "client certificate file for mTLS (optional in dev)")
	rootCmd.PersistentFlags().StringVar(&keyFile, "key-file", "", "client key file for mTLS (optional in dev)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&apiAddr, "api-addr", "http://localhost:8080", "server REST API address for self-update")

	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview mode: show what would change without applying")

	runCmd.Flags().String("module", "", "apply only the named module")
	runCmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview mode: show what would change without applying")

	registerCmd.Flags().String("token", "", "one-time registration token")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(registerCmd)
	rootCmd.AddCommand(versionCmd)
}

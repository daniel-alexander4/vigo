// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/hobnob/hobnob/agent/auth"
	"github.com/hobnob/hobnob/agent/checkin"
	"github.com/hobnob/hobnob/agent/modules"
	"github.com/hobnob/hobnob/agent/modules/pkgmgr"
	"github.com/hobnob/hobnob/agent/traits"
	"github.com/hobnob/hobnob/agent/snapshots"
	"github.com/spf13/cobra"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

var (
	dataDir            string
	server             string
	caFile             string
	certFile           string
	keyFile            string
	apiAddr            string
	dryRun             bool
	logLevel           string
	executorsDir       string
	snapshotDir        string
	snapshotRetention  int
	traitRefreshCycles int
	resultCacheCycles  int
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

// loadThrallID reads the thrall UUID from <data-dir>/thrall.uuid.
func loadNodeID() (string, error) {
	data, err := os.ReadFile(dataDir + "/thrall.uuid")
	if err != nil {
		return "", fmt.Errorf("read thrall UUID from %s/thrall.uuid: %w (have you run 'hobnob register'?)", dataDir, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// saveThrallID writes the thrall UUID to <data-dir>/thrall.uuid.
func saveNodeID(id string) error {
	return os.WriteFile(dataDir+"/thrall.uuid", []byte(id+"\n"), 0600)
}

// saveSpokeAddr writes the spoke gRPC address to <data-dir>/spoke.addr.
// If addr is empty, the file is removed (thrall connects to the default server).
func saveSpokeAddr(addr string) error {
	path := dataDir + "/spoke.addr"
	if addr == "" {
		os.Remove(path)
		return nil
	}
	return os.WriteFile(path, []byte(addr+"\n"), 0600)
}

// loadSpokeAddr reads the spoke address. Returns empty string if not set.
func loadSpokeAddr() string {
	data, err := os.ReadFile(dataDir + "/spoke.addr")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// detectOSInfo returns the OS family and distro from traits. Falls back to
// runtime.GOOS if the OS collector fails.
func detectOSInfo() (family, distro string) {
	osCollector := &traits.OSCollector{}
	osFacts, err := osCollector.Collect(context.Background())
	if err != nil {
		return runtime.GOOS, ""
	}
	family, _ = osFacts["family"].(string)
	distro, _ = osFacts["distro"].(string)
	return family, distro
}

// buildExecutors creates the executor map with all resource type handlers.
func buildExecutors(snapshotMgr *snapshots.Manager) map[string]modules.Executor {
	executors := map[string]modules.Executor{
		"file": &modules.FileExecutor{
			SnapshotMgr: snapshotMgr,
		},
		"service":          &modules.ServiceExecutor{},
		"user":             &modules.UserExecutor{},
		"exec":             &modules.ExecExecutor{},
		"archive":          &modules.ArchiveExecutor{},
		"cron":             &modules.CronExecutor{},
		"process":          &modules.ProcessExecutor{},
		"file_line":        &modules.FileLineExecutor{},
		"group":            &modules.GroupExecutor{},
		"host":             &modules.HostExecutor{},
		"sysctl":           &modules.SysctlExecutor{},
		"mount":            &modules.MountExecutor{},
		"hostname":         &modules.HostnameExecutor{},
		"timezone":         &modules.TimezoneExecutor{},
		"sshkey":           &modules.SSHKeyExecutor{},
		"firewall":         &modules.FirewallExecutor{},
		"kernel_module":    &modules.KernelModuleExecutor{},
		"swap":             &modules.SwapExecutor{},
		"tmpfile":          &modules.TmpfileExecutor{},
		"ini":              &modules.INIExecutor{},
		"json_file":        &modules.JSONFileExecutor{},
		"logrotate":        &modules.LogrotateExecutor{},
		"git":              &modules.GitExecutor{},
		"reboot":           &modules.RebootExecutor{},
		"alternatives":     &modules.AlternativesExecutor{},
		"pip":              &modules.PipExecutor{},
		"gem":              &modules.GemExecutor{},
		"npm":              &modules.NpmExecutor{},
		"blockinfile":      &modules.BlockinfileExecutor{},
		"docker_container": &modules.DockerContainerExecutor{},
		"docker_image":     &modules.DockerImageExecutor{},
	}

	osFamily, osDistro := detectOSInfo()

	// Auto-detect package manager. Non-fatal if detection fails.
	pm, err := pkgmgr.Detect(osFamily, osDistro)
	if err != nil {
		slog.Warn("package manager detection failed, package resources will error",
			"error", err,
		)
	} else {
		executors["package"] = &modules.PackageExecutor{PkgMgr: pm}
	}

	// Auto-detect repo manager. Non-fatal if unavailable (some distros
	// like Alpine don't support managed repos).
	if rm := pkgmgr.DetectRepoManager(osFamily, osDistro); rm != nil {
		executors["repository"] = &modules.RepositoryExecutor{RepoMgr: rm}
	}

	// Load custom executors from the executors directory.
	loadCustomExecutors(executors, executorsDir)

	return executors
}

// loadCustomExecutors scans dir for executable files and registers each as a
// CustomExecutor keyed by filename (without extension). Built-in types are
// never overridden.
func loadCustomExecutors(executors map[string]modules.Executor, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory doesn't exist or unreadable — not an error.
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Must be executable.
		if info.Mode()&0111 == 0 {
			continue
		}

		name := entry.Name()
		// Strip extension if present.
		if ext := filepath.Ext(name); ext != "" {
			name = name[:len(name)-len(ext)]
		}

		if _, builtin := executors[name]; builtin {
			slog.Warn("custom executor ignored: name conflicts with built-in type", "type", name)
			continue
		}

		path := filepath.Join(dir, entry.Name())
		executors[name] = &modules.CustomExecutor{ScriptPath: path}
		slog.Info("loaded custom executor", "type", name, "path", path)
	}
}

// newGRPCClient creates a gRPC client with the configured TLS settings.
// If explicit cert/key/ca flags are empty, it checks for bootstrap-issued certs
// in the data directory.
func newGRPCClient(thrallID string, privateKey []byte) (*checkin.Client, error) {
	ca, cert, key := caFile, certFile, keyFile
	if ca == "" && auth.HasBootstrapCerts(dataDir) {
		ca = auth.CACertPath(dataDir)
		cert = auth.TLSCertPath(dataDir)
		key = auth.TLSKeyPath(dataDir)
	}
	// Use spoke address if hub routed us to a spoke during enrollment.
	addr := server
	if spokeAddr := loadSpokeAddr(); spokeAddr != "" {
		addr = spokeAddr
		slog.Info("using spoke address from enrollment routing", "spoke_addr", addr)
	}
	return checkin.NewClient(addr, ca, cert, key, thrallID, privateKey)
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
	Use:   "hobnob",
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

		// Load thrall UUID.
		thrallID, err := loadNodeID()
		if err != nil {
			return err
		}

		// Create gRPC client.
		client, err := newGRPCClient(thrallID, priv)
		if err != nil {
			return fmt.Errorf("create gRPC client: %w", err)
		}
		defer client.Close()

		// Initialize snapshot manager.
		snapshotMgr := &snapshots.Manager{
			BaseDir:    snapshotDir,
			MaxPerPath: snapshotRetention,
		}

		// Build executors.
		executors := buildExecutors(snapshotMgr)

		// Dry-run: single cycle with trace, then exit.
		if dryRun {
			return dryRunOnce(client, executors)
		}

		logger.Info("starting daemon loop",
			"data_dir", dataDir,
			"server", server,
			"api_addr", apiAddr,
			"version", version,
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
		return checkin.Loop(ctx, client, executors, snapshotMgr, updateCfg, traitsCacheDir, checkin.LoopConfig{
			TraitRefreshCycles: traitRefreshCycles,
			ResultCacheCycles:  resultCacheCycles,
		})
	},
}

// dryRunOnce performs a single check-in with dry-run, prints the resolution
// trace and per-resource results, and returns. Used by both `hobnob --dry-run`
// and `hobnob run --dry-run`.
func dryRunOnce(client *checkin.Client, executors map[string]modules.Executor) error {
	return runOnceWithTrace(client, executors, true, "")
}

// runOnceWithTrace performs a single convergence run with optional dry-run
// and prints a trace of the resolution chain and per-resource results.
func runOnceWithTrace(client *checkin.Client, executors map[string]modules.Executor, isDryRun bool, module string) error {
	logger := slog.Default()

	ctx := context.Background()
	traitsMap, err := traits.CollectAll(ctx, traits.DefaultCollectors())
	if err != nil {
		logger.Warn("fact collection failed", "error", err)
		traitsMap = make(map[string]any)
	}

	traitsJSON, marshalErr := json.Marshal(traitsMap)
	if marshalErr != nil {
		logger.Warn("failed to marshal traits", "error", marshalErr)
		traitsJSON = nil
	}

	runResult, err := checkin.RunOnce(ctx, client, executors, traitsMap, isDryRun, module, "", traitsJSON, nil, nil)
	if err != nil {
		return fmt.Errorf("convergence run failed: %w", err)
	}

	// ANSI color helpers.
	bold := "\033[1m"
	dim := "\033[2m"
	red := "\033[31m"
	green := "\033[32m"
	yellow := "\033[33m"
	cyan := "\033[36m"
	reset := "\033[0m"

	// Print trace header.
	if runResult.PolicyBundle != nil {
		b := runResult.PolicyBundle
		if isDryRun {
			fmt.Fprintf(os.Stderr, "\n%s--- Dry Run ---%s\n", bold+cyan, reset)
		} else {
			fmt.Fprintf(os.Stderr, "\n%s--- Run ---%s\n", bold+green, reset)
		}
		fmt.Fprintf(os.Stderr, "%sMatch:%s       %s\n", bold, reset, b.MatchedPattern)
		if b.Environment != "" {
			fmt.Fprintf(os.Stderr, "%sEnvironment:%s %s\n", bold, reset, b.Environment)
		}
		if len(b.Roles) > 0 {
			fmt.Fprintf(os.Stderr, "%sRoles:%s       %s\n", bold, reset, strings.Join(b.Roles, ", "))
		}
		var modNames []string
		for _, m := range b.Modules {
			modNames = append(modNames, m.Name)
		}
		fmt.Fprintf(os.Stderr, "%sModules:%s     %s\n", bold, reset, strings.Join(modNames, ", "))
		fmt.Fprintln(os.Stderr)
	}

	// Print results grouped by module.
	hasError := false
	curModule := ""
	for _, r := range runResult.Results {
		if r.ModuleName != curModule {
			curModule = r.ModuleName
			fmt.Fprintf(os.Stderr, "  %sModule: %s%s\n", bold, curModule, reset)
		}
		status := string(r.Action)
		statusColor := dim // "already in desired state"
		switch r.Action {
		case modules.ActionCreated:
			statusColor = green
		case modules.ActionUpdated:
			statusColor = yellow
		case modules.ActionDeleted:
			statusColor = red
		case modules.ActionSkipped:
			statusColor = cyan
		}
		if r.Error != nil {
			status = "ERROR: " + r.Error.Error()
			statusColor = bold + red
			hasError = true
		}
		fmt.Fprintf(os.Stderr, "    %s%-12s%s %-24s %s%s%s\n", dim, "["+r.ResourceType+"]", reset, r.ResourceName, statusColor, status, reset)
	}

	if hasError {
		return fmt.Errorf("one or more resources failed")
	}
	return nil
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a single convergence cycle and exit",
	Long:  "Performs one check-in, executes modules, reports results, and exits. Use --module to apply a single module.",
	RunE: func(cmd *cobra.Command, args []string) error {
		module, _ := cmd.Flags().GetString("module")

		// Load keypair.
		_, priv, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		// Load thrall UUID.
		thrallID, err := loadNodeID()
		if err != nil {
			return err
		}

		// Create gRPC client.
		client, err := newGRPCClient(thrallID, priv)
		if err != nil {
			return fmt.Errorf("create gRPC client: %w", err)
		}
		defer client.Close()

		// Build executors.
		snapshotMgr := &snapshots.Manager{
			BaseDir:    snapshotDir,
			MaxPerPath: snapshotRetention,
		}
		executors := buildExecutors(snapshotMgr)

		return runOnceWithTrace(client, executors, dryRun, module)
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

		// Create gRPC client (no thrall UUID yet, use empty string).
		client, err := checkin.NewClient(server, caFile, certFile, keyFile, "", priv)
		if err != nil {
			return fmt.Errorf("create gRPC client: %w", err)
		}
		defer client.Close()

		// Call Register RPC.
		resp, err := client.Register(context.Background(), hostname, ipAddr, pubPEM, token)
		if err != nil {
			return fmt.Errorf("registration failed: %w\n\nhint: verify the server address, check that the CA cert is correct, and ensure the server is reachable on the gRPC port", err)
		}

		if !resp.Success {
			return fmt.Errorf("registration rejected: %s\n\nhint: check that the token is valid and unused, and that the hostname %q matches an approved pattern", resp.Message, hostname)
		}

		// Save thrall UUID.
		if err := saveNodeID(resp.ThrallId); err != nil {
			return fmt.Errorf("save thrall UUID: %w", err)
		}

		// Save spoke address if hub routed us to a spoke.
		if resp.SpokeAddr != "" {
			if err := saveSpokeAddr(resp.SpokeAddr); err != nil {
				return fmt.Errorf("save spoke address: %w", err)
			}
			logger.Info("hub routed to spoke, subsequent check-ins will use spoke",
				"spoke_addr", resp.SpokeAddr,
			)
		}

		logger.Info("registration successful",
			"thrall_id", resp.ThrallId,
			"message", resp.Message,
		)

		return nil
	},
}

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap this agent with the Hobnob server",
	Long: `Generates keys, creates a CSR, registers with the server, and receives
signed TLS certificates. Supports both token-based and trusted enrollment.

After bootstrap, the agent has everything it needs to connect via mTLS.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		logger := slog.Default()

		token, _ := cmd.Flags().GetString("token")
		bootstrapServer, _ := cmd.Flags().GetString("server")

		if bootstrapServer == "" {
			return fmt.Errorf("--server is required (e.g. http://hobd.example.com:8181)")
		}

		// Load or generate ED25519 keypair (for request signing).
		pub, _, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		pubPEM, err := auth.PublicKeyPEM(pub)
		if err != nil {
			return fmt.Errorf("export public key PEM: %w", err)
		}

		// Generate TLS keypair and CSR.
		tlsKey, err := auth.GenerateTLSKey()
		if err != nil {
			return fmt.Errorf("generate TLS key: %w", err)
		}

		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("get hostname: %w", err)
		}

		csrPEM, err := auth.GenerateCSR(tlsKey, hostname)
		if err != nil {
			return fmt.Errorf("generate CSR: %w", err)
		}

		ipAddr := getOutboundIP()

		logger.Info("bootstrapping with server",
			"server", bootstrapServer,
			"hostname", hostname,
			"ip", ipAddr,
		)

		// Call the bootstrap register endpoint.
		type registerReq struct {
			Hostname  string `json:"hostname"`
			IPAddress string `json:"ip_address"`
			PubkeyPEM string `json:"pubkey_pem"`
			CSRPEM    string `json:"csr_pem"`
			Token     string `json:"token,omitempty"`
		}
		type registerResp struct {
			ThrallID        string `json:"thrall_id"`
			SignedCertPEM string `json:"signed_cert_pem"`
			CACertPEM     string `json:"ca_cert_pem"`
		}

		reqBody := registerReq{
			Hostname:  hostname,
			IPAddress: ipAddr,
			PubkeyPEM: pubPEM,
			CSRPEM:    string(csrPEM),
			Token:     token,
		}
		bodyJSON, _ := json.Marshal(reqBody)

		url := strings.TrimRight(bootstrapServer, "/") + "/api/v1/bootstrap/register"
		httpResp, err := http.Post(url, "application/json", bytes.NewReader(bodyJSON))
		if err != nil {
			return fmt.Errorf("bootstrap request failed: %w", err)
		}
		defer httpResp.Body.Close()

		respBody, _ := io.ReadAll(httpResp.Body)

		if httpResp.StatusCode != http.StatusOK {
			return fmt.Errorf("bootstrap rejected (HTTP %d): %s", httpResp.StatusCode, strings.TrimSpace(string(respBody)))
		}

		var resp registerResp
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return fmt.Errorf("parse bootstrap response: %w", err)
		}

		// Save everything to disk.
		if err := auth.SaveTLSKey(dataDir, tlsKey); err != nil {
			return fmt.Errorf("save TLS key: %w", err)
		}
		if err := auth.SaveTLSCert(dataDir, []byte(resp.SignedCertPEM)); err != nil {
			return fmt.Errorf("save TLS cert: %w", err)
		}
		if err := auth.SaveCACert(dataDir, []byte(resp.CACertPEM)); err != nil {
			return fmt.Errorf("save CA cert: %w", err)
		}
		if err := saveNodeID(resp.ThrallID); err != nil {
			return fmt.Errorf("save thrall UUID: %w", err)
		}

		logger.Info("bootstrap successful",
			"thrall_id", resp.ThrallID,
			"tls_cert", auth.TLSCertPath(dataDir),
			"ca_cert", auth.CACertPath(dataDir),
		)

		// Install and start systemd service if requested.
		installService, _ := cmd.Flags().GetBool("install-service")
		if installService {
			grpcAddr, err := installSystemdService(cmd, bootstrapServer, dataDir)
			if err != nil {
				logger.Warn("systemd service installation failed", "error", err)
				fmt.Fprintf(os.Stderr, "Service install failed: %v\nStart the agent manually:\n  hobnob --server <grpc-host>:1530 --data-dir %s\n", err, dataDir)
				return nil
			}

			// Verify connectivity with a single check-in before exiting.
			logger.Info("verifying agent check-in...")
			if err := verifyCheckIn(ctx, grpcAddr, dataDir, resp.ThrallID); err != nil {
				logger.Warn("verification check-in failed", "error", err)
				fmt.Fprintf(os.Stderr, "Check-in verification failed: %v\nThe service is installed but may not be able to reach the server.\n", err)
			} else {
				logger.Info("check-in verified, hobnob-thrall service is running")
			}
		}

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

// installSystemdService creates, enables, and starts a systemd service for hobnob.
// Returns the gRPC address used in the service unit.
func installSystemdService(cmd *cobra.Command, bootstrapServer, dataDir string) (string, error) {
	// Check for systemctl.
	if _, err := os.Stat("/usr/bin/systemctl"); os.IsNotExist(err) {
		if _, err := os.Stat("/bin/systemctl"); os.IsNotExist(err) {
			return "", fmt.Errorf("systemctl not found")
		}
	}

	// Derive gRPC host from bootstrap server URL.
	grpcPort, _ := cmd.Flags().GetString("grpc-port")
	host := bootstrapServer
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	grpcAddr := net.JoinHostPort(host, grpcPort)

	// Copy the running binary to /usr/local/bin/hobnob.
	const installPath = "/usr/local/bin/hobnob"
	srcPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable path: %w", err)
	}
	srcPath, err = filepath.EvalSymlinks(srcPath)
	if err != nil {
		return "", fmt.Errorf("resolving symlinks: %w", err)
	}
	if srcPath != installPath {
		src, err := os.ReadFile(srcPath)
		if err != nil {
			return "", fmt.Errorf("reading binary: %w", err)
		}
		if err := os.WriteFile(installPath, src, 0755); err != nil {
			return "", fmt.Errorf("installing binary to %s: %w", installPath, err)
		}
		fmt.Printf("Installed hobnob to %s\n", installPath)
	}

	unit := fmt.Sprintf(`[Unit]
Description=Hobnob Configuration Management Agent
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s --server %s --data-dir %s
Restart=always
RestartSec=10
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, installPath, grpcAddr, dataDir)

	if err := os.WriteFile("/etc/systemd/system/hobnob-thrall.service", []byte(unit), 0644); err != nil {
		return "", fmt.Errorf("writing unit file: %w", err)
	}

	// daemon-reload, enable, start.
	for _, args := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "hobnob-thrall"},
		{"systemctl", "start", "hobnob-thrall"},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
	}

	fmt.Println("Hobnob agent service installed and started.")
	fmt.Println("  systemctl status hobnob-thrall    # check status")
	fmt.Println("  journalctl -u hobnob-thrall -f    # follow logs")
	return grpcAddr, nil
}

// verifyCheckIn does a single gRPC check-in to confirm the agent can reach the server.
func verifyCheckIn(ctx context.Context, grpcAddr, dataDir, thrallID string) error {
	_, privKey, err := auth.LoadOrGenerate(dataDir)
	if err != nil {
		return fmt.Errorf("loading keypair: %w", err)
	}

	client, err := checkin.NewClient(
		grpcAddr,
		auth.CACertPath(dataDir),
		auth.TLSCertPath(dataDir),
		auth.TLSKeyPath(dataDir),
		thrallID,
		privKey,
	)
	if err != nil {
		return fmt.Errorf("connecting to server: %w", err)
	}
	defer client.Close()

	_, err = client.CheckIn(ctx, "", nil, 0, nil)
	if err != nil {
		return fmt.Errorf("check-in failed: %w", err)
	}
	return nil
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", "/etc/hobnob-thrall", "directory for agent data (keypair, UUID, certs)")
	rootCmd.PersistentFlags().StringVar(&server, "server", "localhost:1530", "server gRPC address")
	rootCmd.PersistentFlags().StringVar(&caFile, "ca-file", "", "CA certificate file for server verification")
	rootCmd.PersistentFlags().StringVar(&certFile, "cert-file", "", "client certificate file for mTLS (optional in dev)")
	rootCmd.PersistentFlags().StringVar(&keyFile, "key-file", "", "client key file for mTLS (optional in dev)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&apiAddr, "api-addr", "http://localhost:8181", "server REST API address for self-update")
	rootCmd.PersistentFlags().StringVar(&executorsDir, "executors-dir", "/etc/hobnob-thrall/executors", "directory for custom executor scripts")
	rootCmd.PersistentFlags().StringVar(&snapshotDir, "snapshot-dir", "/var/lib/hobnob/snapshots", "directory for file snapshots (rollback)")
	rootCmd.PersistentFlags().IntVar(&snapshotRetention, "snapshot-retention", 5, "max snapshots to keep per file path")
	rootCmd.PersistentFlags().IntVar(&traitRefreshCycles, "trait-refresh-cycles", 10, "stable trait collectors refresh every N check-in cycles")
	rootCmd.PersistentFlags().IntVar(&resultCacheCycles, "result-cache-cycles", 5, "no-op resource results cached for N check-in cycles")

	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview mode: show what would change without applying")

	runCmd.Flags().String("module", "", "apply only the named module")
	runCmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview mode: show what would change without applying")

	registerCmd.Flags().String("token", "", "one-time registration token")

	bootstrapCmd.Flags().String("token", "", "one-time registration token (optional for trusted enrollment)")
	bootstrapCmd.Flags().String("server", "", "server REST API address (e.g. http://hobd.example.com:8181)")
	bootstrapCmd.Flags().Bool("install-service", false, "install and start a systemd service for the agent")
	bootstrapCmd.Flags().String("grpc-port", "1530", "gRPC port for the systemd service (default 1530)")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(registerCmd)
	rootCmd.AddCommand(bootstrapCmd)
	rootCmd.AddCommand(versionCmd)
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/validate"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var doctorPreStart bool
var doctorConfigFile string

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Validate server health: config, TLS, secrets, database, license, and spanner",
	Long: `Run health checks to verify the server is operating correctly.

By default, queries the running server's /api/v1/doctor endpoint.

With --pre-start, runs local checks without a running server:
  config_file, secrets_provider, secret_resolution, tls_certs,
  config_dir, config_validation, spanner`,
	RunE: runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorPreStart, "pre-start", false, "Run local pre-start checks (no server needed)")
	doctorCmd.Flags().StringVar(&doctorConfigFile, "config", "server.yaml", "Path to server.yaml (for --pre-start)")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	if doctorPreStart {
		return runDoctorPreStart()
	}
	return runDoctorRuntime()
}

// runDoctorRuntime queries the running server.
func runDoctorRuntime() error {
	client := newAPIClient()

	var resp struct {
		Status string `json:"status"`
		Checks []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}

	if err := client.get("/api/v1/doctor", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	for _, c := range resp.Checks {
		printCheck(c.Name, c.Status, c.Message)
	}

	fmt.Println()
	fmt.Printf("  Status: %s\n", colorStatus(resp.Status))

	if resp.Status == "unhealthy" {
		return fmt.Errorf("server is unhealthy")
	}
	return nil
}

// runDoctorPreStart runs local checks without a server.
func runDoctorPreStart() error {
	var checks []preStartCheck
	hasFailure := false

	// 1. config_file — parse server.yaml
	secretsCfg, serverCfg, cfgErr := loadConfigForDoctor(doctorConfigFile)
	if cfgErr != nil {
		checks = append(checks, preStartCheck{"config_file", "fail", cfgErr.Error()})
		hasFailure = true
	} else {
		checks = append(checks, preStartCheck{"config_file", "pass", ""})
	}

	// 2. secrets_provider — initialize provider
	var provider secrets.Provider
	if cfgErr == nil {
		var err error
		provider, err = secrets.NewFromConfig(secretsCfg)
		if err != nil {
			checks = append(checks, preStartCheck{"secrets_provider", "fail", err.Error()})
			hasFailure = true
		} else {
			defer provider.Close()
			checks = append(checks, preStartCheck{"secrets_provider", "pass", secretsCfg.Backend})
		}
	} else {
		checks = append(checks, preStartCheck{"secrets_provider", "skip", "config_file failed"})
	}

	// 3. secret_resolution — load full config (resolves secret: refs)
	var fullCfg *config.ServerConfig
	if provider != nil && cfgErr == nil {
		var err error
		fullCfg, err = config.LoadServerConfig(doctorConfigFile, provider, secretsCfg.Backend)
		if err != nil {
			checks = append(checks, preStartCheck{"secret_resolution", "fail", err.Error()})
			hasFailure = true
		} else {
			checks = append(checks, preStartCheck{"secret_resolution", "pass", ""})
		}
	} else {
		checks = append(checks, preStartCheck{"secret_resolution", "skip", "provider unavailable"})
	}

	// 4. tls_certs — check cert files exist, parse, check expiry
	if fullCfg != nil {
		checks = append(checks, checkTLSCerts(fullCfg.Server.TLS.CertFile, fullCfg.Server.TLS.KeyFile))
	} else if serverCfg != nil {
		// Try raw paths even without secret resolution
		checks = append(checks, checkTLSCerts(serverCfg.Server.TLS.CertFile, serverCfg.Server.TLS.KeyFile))
	} else {
		checks = append(checks, preStartCheck{"tls_certs", "skip", "config unavailable"})
	}

	// 5. config_dir — example-configs/ exists with modules/, roles/, envoys/
	configDir := "examples"
	if dc := checkConfigDir(configDir); dc.status == "fail" {
		hasFailure = true
		checks = append(checks, dc)
	} else {
		checks = append(checks, dc)
	}

	// 6. config_validation — load all .vgo module files and validate
	checks = append(checks, checkConfigValidation(configDir))

	// 7. spanner
	if serverCfg != nil {
		if err := serverCfg.Spanner.Validate(); err != nil {
			checks = append(checks, preStartCheck{"spanner", "fail", err.Error()})
			hasFailure = true
		} else {
			mode := string(serverCfg.Spanner.Mode)
			if mode == "" {
				mode = "standalone"
			}
			checks = append(checks, preStartCheck{"spanner", "pass", mode})
		}
	} else {
		checks = append(checks, preStartCheck{"spanner", "skip", "config unavailable"})
	}

	// Print results
	for _, c := range checks {
		printCheck(c.name, c.status, c.message)
	}

	fmt.Println()
	if hasFailure {
		fmt.Printf("  Status: %s\n", red("unhealthy"))
		return fmt.Errorf("pre-start checks failed")
	}

	hasWarn := false
	for _, c := range checks {
		if c.status == "warn" {
			hasWarn = true
			break
		}
	}
	if hasWarn {
		fmt.Printf("  Status: %s\n", colorStatus("degraded"))
	} else {
		fmt.Printf("  Status: %s\n", colorStatus("healthy"))
	}

	return nil
}

type preStartCheck struct {
	name    string
	status  string
	message string
}

func loadConfigForDoctor(path string) (secrets.Config, *config.ServerConfig, error) {
	secretsCfg, err := config.LoadSecretsConfig(path)
	if err != nil {
		return secrets.Config{}, nil, err
	}

	// Also do a raw parse (without secret resolution) to get TLS/spanner fields.
	data, err := os.ReadFile(path)
	if err != nil {
		return secretsCfg, nil, nil
	}

	var cfg config.ServerConfig
	if err := parseYAML(data, &cfg); err != nil {
		return secretsCfg, nil, nil
	}

	return secretsCfg, &cfg, nil
}

func parseYAML(data []byte, v any) error {
	return yaml.Unmarshal(data, v)
}

func checkTLSCerts(certFile, keyFile string) preStartCheck {
	if strings.HasPrefix(certFile, "secret:") || strings.HasPrefix(keyFile, "secret:") {
		return preStartCheck{"tls_certs", "skip", "paths are secret references (resolve with secrets provider)"}
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return preStartCheck{"tls_certs", "fail", err.Error()}
	}

	if len(cert.Certificate) == 0 {
		return preStartCheck{"tls_certs", "fail", "no certificates in chain"}
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return preStartCheck{"tls_certs", "fail", fmt.Sprintf("parsing cert: %v", err)}
	}

	daysLeft := int(time.Until(leaf.NotAfter).Hours() / 24)
	expiry := leaf.NotAfter.Format("2006-01-02")

	if daysLeft < 0 {
		return preStartCheck{"tls_certs", "fail", fmt.Sprintf("cert expired %s", expiry)}
	}
	if daysLeft < 30 {
		return preStartCheck{"tls_certs", "warn", fmt.Sprintf("expires in %d days (%s)", daysLeft, expiry)}
	}
	return preStartCheck{"tls_certs", "pass", fmt.Sprintf("expires %s", expiry)}
}

func checkConfigDir(dir string) preStartCheck {
	subdirs := []string{"configcrates", "roles", "envoys"}
	var missing []string
	for _, sub := range subdirs {
		path := filepath.Join(dir, sub)
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			missing = append(missing, sub)
		}
	}

	if _, err := os.Stat(dir); err != nil {
		return preStartCheck{"config_dir", "fail", fmt.Sprintf("%s does not exist", dir)}
	}

	if len(missing) > 0 {
		return preStartCheck{"config_dir", "warn", fmt.Sprintf("missing subdirs: %s", strings.Join(missing, ", "))}
	}
	return preStartCheck{"config_dir", "pass", dir}
}

func checkConfigValidation(dir string) preStartCheck {
	configcratesDir := filepath.Join(dir, "stacks", "configcrates")
	if _, err := os.Stat(configcratesDir); err != nil {
		return preStartCheck{"config_validation", "skip", "configcrates dir not found"}
	}

	var totalConfigcrates, totalErrors, totalWarnings int

	err := filepath.WalkDir(configcratesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".vgo" && ext != ".yaml" && ext != ".yml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		def, err := config.ParseConfigcrateFile(data)
		if err != nil {
			totalErrors++
			return nil
		}
		totalConfigcrates++

		mod := validate.Configcrate{Name: def.Name}
		for _, r := range def.Resources {
			mod.Resources = append(mod.Resources, validate.Resource{
				Name:        r.Name,
				Type:        r.Type,
				Attributes:  r.Attributes,
				Notify:      r.Notify,
				Subscribes:  r.Subscribes,
				WatchSecret: r.WatchSecret,
			})
		}

		findings := validate.ValidateConfigcrate(mod)
		for _, f := range findings {
			if f.Severity == validate.SeverityError {
				totalErrors++
			} else {
				totalWarnings++
			}
		}
		return nil
	})

	if err != nil {
		return preStartCheck{"config_validation", "fail", err.Error()}
	}

	msg := fmt.Sprintf("%d configcrates", totalConfigcrates)
	if totalErrors > 0 {
		msg += fmt.Sprintf(", %d errors", totalErrors)
	}
	if totalWarnings > 0 {
		msg += fmt.Sprintf(", %d warnings", totalWarnings)
	}

	if totalErrors > 0 {
		return preStartCheck{"config_validation", "fail", msg}
	}
	if totalWarnings > 0 {
		return preStartCheck{"config_validation", "warn", msg}
	}
	return preStartCheck{"config_validation", "pass", msg}
}

func printCheck(name, status, message string) {
	// Pad name + dots to 24 chars
	dots := 24 - len(name)
	if dots < 3 {
		dots = 3
	}
	padding := " " + strings.Repeat(".", dots) + " "

	var statusStr string
	switch status {
	case "pass":
		statusStr = green("OK")
	case "warn":
		statusStr = yellow("WARN")
	case "fail":
		statusStr = red("FAIL")
	case "skip":
		statusStr = dim("SKIP")
	default:
		statusStr = status
	}

	if message != "" {
		fmt.Printf("  %s%s%s (%s)\n", name, padding, statusStr, message)
	} else {
		fmt.Printf("  %s%s%s\n", name, padding, statusStr)
	}
}

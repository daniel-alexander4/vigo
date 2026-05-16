// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"git.alexander4.io/vigo/server/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage AES-256-GCM encrypted secrets resolved by the secret: prefix in configs",
	Long:  "Manage secrets stored as AES-256-GCM encrypted files. These are the values resolved when configs use the secret: prefix.",
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <type> <key>",
	Short: "Encrypt and store a secret value",
	Long: `Encrypt and store a secret value. The type determines how the value is
read from the operator:

  raw       Arbitrary value (API keys, DSNs, certificates). Can be supplied via
            --from-file, stdin pipe, or interactive prompt. No complexity check.
  password  Human-typed credential. Interactive prompt only, complexity-enforced.
            Stored verbatim — consumers (web auth, the agent's user: executor)
            apply their own hashing at point of use.

Both types store the value in plain form inside the master-key AES-256-GCM
envelope. The unlock gate is the operator-side boundary.`,
	Args: cobra.ExactArgs(2),
	RunE: runSecretsSet,
}

var secretsGetCmd = &cobra.Command{
	Use:   "reveal <key>",
	Short: "Decrypt and print a secret value to stdout",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretsGet,
}

var secretsDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete an encrypted secret file from the secrets directory",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretsDelete,
}

var secretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secret paths available for use with the secret: prefix",
	RunE:  runSecretsList,
}

var secretsInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a new AES-256 master key for encrypting secrets at rest",
	RunE:  runSecretsInit,
}

var secretsGenerateCertsCmd = &cobra.Command{
	Use:   "generate-certs",
	Short: "Generate self-signed CA and server TLS certificates for mTLS bootstrap",
	Long: `Generate a self-signed CA keypair and server certificate as plain PEM files:

  tls/ca       CA certificate (PEM)
  tls/ca_key   CA private key (PEM)
  tls/cert     Server certificate (PEM)
  tls/key      Server private key (PEM)

Uses ECDSA P-256 keys. The server certificate includes SANs for
localhost, the system hostname, and any additional names specified
with --san. Refuses to overwrite existing certs unless --force is set.`,
	RunE: runSecretsGenerateCerts,
}


// promptPassword reads a password interactively with confirmation.
// Enforces HIPAA-grade complexity: 12+ chars, uppercase, lowercase, digit, special.
func promptPassword() ([]byte, error) {
	fmt.Fprint(os.Stderr, "Enter password: ")
	pw1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}
	if err := validatePasswordComplexity(string(pw1)); err != nil {
		return nil, err
	}

	fmt.Fprint(os.Stderr, "Confirm password: ")
	pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}
	if string(pw1) != string(pw2) {
		return nil, fmt.Errorf("passwords do not match")
	}

	return pw1, nil
}

// validatePasswordComplexity checks password meets HIPAA complexity requirements:
// minimum 12 characters, at least one uppercase, one lowercase, one digit, one special character.
func validatePasswordComplexity(pw string) error {
	if len(pw) < 12 {
		return fmt.Errorf("password must be at least 12 characters (got %d)", len(pw))
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, c := range pw {
		switch {
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= '0' && c <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	var missing []string
	if !hasUpper {
		missing = append(missing, "uppercase letter")
	}
	if !hasLower {
		missing = append(missing, "lowercase letter")
	}
	if !hasDigit {
		missing = append(missing, "digit")
	}
	if !hasSpecial {
		missing = append(missing, "special character")
	}
	if len(missing) > 0 {
		return fmt.Errorf("password must contain at least one %s", strings.Join(missing, ", "))
	}
	return nil
}

func init() {
	secretsCmd.PersistentFlags().String("key-file", "", "Path to master encryption key (omit to auto-generate)")
	secretsCmd.PersistentFlags().String("secrets-dir", "/srv/vigo/secrets", "Directory for secret files")
	secretsCmd.PersistentPreRunE = requireUnlocked

	secretsSetCmd.Flags().String("from-file", "", "Read secret value from a file")

	secretsInitCmd.Flags().String("key-file", "", "Path to write the new master key")
	secretsInitCmd.MarkFlagRequired("key-file")

	secretsGenerateCertsCmd.Flags().String("tls-dir", "/srv/vigo/tls", "Directory for TLS certificate files")
	secretsGenerateCertsCmd.Flags().StringSlice("san", nil, "Additional SANs (DNS names or IPs)")
	secretsGenerateCertsCmd.Flags().Int("ca-days", 3650, "CA certificate validity in days")
	secretsGenerateCertsCmd.Flags().Int("server-days", 365, "Server certificate validity in days")
	secretsGenerateCertsCmd.Flags().Bool("force", false, "Overwrite existing certificates")

	secretsCmd.AddCommand(secretsSetCmd)
	secretsCmd.AddCommand(secretsGetCmd)
	secretsCmd.AddCommand(secretsDeleteCmd)
	secretsCmd.AddCommand(secretsListCmd)
	secretsCmd.AddCommand(secretsInitCmd)
	secretsCmd.AddCommand(secretsGenerateCertsCmd)
}

func newLocalProvider(cmd *cobra.Command) (*secrets.LocalProvider, error) {
	keyFile, _ := cmd.Flags().GetString("key-file")
	secretsDir, _ := cmd.Flags().GetString("secrets-dir")
	return secrets.NewLocalProvider(keyFile, secretsDir)
}

// readSecretValue determines the secret value from flags, stdin, or interactive prompt.
func readSecretValue(cmd *cobra.Command) (string, error) {
	// Priority 1: --from-file flag.
	fromFile, _ := cmd.Flags().GetString("from-file")
	if fromFile != "" {
		data, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("reading secret from file: %w", err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	}

	// Priority 2: Stdin is a pipe (not a terminal).
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	}

	// Priority 3: Interactive prompt with longdrawer disabled.
	fmt.Fprint(os.Stderr, "Enter secret value: ")
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading secret: %w", err)
	}
	if len(value) == 0 {
		return "", fmt.Errorf("empty secret value")
	}
	return string(value), nil
}

func runSecretsSet(cmd *cobra.Command, args []string) error {
	secretType := args[0]
	key := args[1]

	switch secretType {
	case "raw":
		return setRawSecret(cmd, key)
	case "password":
		return setPasswordSecret(cmd, key)
	default:
		return fmt.Errorf("unknown secret type %q — must be raw or password", secretType)
	}
}

func setRawSecret(cmd *cobra.Command, key string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	value, err := readSecretValue(cmd)
	if err != nil {
		return err
	}

	if err := provider.Set(key, value); err != nil {
		return err
	}
	fmt.Printf("  Secret %q stored.\n", key)

	// Notify the server so watch_secret resources trigger on next check-in.
	// Surface failures: secret is stored locally, but envoys won't see the
	// new value until the secret watcher's next poll (default 5 min) unless
	// this notification lands.
	client := newAPIClient()
	if err := client.post("/api/v1/secrets/rotated", map[string]any{"path": key}, nil); err != nil {
		return fmt.Errorf("server notification failed (secret stored locally; envoys will pick it up at the next watcher poll, ~5 min): %w", err)
	}
	fmt.Println("  Server notified — affected envoys will apply on next check-in.")

	return nil
}

// setPasswordSecret prompts for a human-typed credential with complexity
// enforcement, stores it verbatim under the master-key envelope, and notifies
// the server so watch_secret resources trigger on the next check-in. Replaces
// the old set os / set web verbs — consumers (the agent's user: executor and
// server/auth/basic.go) handle their own hashing or comparison at point of use.
func setPasswordSecret(cmd *cobra.Command, key string) error {
	pw, err := promptPassword()
	if err != nil {
		return err
	}

	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	if err := provider.Set(key, string(pw)); err != nil {
		return fmt.Errorf("storing password: %w", err)
	}
	fmt.Printf("  Password stored at %s.\n", key)

	// Notify the server so affected envoys converge on the new value.
	// Surface failures: see setRawSecret for the rationale.
	client := newAPIClient()
	if err := client.post("/api/v1/secrets/rotated", map[string]any{"path": key}, nil); err != nil {
		return fmt.Errorf("server notification failed (password stored locally; envoys will pick it up at the next watcher poll, ~5 min): %w", err)
	}
	fmt.Println("  Server notified — affected envoys will apply on next check-in.")

	return nil
}

func runSecretsGet(cmd *cobra.Command, args []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	key := args[0]
	val, err := provider.Get(cmd.Context(), key)
	if err != nil {
		return err
	}
	fmt.Println(val)

	// Record secret access in the audit trail. Best-effort: the operator has
	// already received the value, so failure here is a logged warning rather
	// than a command error — but it must not be silent (would leave a hole in
	// the audit chain that nobody sees).
	client := newAPIClient()
	if err := client.post("/api/v1/secrets/accessed", map[string]any{"path": key}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: audit recording failed: %v\n", err)
	}

	return nil
}

func runSecretsDelete(cmd *cobra.Command, args []string) error {
	if !confirmAction(fmt.Sprintf("Delete secret %q? This cannot be undone.", args[0])) {
		fmt.Println("Cancelled.")
		return nil
	}

	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	if err := provider.Delete(args[0]); err != nil {
		return err
	}
	fmt.Printf("  Secret %q deleted.\n", args[0])
	return nil
}

func runSecretsList(cmd *cobra.Command, _ []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	keys, err := provider.List()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		fmt.Println("  No secrets stored.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\n", header("KEY"))
	for _, k := range keys {
		fmt.Fprintf(w, "  %s\n", k)
	}
	w.Flush()
	return nil
}

func runSecretsInit(cmd *cobra.Command, _ []string) error {
	keyFile, _ := cmd.Flags().GetString("key-file")

	masterKeyExists := false
	if _, err := os.Stat(keyFile); err == nil {
		masterKeyExists = true
	}

	// Both halves of bootstrap already present — operator wants `rotate`, not `init`.
	if masterKeyExists && verifierExists() {
		return fmt.Errorf("vault already initialized (master key %s and verifier %s both exist) — use `vigocli secrets rotate` to change the passphrase", keyFile, verifierPath())
	}

	if !masterKeyExists {
		// Generate 32 random bytes.
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return fmt.Errorf("generating key: %w", err)
		}

		// Create parent directory if needed.
		if dir := filepath.Dir(keyFile); dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("creating directory: %w", err)
			}
		}

		// Write as hex (64 characters).
		hexKey := fmt.Sprintf("%x\n", key)
		if err := os.WriteFile(keyFile, []byte(hexKey), 0o600); err != nil {
			return fmt.Errorf("writing key file: %w", err)
		}

		fmt.Printf("  Master key written to %s (mode 0600).\n", keyFile)
		fmt.Println("  Keep this file safe — losing it means losing access to all encrypted secrets.")
	} else {
		fmt.Printf("  Master key already present at %s — initializing unlock verifier only.\n", keyFile)
	}

	return initVerifierPrompt()
}

func runSecretsGenerateCerts(cmd *cobra.Command, _ []string) error {
	tlsDir, _ := cmd.Flags().GetString("tls-dir")
	if tlsDir == "" {
		tlsDir = "/srv/vigo/tls"
	}

	force, _ := cmd.Flags().GetBool("force")
	caDays, _ := cmd.Flags().GetInt("ca-days")
	serverDays, _ := cmd.Flags().GetInt("server-days")
	extraSANs, _ := cmd.Flags().GetStringSlice("san")

	// Check for existing cert files.
	caPath, caKeyPath, certPath, keyPath := secrets.TLSCertPaths(tlsDir)
	if !force {
		for _, f := range []string{caPath, caKeyPath, certPath, keyPath} {
			if _, err := os.Stat(f); err == nil {
				return fmt.Errorf("cert file %q already exists — use --force to overwrite", f)
			}
		}
	}

	// Build SAN list: always include localhost + hostname.
	dnsNames := []string{"localhost"}
	var ips []net.IP
	ips = append(ips, net.IPv4(127, 0, 0, 1))

	if hostname, err := os.Hostname(); err == nil && hostname != "localhost" {
		dnsNames = append(dnsNames, hostname)
	}

	for _, san := range extraSANs {
		if ip := net.ParseIP(san); ip != nil {
			ips = append(ips, ip)
		} else {
			dnsNames = append(dnsNames, san)
		}
	}

	// Generate CA keypair.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating CA key: %w", err)
	}

	caSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject:      pkix.Name{CommonName: "Vigo CA"},
		NotBefore:    now,
		NotAfter:     now.AddDate(0, 0, caDays),
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:         true,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("creating CA certificate: %w", err)
	}
	caCert, _ := x509.ParseCertificate(caCertDER)

	// Generate server keypair.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating server key: %w", err)
	}

	serverSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		NotBefore:    now,
		NotAfter:     now.AddDate(0, 0, serverDays),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("creating server certificate: %w", err)
	}

	// Encode to PEM.
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	caKeyDER, _ := x509.MarshalECPrivateKey(caKey)
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})
	serverKeyDER, _ := x509.MarshalECPrivateKey(serverKey)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	// Write PEM files to the TLS directory.
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
		return fmt.Errorf("creating TLS directory: %w", err)
	}

	files := []struct {
		path  string
		data  []byte
		mode  os.FileMode
		label string
	}{
		{caPath, caCertPEM, 0644, "CA certificate"},
		{caKeyPath, caKeyPEM, 0600, "CA private key"},
		{certPath, serverCertPEM, 0644, "Server certificate"},
		{keyPath, serverKeyPEM, 0600, "Server private key"},
	}

	for _, f := range files {
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.label, err)
		}
		fmt.Printf("  %-20s → %s\n", f.label, f.path)
	}

	fmt.Printf("\n  CA valid for %d days, server cert valid for %d days.\n", caDays, serverDays)
	fmt.Printf("  SANs: %s\n", strings.Join(dnsNames, ", "))
	if len(ips) > 0 {
		ipStrs := make([]string, len(ips))
		for i, ip := range ips {
			ipStrs[i] = ip.String()
		}
		fmt.Printf("  IPs:  %s\n", strings.Join(ipStrs, ", "))
	}
	return nil
}

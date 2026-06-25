// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/argon2"
	"golang.org/x/term"
)

// Argon2id parameters for the unlock verifier. Match the wrap pattern in
// server/backup/snapshot.go so the Go codebase has one set of password-KDF
// constants. Memory/time match the OWASP 2024 recommendation also used by
// the Rust puddle helper (agent/src/swarm/lockbox/crypt.rs).
const (
	verifierArgon2Time    = 3
	verifierArgon2Memory  = 64 * 1024
	verifierArgon2Threads = 4
	verifierArgon2KeyLen  = 32
	verifierSaltLen       = 16
)

// idleTTL is the sliding-window unlock lifetime. Bumped on every successful
// gated call. Matches the puddle helper's DEFAULT_IDLE_MINUTES.
const idleTTL = 24 * time.Hour

// defaultVerifierPath and defaultSentinelPath are the canonical locations on
// the vigosrv host. Overridable via env vars for tests.
const (
	defaultVerifierPath = "/srv/vigo/secrets-verifier.json"
	defaultSentinelPath = "/srv/vigo/.secrets-unlocked"
)

// gateExemptVerbs are the subcommands of `vigocli secrets` that do NOT require
// the vault to be unlocked. Bootstrap and lifecycle verbs only — every verb
// that touches secret values requires unlock.
var gateExemptVerbs = map[string]bool{
	"init":           true,
	"generate-certs": true,
	"unlock":         true,
	"lock":           true,
	"rotate":         true,
	"reset":          true,
	"secrets":        true, // help / no subcommand
}

func verifierPath() string {
	if p := os.Getenv("VIGO_VERIFIER_PATH"); p != "" {
		return p
	}
	return defaultVerifierPath
}

func sentinelPath() string {
	if p := os.Getenv("VIGO_SENTINEL_PATH"); p != "" {
		return p
	}
	return defaultSentinelPath
}

// verifier is the on-disk envelope at verifierPath(). The hash is
// Argon2id(passphrase, salt) under the constants above — never the passphrase
// itself. Compare with subtle.ConstantTimeCompare.
type verifier struct {
	Version int    `json:"version"`
	Salt    string `json:"salt"` // base64 std
	Hash    string `json:"hash"` // base64 std
}

func deriveVerifier(passphrase string, salt []byte) []byte {
	return argon2.IDKey(
		[]byte(passphrase),
		salt,
		verifierArgon2Time,
		verifierArgon2Memory,
		verifierArgon2Threads,
		verifierArgon2KeyLen,
	)
}

// writeVerifier derives a fresh Argon2id hash and writes the verifier atomically.
// Used by init (first-run) and rotate/reset (replace existing).
func writeVerifier(passphrase string) error {
	salt := make([]byte, verifierSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}
	hash := deriveVerifier(passphrase, salt)

	v := verifier{
		Version: 1,
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Hash:    base64.StdEncoding.EncodeToString(hash),
	}
	data, err := json.MarshalIndent(&v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding verifier: %w", err)
	}
	data = append(data, '\n')

	p := verifierPath()
	if dir := filepath.Dir(p); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating verifier dir: %w", err)
		}
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing verifier: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("installing verifier: %w", err)
	}
	return nil
}

func readVerifier() (*verifier, error) {
	data, err := os.ReadFile(verifierPath())
	if err != nil {
		return nil, err
	}
	var v verifier
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parsing verifier: %w", err)
	}
	if v.Version != 1 {
		return nil, fmt.Errorf("unsupported verifier version %d", v.Version)
	}
	return &v, nil
}

// vaultNotInitializedError is the canonical error returned by callers
// that need to gate an operation on an initialized vault. The message
// names the exact next command an operator should run.
func vaultNotInitializedError() error {
	return fmt.Errorf(`secrets vault not initialized — run "sudo vigocli secrets init" to initialize it`)
}

// verifierExists returns true iff a readable verifier file is present.
func verifierExists() bool {
	_, err := os.Stat(verifierPath())
	return err == nil
}

// verifyPassphrase returns nil iff the passphrase matches the stored verifier.
// Constant-time compare. The verifier must exist; callers should check
// verifierExists() if they need a different error for "no passphrase set yet".
func verifyPassphrase(passphrase string) error {
	v, err := readVerifier()
	if err != nil {
		return err
	}
	salt, err := base64.StdEncoding.DecodeString(v.Salt)
	if err != nil {
		return fmt.Errorf("decoding salt: %w", err)
	}
	storedHash, err := base64.StdEncoding.DecodeString(v.Hash)
	if err != nil {
		return fmt.Errorf("decoding stored hash: %w", err)
	}
	derived := deriveVerifier(passphrase, salt)
	if subtle.ConstantTimeCompare(derived, storedHash) != 1 {
		return errors.New("incorrect passphrase")
	}
	return nil
}

// writeSentinel writes the sliding-window expiry timestamp atomically.
func writeSentinel(expiresAt time.Time) error {
	p := sentinelPath()
	if dir := filepath.Dir(p); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating sentinel dir: %w", err)
		}
	}
	tmp := p + ".tmp"
	body := strconv.FormatInt(expiresAt.UnixMilli(), 10) + "\n"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return fmt.Errorf("writing sentinel: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("installing sentinel: %w", err)
	}
	return nil
}

// readSentinelExpiry returns the absolute expiry time recorded in the sentinel,
// or an error if the file is missing or unparseable.
func readSentinelExpiry() (time.Time, error) {
	data, err := os.ReadFile(sentinelPath())
	if err != nil {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing sentinel timestamp: %w", err)
	}
	return time.UnixMilli(ms), nil
}

// isUnlocked is the gate predicate: a present sentinel with an unexpired timestamp.
// Returns false on any error (missing, corrupted, expired).
func isUnlocked() bool {
	exp, err := readSentinelExpiry()
	if err != nil {
		return false
	}
	return time.Now().Before(exp)
}

// bumpSentinel rewrites the sentinel with a fresh idleTTL-from-now expiry.
// Best-effort: failure to bump doesn't fail the gated call (the operator is
// already authenticated for this session; the next call will re-check).
func bumpSentinel() {
	_ = writeSentinel(time.Now().Add(idleTTL))
}

// removeSentinel deletes the unlock sentinel. No error if absent.
func removeSentinel() error {
	if err := os.Remove(sentinelPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// requireUnlocked is wired to secretsCmd.PersistentPreRunE. Lets exempt verbs
// (init/generate-certs/unlock/lock/rotate/reset) through unconditionally;
// every other verb requires a present, unexpired sentinel and bumps the
// expiry on success. SilenceUsage is set up-front so a gate refusal prints
// only the diagnostic — cobra's default would otherwise dump the per-verb
// usage banner because the root's PersistentPreRun (which silences usage)
// never runs when PersistentPreRunE returns an error.
func requireUnlocked(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	if gateExemptVerbs[cmd.Name()] {
		return nil
	}
	if !verifierExists() {
		return vaultNotInitializedError()
	}
	if !isUnlocked() {
		return fmt.Errorf("secrets vault is locked — run `vigocli secrets unlock`")
	}
	bumpSentinel()
	return nil
}

// promptPassphrase reads a passphrase interactively. Confirms on first-set
// paths (init, rotate new, reset new). Single-prompt for unlock and rotate old.
func promptPassphrase(label string, confirm bool) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	pw1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	if !confirm {
		if len(pw1) == 0 {
			return "", errors.New("empty passphrase")
		}
		return string(pw1), nil
	}
	if err := validatePasswordComplexity(string(pw1)); err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Confirm passphrase: ")
	pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	if string(pw1) != string(pw2) {
		return "", errors.New("passphrases do not match")
	}
	return string(pw1), nil
}

// recordSecretsEvent posts a fire-and-forget audit event. Errors swallowed —
// matches the existing pattern in cli/secrets.go for `secret_rotated` /
// `secret_accessed` notifications.
func recordSecretsEvent(eventType string, payload map[string]any) {
	body := map[string]any{"type": eventType}
	for k, v := range payload {
		body[k] = v
	}
	_ = newAPIClient().post("/api/v1/secrets/event", body, nil)
}

// initVerifierPrompt is called from runSecretsInit after the master key is
// written. Prompts for the initial passphrase and writes the verifier. Safe to
// re-run if the master key was written by an older version of vigocli: when
// the master key exists but no verifier is present, the operator runs `init`
// again to set the unlock passphrase without overwriting the key.
func initVerifierPrompt() error {
	if verifierExists() {
		return errors.New("unlock verifier already exists; use `vigocli secrets rotate` to change the passphrase")
	}
	fmt.Println()
	fmt.Println("  Set the unlock passphrase for this vault.")
	fmt.Println("  This passphrase gates every `vigocli secrets` command except init/generate-certs/unlock/lock/rotate/reset.")
	pass, err := promptPassphrase("Enter unlock passphrase", true)
	if err != nil {
		return err
	}
	if err := writeVerifier(pass); err != nil {
		return err
	}
	fmt.Printf("  Unlock verifier written to %s.\n", verifierPath())
	fmt.Println("  Run `vigocli secrets unlock` to open the vault.")
	return nil
}

// ---------- cobra commands ----------

var secretsUnlockCmd = &cobra.Command{
	Use:   "unlock",
	Short: "Unlock the secrets vault for this operator session",
	Long: `Unlocks the secrets vault so the gated verbs (set, reveal, delete, list)
become available. The unlock state persists for 24h of inactivity; every
successful gated command extends the window. Use 'lock' to end the session
explicitly.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !verifierExists() {
			return errors.New("secrets vault not initialized — run `vigocli secrets init` first")
		}
		pass, err := promptPassphrase("Enter unlock passphrase", false)
		if err != nil {
			return err
		}
		if err := verifyPassphrase(pass); err != nil {
			return err
		}
		if err := writeSentinel(time.Now().Add(idleTTL)); err != nil {
			return fmt.Errorf("writing unlock sentinel: %w", err)
		}
		fmt.Printf("  Vault unlocked. Idle timeout: %s.\n", idleTTL)
		recordSecretsEvent("secrets.unlocked", nil)
		return nil
	},
}

var secretsLockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Lock the secrets vault — gated verbs will refuse until unlocked again",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := removeSentinel(); err != nil {
			return fmt.Errorf("removing unlock sentinel: %w", err)
		}
		fmt.Println("  Vault locked.")
		recordSecretsEvent("secrets.locked", nil)
		return nil
	},
}

var secretsRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate the unlock passphrase — requires the current passphrase",
	Long: `Replaces the unlock passphrase. Prompts for the current passphrase, then
the new one. Any existing unlock sentinels are cleared — every operator must
re-unlock with the new passphrase.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !verifierExists() {
			return errors.New("secrets vault not initialized — run `vigocli secrets init` first")
		}
		oldPass, err := promptPassphrase("Enter current passphrase", false)
		if err != nil {
			return err
		}
		if err := verifyPassphrase(oldPass); err != nil {
			return err
		}
		newPass, err := promptPassphrase("Enter new passphrase", true)
		if err != nil {
			return err
		}
		if err := writeVerifier(newPass); err != nil {
			return fmt.Errorf("writing new verifier: %w", err)
		}
		if err := removeSentinel(); err != nil {
			return fmt.Errorf("clearing unlock sentinel: %w", err)
		}
		fmt.Println("  Passphrase rotated. Run `vigocli secrets unlock` with the new passphrase.")
		recordSecretsEvent("secrets.passphrase_rotated", nil)
		return nil
	},
}

var secretsResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Break-glass: replace the unlock passphrase by proving you can read the master key",
	Long: `Replaces the unlock passphrase without knowing the current one. Requires
read access to the master key file (the same access level needed to decrypt
secrets anyway). Typed confirmation required to avoid accidents.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		keyFile, _ := cmd.Flags().GetString("key-file")
		secretsDir, _ := cmd.Flags().GetString("secrets-dir")
		if keyFile == "" {
			keyFile = filepath.Join(secretsDir, ".master.key")
		}
		if _, err := os.ReadFile(keyFile); err != nil {
			return fmt.Errorf("cannot prove master-key access (read %s): %w", keyFile, err)
		}

		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("reset-passphrase requires an interactive terminal to type the RESET confirmation")
		}
		fmt.Println("  This will replace the unlock passphrase without verifying the old one.")
		fmt.Print("  Type RESET to proceed: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) != "RESET" {
			fmt.Println("  Cancelled.")
			return nil
		}

		newPass, err := promptPassphrase("Enter new passphrase", true)
		if err != nil {
			return err
		}
		if err := writeVerifier(newPass); err != nil {
			return fmt.Errorf("writing new verifier: %w", err)
		}
		if err := removeSentinel(); err != nil {
			return fmt.Errorf("clearing unlock sentinel: %w", err)
		}
		fmt.Println("  Passphrase reset. Run `vigocli secrets unlock` with the new passphrase.")
		recordSecretsEvent("secrets.passphrase_reset", nil)
		return nil
	},
}

func init() {
	secretsCmd.AddCommand(secretsUnlockCmd)
	secretsCmd.AddCommand(secretsLockCmd)
	secretsCmd.AddCommand(secretsRotateCmd)
	secretsCmd.AddCommand(secretsResetCmd)
}

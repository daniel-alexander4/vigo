// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package hardening provides binary integrity verification and anti-tamper
// checks for the vigosrv server binary.
package hardening

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
)

// serverSigningPubKey is the ED25519 public key (hex) the running binary's
// detached signature must verify against. It is baked in at build time via
//
//	go build -ldflags "-X git.alexander4.io/vigo/server/hardening.serverSigningPubKey=<hex>"
//
// mirroring how the Rust agent bakes in VIGO_AGENT_SIGNING_PUBKEY. When SET,
// the .sig file's second line (the pubkey it carries) is ignored entirely and
// the signature is verified against THIS key — so a tampered binary re-signed
// with an attacker key (and an attacker pubkey written into the .sig) is
// rejected. When UNSET (the common dev build), we fall back to trusting the
// pubkey embedded in the .sig: that detects corruption but not tampering, and
// we log that anti-tamper is in corruption-detection mode.
//
// BUILD-WIRING TODO: the Makefile derives the agent pubkey from SIGN_KEY
// (VIGO_AGENT_SIGNING_PUBKEY) but the vigosrv `build`/`dist` targets do not yet
// inject this -X flag. To enable tamper-evident mode, add to the vigosrv
// go-build ldflags:
//
//	-X git.alexander4.io/vigo/server/hardening.serverSigningPubKey=$(VIGO_AGENT_SIGNING_PUBKEY)
//
// (reuse the same signing key the agent uses, or a server-specific one).
var serverSigningPubKey = ""

// VerifyBinarySignature checks a detached ED25519 signature file (.sig) against
// the running binary. The .sig file must be adjacent to the binary and contain
// a hex-encoded signature on the first line, followed by the hex-encoded public
// key on the second line.
//
// If serverSigningPubKey is baked in at build time, the signature is verified
// against that key (true anti-tamper — the .sig's own pubkey line is ignored).
// Otherwise it verifies against the pubkey carried in the .sig (corruption
// detection only), logging that downgrade.
//
// Returns nil if no .sig file exists (signature verification is optional).
// Returns an error if the .sig file exists but verification fails.
func VerifyBinarySignature() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("hardening: cannot determine executable path: %w", err)
	}

	sigPath := exe + ".sig"
	sigData, err := os.ReadFile(sigPath)
	if os.IsNotExist(err) {
		// No signature file. When a signing pubkey is baked in (a signed
		// production/dist build), a missing .sig means the binary was deployed
		// without its signature — a tamper / incomplete-deploy signal — so fail
		// closed. In an unsigned dev build (no baked pubkey) a missing .sig is
		// expected, so verification stays optional.
		if serverSigningPubKey != "" {
			return fmt.Errorf("hardening: signed build (baked-in pubkey present) but signature file %s is missing — refusing to start", sigPath)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("hardening: cannot read signature file %s: %w", sigPath, err)
	}

	lines := strings.SplitN(strings.TrimSpace(string(sigData)), "\n", 3)
	if len(lines) < 2 {
		return fmt.Errorf("hardening: signature file must contain signature and public key (hex, one per line)")
	}

	sig, err := hex.DecodeString(strings.TrimSpace(lines[0]))
	if err != nil {
		return fmt.Errorf("hardening: invalid signature hex: %w", err)
	}

	// Select the verifying key. When a pubkey is baked in at build time, use
	// it and ignore the .sig's own pubkey line — that's the difference between
	// tamper-evidence and mere corruption detection. Otherwise trust the .sig.
	pubKeyHex := strings.TrimSpace(lines[1])
	if serverSigningPubKey != "" {
		pubKeyHex = serverSigningPubKey
	} else {
		slog.Warn("hardening: no baked-in signing pubkey — anti-tamper is in corruption-detection mode only (a re-signed binary would pass); see serverSigningPubKey build wiring")
	}

	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return fmt.Errorf("hardening: invalid public key hex: %w", err)
	}

	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("hardening: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pubKeyBytes))
	}

	pubKey := ed25519.PublicKey(pubKeyBytes)

	// Hash the binary.
	f, err := os.Open(exe)
	if err != nil {
		return fmt.Errorf("hardening: cannot open own binary: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hardening: cannot hash binary: %w", err)
	}
	digest := h.Sum(nil)

	if !ed25519.Verify(pubKey, digest, sig) {
		return fmt.Errorf("hardening: binary signature verification FAILED — binary may have been tampered with")
	}

	slog.Info("binary signature verified", "binary", exe)
	return nil
}

// Finding describes a single attack indicator surfaced by a Detect* call.
// Each Detect* function returns a slice (possibly empty) for the caller
// to route through the audit writer + integrations dispatcher. The same
// findings are also emitted via slog.Warn so journald-only operators
// continue to see them without configuring SIEM forwarding.
type Finding struct {
	Kind    string            // "debugger" | "ld_preload" | "ld_library_path" | "suspicious_map"
	Summary string            // one-line human description
	Detail  map[string]string // structured fields for the audit payload
}

// DetectDebugger checks for signs of debugging or instrumentation on Linux.
// Returns findings for caller to route; also emits slog.Warn for journald.
// Does not block startup — defense in depth.
func DetectDebugger() []Finding {
	if runtime.GOOS != "linux" {
		return nil
	}

	// Check /proc/self/status for TracerPid.
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return nil
	}

	var findings []Finding
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TracerPid:") {
			pid := strings.TrimSpace(strings.TrimPrefix(line, "TracerPid:"))
			if pid != "0" {
				slog.Warn("SECURITY: debugger detected", "tracer_pid", pid)
				findings = append(findings, Finding{
					Kind:    "debugger",
					Summary: "debugger attached (TracerPid=" + pid + ")",
					Detail:  map[string]string{"tracer_pid": pid},
				})
			}
		}
	}
	return findings
}

// DetectInjection checks for LD_PRELOAD and other injection vectors on Linux.
// Returns findings for caller to route; also emits slog.Warn for journald.
// Does not block startup — defense in depth.
func DetectInjection() []Finding {
	if runtime.GOOS != "linux" {
		return nil
	}

	var findings []Finding
	for _, env := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH"} {
		if val := os.Getenv(env); val != "" {
			slog.Warn("SECURITY: injection vector detected", "env", env, "value", val)
			findings = append(findings, Finding{
				Kind:    strings.ToLower(env),
				Summary: env + " is set in the server environment",
				Detail:  map[string]string{"env": env, "value": val},
			})
		}
	}

	// Check /proc/self/maps for injected shared objects.
	data, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return findings
	}

	suspicious := []string{"inject", "hook", "preload", "frida", "xposed"}
	for _, line := range strings.Split(string(data), "\n") {
		lower := strings.ToLower(line)
		for _, pattern := range suspicious {
			if strings.Contains(lower, pattern) {
				mapping := strings.TrimSpace(line)
				slog.Warn("SECURITY: suspicious memory mapping", "line", mapping)
				findings = append(findings, Finding{
					Kind:    "suspicious_map",
					Summary: "suspicious memory mapping matches /" + pattern + "/",
					Detail:  map[string]string{"pattern": pattern, "mapping": mapping},
				})
			}
		}
	}
	return findings
}

// loadPrivateKey reads an ED25519 private key from disk in either PEM or
// raw-hex-seed format. Shared by SignBinary and PubkeyFromKey so the
// accepted formats stay in lock-step.
func loadPrivateKey(keyPath string) (ed25519.PrivateKey, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	block, _ := pem.Decode(keyData)
	if block != nil {
		if len(block.Bytes) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(block.Bytes), nil
		}
		if len(block.Bytes) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(block.Bytes), nil
		}
		return nil, fmt.Errorf("PEM key must be %d (seed) or %d (full) bytes, got %d",
			ed25519.SeedSize, ed25519.PrivateKeySize, len(block.Bytes))
	}

	seed, err := hex.DecodeString(strings.TrimSpace(string(keyData)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("key must be PEM-encoded or a %d-byte hex seed", ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PubkeyFromKey returns the hex-encoded ED25519 public key derived from
// the private key at keyPath. The key file may be PEM-encoded or a raw
// hex seed (same formats SignBinary accepts).
//
// Used by the agent build pipeline: the resulting pubkey is baked into
// the agent binary at build time as VIGO_AGENT_SIGNING_PUBKEY, so the
// agent verifies updates against a key it holds out-of-band rather than
// against a key the server hands it at update time.
func PubkeyFromKey(keyPath string) (string, error) {
	privKey, err := loadPrivateKey(keyPath)
	if err != nil {
		return "", err
	}
	pubKey := privKey.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pubKey), nil
}

// SignBinary creates a detached .sig file for a binary using an ED25519 private key.
// The key can be a raw 64-byte seed or a PEM-encoded PKCS8 key.
// This is a build-time utility, not called at runtime.
func SignBinary(binaryPath, keyPath, outputPath string) error {
	privKey, err := loadPrivateKey(keyPath)
	if err != nil {
		return err
	}

	// Hash the binary.
	f, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open binary: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash binary: %w", err)
	}
	digest := h.Sum(nil)

	// Sign.
	sig := ed25519.Sign(privKey, digest)
	pubKey := privKey.Public().(ed25519.PublicKey)

	// Write signature file: sig hex + newline + pubkey hex.
	content := fmt.Sprintf("%s\n%s\n", hex.EncodeToString(sig), hex.EncodeToString(pubKey))
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}

	return nil
}

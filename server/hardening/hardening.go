// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

// VerifyBinarySignature checks a detached ED25519 signature file (.sig) against
// the running binary. The .sig file must be adjacent to the binary and contain
// a hex-encoded signature on the first line, followed by the hex-encoded public
// key on the second line.
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
		// No signature file — verification is optional.
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

	pubKeyBytes, err := hex.DecodeString(strings.TrimSpace(lines[1]))
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

// DetectDebugger checks for signs of debugging or instrumentation on Linux.
// Logs a warning but does not block startup — this is defense in depth.
func DetectDebugger() {
	if runtime.GOOS != "linux" {
		return
	}

	// Check /proc/self/status for TracerPid.
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TracerPid:") {
			pid := strings.TrimSpace(strings.TrimPrefix(line, "TracerPid:"))
			if pid != "0" {
				slog.Warn("SECURITY: debugger detected", "tracer_pid", pid)
			}
		}
	}
}

// DetectInjection checks for LD_PRELOAD and other injection vectors on Linux.
// Logs a warning if detected.
func DetectInjection() {
	if runtime.GOOS != "linux" {
		return
	}

	for _, env := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH"} {
		if val := os.Getenv(env); val != "" {
			slog.Warn("SECURITY: injection vector detected", "env", env, "value", val)
		}
	}

	// Check /proc/self/maps for injected shared objects.
	data, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return
	}

	suspicious := []string{"inject", "hook", "preload", "frida", "xposed"}
	for _, line := range strings.Split(string(data), "\n") {
		lower := strings.ToLower(line)
		for _, pattern := range suspicious {
			if strings.Contains(lower, pattern) {
				slog.Warn("SECURITY: suspicious memory mapping", "line", strings.TrimSpace(line))
			}
		}
	}
}

// SignBinary creates a detached .sig file for a binary using an ED25519 private key.
// The key can be a raw 64-byte seed or a PEM-encoded PKCS8 key.
// This is a build-time utility, not called at runtime.
func SignBinary(binaryPath, keyPath, outputPath string) error {
	// Read private key.
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}

	var privKey ed25519.PrivateKey

	// Try PEM first.
	block, _ := pem.Decode(keyData)
	if block != nil {
		// Raw ED25519 seed in PEM.
		if len(block.Bytes) == ed25519.SeedSize {
			privKey = ed25519.NewKeyFromSeed(block.Bytes)
		} else if len(block.Bytes) == ed25519.PrivateKeySize {
			privKey = ed25519.PrivateKey(block.Bytes)
		} else {
			return fmt.Errorf("PEM key must be %d (seed) or %d (full) bytes, got %d",
				ed25519.SeedSize, ed25519.PrivateKeySize, len(block.Bytes))
		}
	} else {
		// Try raw hex seed.
		seed, err := hex.DecodeString(strings.TrimSpace(string(keyData)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return fmt.Errorf("key must be PEM-encoded or a %d-byte hex seed", ed25519.SeedSize)
		}
		privKey = ed25519.NewKeyFromSeed(seed)
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

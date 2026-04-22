// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Command sign creates a detached ED25519 signature file for a binary.
//
// Usage:
//
//	go run ./server/hardening/cmd/sign -key signing.key -binary bin/vigosrv
//	go run ./server/hardening/cmd/sign -genkey -output signing.key
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"git.alexander4.io/vigo/server/hardening"
)

func main() {
	var (
		keyPath    string
		binaryPath string
		outputPath string
		genKey     bool
	)

	flag.StringVar(&keyPath, "key", "signing.key", "path to ED25519 private key (hex seed)")
	flag.StringVar(&binaryPath, "binary", "", "path to binary to sign")
	flag.StringVar(&outputPath, "output", "", "output .sig file path (default: <binary>.sig)")
	flag.BoolVar(&genKey, "genkey", false, "generate a new ED25519 signing key")
	flag.Parse()

	if genKey {
		out := outputPath
		if out == "" {
			out = keyPath
		}
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			fmt.Fprintf(os.Stderr, "error generating key: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(out, []byte(hex.EncodeToString(seed)+"\n"), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "error writing key: %v\n", err)
			os.Exit(1)
		}
		pubKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		fmt.Printf("key written to %s\n", out)
		fmt.Printf("public key: %s\n", hex.EncodeToString(pubKey))
		return
	}

	if binaryPath == "" {
		fmt.Fprintln(os.Stderr, "error: -binary is required")
		flag.Usage()
		os.Exit(1)
	}

	if outputPath == "" {
		outputPath = binaryPath + ".sig"
	}

	if err := hardening.SignBinary(binaryPath, keyPath, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("signature written to %s\n", outputPath)
}

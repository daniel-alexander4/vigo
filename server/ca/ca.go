// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package ca implements a certificate authority that signs agent CSRs
// during bootstrap enrollment.
package ca

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

// ValidateHostname enforces the shape an enrolling envoy's hostname must have
// before it can become the CN/DNS SAN of a fleet-CA certificate: RFC 1123
// labels only (letters, digits, hyphen; 1-63 chars; no leading/trailing
// hyphen), at most 253 chars, no wildcard, no trailing dot, and not an IP
// literal. The cert carries ServerAuth (envoys serve the swarm content plane
// to each other over mTLS), so a wildcard or IP-shaped name would mint a
// fleet-trusted server identity broader than the one host that asked for it.
func ValidateHostname(name string) error {
	if name == "" {
		return fmt.Errorf("hostname is empty")
	}
	if len(name) > 253 {
		return fmt.Errorf("hostname exceeds 253 characters")
	}
	if net.ParseIP(name) != nil {
		return fmt.Errorf("hostname %q is an IP literal", name)
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return fmt.Errorf("hostname %q has an empty label", name)
		}
		if len(label) > 63 {
			return fmt.Errorf("hostname %q has a label longer than 63 characters", name)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("hostname %q has a label starting or ending with '-'", name)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("hostname %q contains invalid character %q", name, c)
			}
		}
	}
	return nil
}

// CA holds the certificate authority's cert and private key for signing CSRs.
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     any // crypto.PrivateKey (RSA, ECDSA, or Ed25519)
}

// Load reads a CA certificate and private key from PEM files on disk.
func Load(certFile, keyFile string) (*CA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in CA cert file")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("no PEM block found in CA key file")
	}

	key, err := parsePrivateKey(keyBlock)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}

	return &CA{cert: cert, certPEM: certPEM, key: key}, nil
}

// CACertPEM returns the CA certificate in PEM format.
func (ca *CA) CACertPEM() []byte {
	return ca.certPEM
}

// SignCSR validates and signs a PEM-encoded CSR, returning the signed
// certificate in PEM format. The certificate includes the hostname as
// Common Name and DNS SAN, with both ClientAuth and ServerAuth EKU
// (agents act as both gRPC clients and swarm peer servers — the swarm peer
// verifier in agent/src/swarm/tls.rs requires the server-auth EKU). The
// hostname must pass ValidateHostname; the bootstrap caller additionally
// refuses names the server's own TLS cert carries.
func (ca *CA) SignCSR(csrPEM []byte, hostname string, validity time.Duration) ([]byte, error) {
	if err := ValidateHostname(hostname); err != nil {
		return nil, err
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("invalid CSR PEM")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature invalid: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		DNSNames:              []string{hostname},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	}), nil
}

// parsePrivateKey tries PKCS8, PKCS1 (RSA), and EC formats.
func parsePrivateKey(block *pem.Block) (any, error) {
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("unsupported private key type %q", block.Type)
}

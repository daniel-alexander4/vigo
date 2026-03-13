// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// EnsureTLSCerts checks whether the four TLS secrets exist and generates
// self-signed certificates if any are missing. This gives every server
// install unique keys on first start with zero manual steps.
//
// Only called for the local backend — isopass/static manage certs externally.
func (p *LocalProvider) EnsureTLSCerts() error {
	keys := []string{"hobnob/tls/ca", "hobnob/tls/ca_key", "hobnob/tls/cert", "hobnob/tls/key"}
	for _, k := range keys {
		ver, _ := p.GetVersion(context.Background(), k)
		if ver == 0 {
			return p.generateSelfSignedCerts()
		}
	}
	return nil
}

func (p *LocalProvider) generateSelfSignedCerts() error {
	// SAN list: localhost + hostname + 127.0.0.1.
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	if hostname, err := os.Hostname(); err == nil && hostname != "localhost" {
		dnsNames = append(dnsNames, hostname)
	}

	// Generate CA.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating CA key: %w", err)
	}

	caSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Hobnob CA"},
		NotBefore:             now,
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("creating CA certificate: %w", err)
	}
	caCert, _ := x509.ParseCertificate(caCertDER)

	// Generate server cert signed by CA.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating server key: %w", err)
	}

	serverSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	serverTemplate := &x509.Certificate{
		SerialNumber:          serverSerial,
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		DNSNames:              dnsNames,
		IPAddresses:           ips,
		NotBefore:             now,
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("creating server certificate: %w", err)
	}

	// PEM encode.
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	caKeyDER, _ := x509.MarshalECPrivateKey(caKey)
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})
	serverKeyDER, _ := x509.MarshalECPrivateKey(serverKey)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	// Store encrypted.
	pairs := []struct{ key, val string }{
		{"hobnob/tls/ca", string(caCertPEM)},
		{"hobnob/tls/ca_key", string(caKeyPEM)},
		{"hobnob/tls/cert", string(serverCertPEM)},
		{"hobnob/tls/key", string(serverKeyPEM)},
	}
	for _, kv := range pairs {
		if err := p.Set(kv.key, kv.val); err != nil {
			return fmt.Errorf("storing %s: %w", kv.key, err)
		}
	}

	return nil
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
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
	"path/filepath"
	"time"
)

// TLS cert file names within the secrets directory (hobnob/tls/).
const (
	tlsDir     = "hobnob/tls"
	tlsCA      = "ca"
	tlsCAKey   = "ca_key"
	tlsCert    = "cert"
	tlsKey     = "key"
)

// TLSCertPaths returns the absolute paths for the four TLS files under dir.
func TLSCertPaths(secretsDir string) (ca, caKey, cert, key string) {
	d := filepath.Join(secretsDir, tlsDir)
	return filepath.Join(d, tlsCA),
		filepath.Join(d, tlsCAKey),
		filepath.Join(d, tlsCert),
		filepath.Join(d, tlsKey)
}

// EnsureTLSCerts checks whether the four TLS PEM files exist under the
// secrets directory and generates self-signed certificates if any are missing.
// Files are written as plain PEM (not encrypted) since they are loaded
// directly by the TLS stack via file paths in server.yaml.
//
// Only called for the local backend — isopass/static manage certs externally.
func (p *LocalProvider) EnsureTLSCerts() error {
	caPath, caKeyPath, certPath, keyPath := TLSCertPaths(p.secretsDir)
	for _, f := range []string{caPath, caKeyPath, certPath, keyPath} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
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

	// Write PEM files to secrets/hobnob/tls/.
	caPath, caKeyPath, certPath, keyPath := TLSCertPaths(p.secretsDir)
	if err := os.MkdirAll(filepath.Dir(caPath), 0700); err != nil {
		return fmt.Errorf("creating TLS directory: %w", err)
	}

	files := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{caPath, caCertPEM, 0644},
		{caKeyPath, caKeyPEM, 0600},
		{certPath, serverCertPEM, 0644},
		{keyPath, serverKeyPEM, 0600},
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.path, err)
		}
	}

	return nil
}

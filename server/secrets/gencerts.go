// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

// TLS cert file names within the TLS directory.
const (
	tlsCA    = "ca"
	tlsCAKey = "ca_key"
	tlsCert  = "cert"
	tlsKey   = "key"
)

// TLSCertPaths returns the absolute paths for the four TLS files under tlsDir.
func TLSCertPaths(tlsDir string) (ca, caKey, cert, key string) {
	return filepath.Join(tlsDir, tlsCA),
		filepath.Join(tlsDir, tlsCAKey),
		filepath.Join(tlsDir, tlsCert),
		filepath.Join(tlsDir, tlsKey)
}

// EnsureTLSCerts checks whether the four TLS PEM files exist under tlsDir
// and generates self-signed certificates if any are missing. extraSANs are
// additional DNS names or IP addresses to include (e.g., the host's LAN IP
// when running inside Docker). Files are written as plain PEM (not encrypted)
// since they are loaded directly by the TLS stack.
//
// Only called for the local backend — isopass/static manage certs externally.
func EnsureTLSCerts(tlsDir string, extraSANs ...string) error {
	caPath, caKeyPath, certPath, keyPath := TLSCertPaths(tlsDir)
	for _, f := range []string{caPath, caKeyPath, certPath, keyPath} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			return generateSelfSignedCerts(tlsDir, extraSANs)
		}
	}
	return nil
}

func generateSelfSignedCerts(tlsDir string, extraSANs []string) error {
	// SAN list: localhost + hostname + 127.0.0.1 + all non-loopback interface IPs.
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	if hostname, err := os.Hostname(); err == nil && hostname != "localhost" {
		dnsNames = append(dnsNames, hostname)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				ips = append(ips, ipNet.IP)
			}
		}
	}

	// Add extra SANs from config (e.g., host LAN IP when running in Docker).
	for _, san := range extraSANs {
		if ip := net.ParseIP(san); ip != nil {
			ips = append(ips, ip)
		} else {
			dnsNames = append(dnsNames, san)
		}
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

	// Write PEM files to the TLS directory.
	caPath, caKeyPath, certPath, keyPath := TLSCertPaths(tlsDir)
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
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

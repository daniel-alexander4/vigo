// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
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

// EnsureTLSCerts makes the on-disk TLS state match the expected SAN set.
//
//	First run (any file missing): generate a new CA + server cert.
//	Subsequent runs: if the on-disk server cert already covers every expected
//	SAN, no-op. Otherwise refresh just the server cert, signing it with the
//	existing CA so enrolled clients keep their trust chain.
//
// Expected SANs = localhost + hostname + non-loopback interface IPs + extraSANs.
// extraSANs comes from server.yaml `server.tls_sans:`. Only called for the
// local backend — isopass manages certs externally.
func EnsureTLSCerts(tlsDir string, extraSANs ...string) error {
	caPath, caKeyPath, certPath, keyPath := TLSCertPaths(tlsDir)
	dnsNames, ips := buildExpectedSANs(extraSANs)

	for _, f := range []string{caPath, caKeyPath, certPath, keyPath} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			if err := generateCAAndServerCert(tlsDir, dnsNames, ips); err != nil {
				return err
			}
			slog.Info("TLS certs generated", "dns", dnsNames, "ips", ipsToStrings(ips))
			return nil
		}
	}

	serverCert, err := readCert(certPath)
	if err != nil {
		return fmt.Errorf("reading server cert: %w", err)
	}
	if certCoversSANs(serverCert, dnsNames, ips) {
		return nil
	}

	caCert, caKey, err := loadCA(caPath, caKeyPath)
	if err != nil {
		return fmt.Errorf("loading CA: %w", err)
	}
	if err := writeServerCert(tlsDir, caCert, caKey, dnsNames, ips); err != nil {
		return err
	}
	slog.Info("TLS server cert refreshed for updated SANs",
		"dns", dnsNames, "ips", ipsToStrings(ips))
	return nil
}

// buildExpectedSANs returns the DNS names and IPs that the server cert should
// cover: localhost + hostname + non-loopback interface IPs + caller-supplied
// extras (typically server.yaml tls_sans).
func buildExpectedSANs(extraSANs []string) ([]string, []net.IP) {
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
	for _, san := range extraSANs {
		if ip := net.ParseIP(san); ip != nil {
			ips = append(ips, ip)
		} else {
			dnsNames = append(dnsNames, san)
		}
	}
	return dnsNames, ips
}

// certCoversSANs reports whether every expected DNS name and IP is already
// listed in the cert. Extra SANs on the cert are tolerated — only missing
// expected entries trigger a refresh.
func certCoversSANs(cert *x509.Certificate, expectedDNS []string, expectedIPs []net.IP) bool {
	have := make(map[string]struct{}, len(cert.DNSNames))
	for _, name := range cert.DNSNames {
		have[strings.ToLower(name)] = struct{}{}
	}
	for _, name := range expectedDNS {
		if _, ok := have[strings.ToLower(name)]; !ok {
			return false
		}
	}
	for _, want := range expectedIPs {
		matched := false
		for _, got := range cert.IPAddresses {
			if got.Equal(want) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// generateCAAndServerCert creates a new CA + server cert and writes all four
// PEM files. Used on first run (any of the four files missing).
func generateCAAndServerCert(tlsDir string, dnsNames []string, ips []net.IP) error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating CA key: %w", err)
	}

	caSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Vigo CA"},
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

	caPath, caKeyPath, _, _ := TLSCertPaths(tlsDir)
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
		return fmt.Errorf("creating TLS directory: %w", err)
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	caKeyDER, _ := x509.MarshalECPrivateKey(caKey)
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})
	if err := os.WriteFile(caPath, caCertPEM, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", caPath, err)
	}
	if err := os.WriteFile(caKeyPath, caKeyPEM, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", caKeyPath, err)
	}

	return writeServerCert(tlsDir, caCert, caKey, dnsNames, ips)
}

// writeServerCert signs a fresh server cert with the supplied CA and writes
// it (and its private key) to disk. Caller is responsible for ensuring tlsDir
// already exists with the CA files in place.
func writeServerCert(tlsDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, dnsNames []string, ips []net.IP) error {
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating server key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		DNSNames:              dnsNames,
		IPAddresses:           ips,
		NotBefore:             now,
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("creating server certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(serverKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	_, _, certPath, keyPath := TLSCertPaths(tlsDir)
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", keyPath, err)
	}
	return nil
}

// ExportCA reads this host's fleet CA certificate + private key PEM so an
// operator can establish a SHARED CA across spanner bolts (ADR-026 config-driven
// peering, option 3 — the CA is distributed out-of-band, never over the wire).
// The CA key is the fleet's crown jewel; callers must protect the output (the
// vigocli `spanner ca export` surface writes it 0600, root-only).
func ExportCA(tlsDir string) (caPEM, caKeyPEM []byte, err error) {
	caPath, caKeyPath, _, _ := TLSCertPaths(tlsDir)
	caPEM, err = os.ReadFile(caPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading CA cert: %w", err)
	}
	caKeyPEM, err = os.ReadFile(caKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading CA key: %w", err)
	}
	return caPEM, caKeyPEM, nil
}

// AdoptCA installs a CA (cert + key) exported from another bolt as THIS host's
// fleet CA, then re-signs this host's server cert from it so the cert chains to
// the shared CA with this host's own SANs (hostname + interface IPs + localhost
// + extraSANs). This is how a joining bolt adopts the founder's CA in the
// option-3 (CA-off-the-wire) flow: the operator runs `vigocli spanner ca import`
// with the exported bundle before the bolt joins. Idempotent. The server reads
// the new files on its next start (or restart after import).
func AdoptCA(tlsDir string, caPEM, caKeyPEM []byte, extraSANs ...string) error {
	if _, err := x509.ParseCertificate(mustDERFromPEM(caPEM)); err != nil {
		return fmt.Errorf("CA cert is not a valid certificate: %w", err)
	}
	caPath, caKeyPath, _, _ := TLSCertPaths(tlsDir)
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
		return fmt.Errorf("creating tls dir: %w", err)
	}
	if err := os.WriteFile(caPath, caPEM, 0644); err != nil {
		return fmt.Errorf("writing CA cert: %w", err)
	}
	if err := os.WriteFile(caKeyPath, caKeyPEM, 0600); err != nil {
		return fmt.Errorf("writing CA key: %w", err)
	}
	caCert, caKey, err := loadCA(caPath, caKeyPath)
	if err != nil {
		return fmt.Errorf("loading adopted CA: %w", err)
	}
	dnsNames, ips := buildExpectedSANs(extraSANs)
	if err := writeServerCert(tlsDir, caCert, caKey, dnsNames, ips); err != nil {
		return fmt.Errorf("re-signing server cert from adopted CA: %w", err)
	}
	return nil
}

// mustDERFromPEM returns the DER bytes of the first PEM block, or nil if the
// input isn't PEM — the caller's x509.ParseCertificate then reports the error.
func mustDERFromPEM(p []byte) []byte {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil
	}
	return block.Bytes
}

// loadCA parses the on-disk CA certificate and private key.
func loadCA(caPath, caKeyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", caPath, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no PEM block", caPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", caPath, err)
	}

	keyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", caKeyPath, err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("%s: no PEM block", caKeyPath)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", caKeyPath, err)
	}
	return cert, key, nil
}

// readCert parses a single x509 certificate from a PEM file.
func readCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func ipsToStrings(ips []net.IP) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}

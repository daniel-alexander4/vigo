// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
)

// testEnvoy holds the materials for one simulated agent: its enrollment
// ed25519 keypair (for the signed ReportResult), the issued mTLS
// cert/key pair, and the CA cert. Mirrors test/burst — these envoys
// never CheckIn; they go straight to the ReportResult storm, so the
// state_fingerprint field burst carries isn't needed here.
type testEnvoy struct {
	id         string
	hostname   string
	signingKey ed25519.PrivateKey
	certPEM    []byte
	keyPEM     []byte
	caCertPEM  []byte
}

// enroll registers one envoy via the bootstrap HTTP endpoint (trusted
// enrollment pattern; no token required). Sets up a fresh ed25519
// keypair + TLS client cert per envoy. The recorded pubkey is what the
// server verifies the ReportResult signature against.
func enroll(client *http.Client, apiAddr, hostname string) (testEnvoy, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return testEnvoy{}, fmt.Errorf("keygen: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return testEnvoy{}, fmt.Errorf("marshal pubkey: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	_, tlsPriv, _ := ed25519.GenerateKey(rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostname},
	}, tlsPriv)
	if err != nil {
		return testEnvoy{}, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	tlsKeyDER, _ := x509.MarshalPKCS8PrivateKey(tlsPriv)
	tlsKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: tlsKeyDER})

	body, _ := json.Marshal(map[string]string{
		"hostname":   hostname,
		"ip_address": "10.0.0.1",
		"pubkey_pem": string(pubPEM),
		"csr_pem":    string(csrPEM),
	})

	resp, err := client.Post(apiAddr+"/api/v1/bootstrap/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return testEnvoy{}, fmt.Errorf("POST register: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return testEnvoy{}, fmt.Errorf("register: %s — %s", resp.Status, string(respBody))
	}

	var reg struct {
		EnvoyID       string `json:"envoy_id"`
		SignedCertPEM string `json:"signed_cert_pem"`
		CACertPEM     string `json:"ca_cert_pem"`
	}
	if err := json.Unmarshal(respBody, &reg); err != nil {
		return testEnvoy{}, fmt.Errorf("decode: %w", err)
	}

	return testEnvoy{
		id:         reg.EnvoyID,
		hostname:   hostname,
		signingKey: priv,
		certPEM:    []byte(reg.SignedCertPEM),
		keyPEM:     tlsKeyPEM,
		caCertPEM:  []byte(reg.CACertPEM),
	}, nil
}

// newTLSConfigFor builds an mTLS client config from the envoy's cert.
func newTLSConfigFor(e *testEnvoy) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(e.certPEM, e.keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(e.caCertPEM)
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		InsecureSkipVerify: true, // server cert may be IP-based
	}, nil
}

// deleteEnvoy issues `DELETE /api/v1/envoys/{id}?skip_swarm_cleanup=true`.
// skip_swarm_cleanup avoids the N×fleet lockbox-recipient scrub fan-out
// that's pointless for bare bootstrap-enrolled simulated envoys.
func deleteEnvoy(client *http.Client, apiAddr, adminToken, envoyID string) error {
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/envoys/%s?skip_swarm_cleanup=true", apiAddr, envoyID), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete %s: %s — %s", envoyID, resp.Status, string(body))
	}
	return nil
}

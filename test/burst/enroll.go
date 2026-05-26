// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
// ed25519 keypair (for the signed CheckIn), the issued mTLS cert/key
// pair, the CA cert, and the last-known state_fingerprint the server
// returned. Echoing the fingerprint back on subsequent CheckIns is what
// makes steady-state cheap (server short-circuits to "no change");
// dropping the fingerprint is what triggers a bundle rebuild.
type testEnvoy struct {
	id               string
	hostname         string
	signingKey       ed25519.PrivateKey
	certPEM          []byte
	keyPEM           []byte
	caCertPEM        []byte
	stateFingerprint string // updated from server's CheckIn response; echoed back
}

// enroll registers one envoy via the bootstrap HTTP endpoint (trusted
// enrollment pattern; no token required). Mirrors the simple loadtest's
// enrollment path. Sets up a fresh ed25519 keypair + TLS client cert
// per envoy.
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
// Returns an error if cert/key parse fails — caller treats this as a
// per-envoy fatal (the envoy is discarded from the test).
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
		InsecureSkipVerify: true, // matches the simple loadtest; server cert may be IP-based
	}, nil
}

// triggerConfigReload POSTs /api/v1/config/reload on the server with
// the admin token. The server reloads .live/, increments its config
// version + secrets epoch, and the next CheckIn from every envoy will
// see a stale state_fingerprint → full bundle rebuild path. That's
// what creates the publish burst this test measures.
func triggerConfigReload(client *http.Client, apiAddr, adminToken string) (int, error) {
	req, _ := http.NewRequest("POST", apiAddr+"/api/v1/config/reload", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("reload returned %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// deleteEnvoy issues `DELETE /api/v1/envoys/{id}?skip_swarm_cleanup=true`.
// Matches the simple loadtest cleanup path (swarm-cleanup fan-out is
// pointless for bare bootstrap-enrolled simulated envoys with no
// usercrate state).
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

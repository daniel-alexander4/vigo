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
	"os"
	"os/exec"
)

// enrolled is the material a single synthetic envoy needs to act: its
// server-assigned id, the ed25519 key that signs every CheckIn /
// ReportResult, and the issued mTLS cert/key + CA. Mirrors test/burst's
// testEnvoy; persisted into the state manifest so a tick can resume the
// same identity without re-enrolling.
type enrolled struct {
	id         string
	signingKey ed25519.PrivateKey
	certPEM    []byte
	keyPEM     []byte
	caCertPEM  []byte
}

// enroll registers one envoy via the trusted bootstrap HTTP endpoint (no
// token; the server auto-signs from a private/loopback CIDR). Fresh
// ed25519 signing key + TLS client cert per envoy — identical to the
// load-test harnesses' enrollment path.
func enroll(client *http.Client, apiAddr, hostname, ip string) (enrolled, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return enrolled{}, fmt.Errorf("keygen: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return enrolled{}, fmt.Errorf("marshal pubkey: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	_, tlsPriv, _ := ed25519.GenerateKey(rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostname},
	}, tlsPriv)
	if err != nil {
		return enrolled{}, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	tlsKeyDER, _ := x509.MarshalPKCS8PrivateKey(tlsPriv)
	tlsKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: tlsKeyDER})

	body, _ := json.Marshal(map[string]string{
		"hostname":   hostname,
		"ip_address": ip,
		"pubkey_pem": string(pubPEM),
		"csr_pem":    string(csrPEM),
	})

	resp, err := client.Post(apiAddr+"/api/v1/bootstrap/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return enrolled{}, fmt.Errorf("POST register: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return enrolled{}, fmt.Errorf("register: %s — %s", resp.Status, string(respBody))
	}

	var reg struct {
		EnvoyID       string `json:"envoy_id"`
		SignedCertPEM string `json:"signed_cert_pem"`
		CACertPEM     string `json:"ca_cert_pem"`
	}
	if err := json.Unmarshal(respBody, &reg); err != nil {
		return enrolled{}, fmt.Errorf("decode: %w", err)
	}
	return enrolled{
		id:         reg.EnvoyID,
		signingKey: priv,
		certPEM:    []byte(reg.SignedCertPEM),
		keyPEM:     tlsKeyPEM,
		caCertPEM:  []byte(reg.CACertPEM),
	}, nil
}

// tlsConfigFor builds an mTLS client config from one envoy's issued cert.
func tlsConfigFor(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		InsecureSkipVerify: true, // server cert may be IP-based; matches the load harnesses
	}, nil
}

// signWith signs with the envoy's enrollment key. Both CheckIn and
// ReportResult sign `<payload> || <unix-seconds>` derived from one
// time instant, so the value can't straddle a second boundary.
func signWith(key ed25519.PrivateKey, msg []byte) []byte { return ed25519.Sign(key, msg) }

// publishConfig runs `vigocli config publish` — the canonical path that
// validates stacks/, syncs to .live/ (what the server actually reads),
// and triggers the reload. A bare /config/reload would NOT see new files
// because the server reads .live/, not stacks/. Shelling out to vigocli
// (which, on the server host, auto-uses the root-only local-admin token)
// keeps this on the exact operator path rather than re-implementing the
// publish/validate/sync machinery here.
func publishConfig(cfg config) error {
	cmd := exec.Command(cfg.vigocliBin, "config", "publish", "--skip-url-check", "--operator", "vigo")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s config publish: %w", cfg.vigocliBin, err)
	}
	return nil
}

// deleteEnvoy removes one envoy by id. skip_swarm_cleanup is safe — a
// synthetic envoy has no usercrate/swarm state to tear down.
func deleteEnvoy(client *http.Client, apiAddr, adminToken, id string) error {
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/envoys/%s?skip_swarm_cleanup=true", apiAddr, id), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete %s: %s — %s", id, resp.Status, string(body))
	}
	return nil
}

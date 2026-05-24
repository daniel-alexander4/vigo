// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// vigo-loadtest simulates N envoys checking in at a configurable interval.
// Measures server throughput, latency distribution, and error rate.
//
// Prerequisites:
//
//	Add a trusted enrollment pattern to server.yaml:
//	  trusted_enrollment:
//	    - pattern: "loadtest-*.test"
//	      cidr: "0.0.0.0/0"
//	Then: vigocli config publish && restart server
//
// Usage:
//
//	go run ./test/loadtest \
//	  --server 127.0.0.1:1530 \
//	  --api https://127.0.0.1:8443 \
//	  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
//	  --count 1000 \
//	  --interval 5s \
//	  --duration 5m
//
// --admin-token is the boot-minted local-admin credential at
// /srv/vigo/cli-admin-token on the server host; required for the
// post-test envoy revoke. Omit (or pass --skip-cleanup) to leave the
// simulated envoys in the DB.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type testEnvoy struct {
	id         string
	hostname   string
	signingKey ed25519.PrivateKey
	certPEM    []byte
	keyPEM     []byte
	caCertPEM  []byte
}

type latencyStats struct {
	success atomic.Int64
	fail    atomic.Int64
	mu      sync.Mutex
	latNs   []int64
}

func (s *latencyStats) record(d time.Duration, err error) {
	if err != nil {
		s.fail.Add(1)
		return
	}
	s.success.Add(1)
	s.mu.Lock()
	s.latNs = append(s.latNs, d.Nanoseconds())
	s.mu.Unlock()
}

func (s *latencyStats) snapshot() (ok, fail int64, lats []int64) {
	s.mu.Lock()
	lats = make([]int64, len(s.latNs))
	copy(lats, s.latNs)
	s.mu.Unlock()
	return s.success.Load(), s.fail.Load(), lats
}

func printStats(ok, fail int64, lats []int64, elapsed time.Duration) {
	total := ok + fail
	if len(lats) == 0 {
		fmt.Printf("  Total: %d  Success: %d  Failed: %d  (no latency data)\n", total, ok, fail)
		return
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p := func(pct float64) time.Duration {
		idx := int(math.Ceil(float64(len(lats))*pct/100)) - 1
		if idx < 0 {
			idx = 0
		}
		return time.Duration(lats[idx])
	}
	fmt.Printf("  Total: %d  Success: %d  Failed: %d\n", total, ok, fail)
	fmt.Printf("  Latency  p50=%v  p95=%v  p99=%v  max=%v\n",
		p(50).Round(100*time.Microsecond),
		p(95).Round(100*time.Microsecond),
		p(99).Round(100*time.Microsecond),
		time.Duration(lats[len(lats)-1]).Round(100*time.Microsecond),
	)
	if elapsed.Seconds() > 0 {
		fmt.Printf("  Throughput: %.0f check-ins/sec\n", float64(ok)/elapsed.Seconds())
	}
}

func main() {
	grpcAddr := flag.String("server", "localhost:1530", "gRPC server address (host:port)")
	apiAddr := flag.String("api", "https://localhost:8443", "REST API address")
	count := flag.Int("count", 100, "number of simulated envoys")
	interval := flag.Duration("interval", 5*time.Second, "check-in interval per envoy")
	duration := flag.Duration("duration", 2*time.Minute, "total test duration")
	skipCleanup := flag.Bool("skip-cleanup", false, "leave test envoys enrolled after test")
	adminToken := flag.String("admin-token", "", "admin bearer token for the post-test envoy revoke calls (e.g. $(sudo cat /srv/vigo/cli-admin-token) on the server host); cleanup is skipped if empty")
	flag.Parse()

	fmt.Printf("Vigo Load Test\n")
	fmt.Printf("  Server:   %s (gRPC) / %s (API)\n", *grpcAddr, *apiAddr)
	fmt.Printf("  Envoys:   %d\n", *count)
	fmt.Printf("  Interval: %v\n", *interval)
	fmt.Printf("  Duration: %v\n", *duration)
	fmt.Println()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Phase 1: Enroll test envoys via trusted enrollment.
	fmt.Printf("Enrolling %d test envoys...\n", *count)
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}

	envoys := make([]testEnvoy, 0, *count)
	var enrollFailed int
	for i := range *count {
		hostname := fmt.Sprintf("loadtest-%05d.test", i)
		e, err := enroll(httpClient, *apiAddr, hostname)
		if err != nil {
			enrollFailed++
			if enrollFailed <= 3 {
				fmt.Printf("  FAIL %s: %v\n", hostname, err)
			} else if enrollFailed == 4 {
				fmt.Printf("  ... suppressing further enrollment errors\n")
			}
			continue
		}
		envoys = append(envoys, e)
		if (i+1)%100 == 0 {
			fmt.Printf("  enrolled %d/%d\n", i+1, *count)
		}
	}
	fmt.Printf("  Enrolled %d/%d envoys (%d failed)\n\n", len(envoys), *count, enrollFailed)

	if len(envoys) == 0 {
		fmt.Println("No envoys enrolled. Check that server.yaml has:")
		fmt.Println("  trusted_enrollment:")
		fmt.Println("    - pattern: \"loadtest-*.test\"")
		fmt.Println("      cidr: \"0.0.0.0/0\"")
		os.Exit(1)
	}

	// Phase 2: Run check-in loops.
	fmt.Printf("Running check-ins for %v...\n", *duration)
	st := &latencyStats{}
	var wg sync.WaitGroup
	testStart := time.Now()

	testCtx, testCancel := context.WithTimeout(ctx, *duration)
	defer testCancel()

	for idx := range envoys {
		wg.Add(1)
		go func(e *testEnvoy) {
			defer wg.Done()
			runCheckins(testCtx, *grpcAddr, e, *interval, st)
		}(&envoys[idx])

		// Stagger starts to avoid thundering herd on first cycle.
		stagger := *interval / time.Duration(len(envoys))
		if stagger < 500*time.Microsecond {
			stagger = 500 * time.Microsecond
		}
		if stagger > 50*time.Millisecond {
			stagger = 50 * time.Millisecond
		}
		time.Sleep(stagger)
	}

	// Periodic stats while running.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-testCtx.Done():
				return
			case <-ticker.C:
				ok, fail, lats := st.snapshot()
				elapsed := time.Since(testStart)
				fmt.Printf("\n[%v elapsed]\n", elapsed.Round(time.Second))
				printStats(ok, fail, lats, elapsed)
			}
		}
	}()

	wg.Wait()

	elapsed := time.Since(testStart)
	ok, fail, lats := st.snapshot()
	fmt.Printf("\n=== Final Results (%v) ===\n", elapsed.Round(time.Second))
	printStats(ok, fail, lats, elapsed)

	// Phase 3: Cleanup.
	switch {
	case *skipCleanup:
		// Intentional: operator wants to keep the envoys for follow-up inspection.
	case len(envoys) == 0:
		// Nothing to do.
	case *adminToken == "":
		fmt.Printf("\nSkipping cleanup: --admin-token not provided. To purge, re-run with the token from /srv/vigo/cli-admin-token, or:\n")
		fmt.Printf("  vigocli envoys list --hostname 'loadtest-*.test' | <iterate> vigocli envoys delete\n")
	default:
		fmt.Printf("\nCleaning up %d test envoys...\n", len(envoys))
		cleaned := 0
		var firstErr string
		for _, e := range envoys {
			if err := revokeEnvoy(httpClient, *apiAddr, *adminToken, e.id); err != nil {
				if firstErr == "" {
					firstErr = err.Error()
				}
				continue
			}
			cleaned++
		}
		fmt.Printf("  Revoked %d/%d\n", cleaned, len(envoys))
		if cleaned < len(envoys) && firstErr != "" {
			fmt.Printf("  first failure: %s\n", firstErr)
		}
	}
}

// enroll registers a test envoy via the bootstrap HTTP endpoint (trusted, no token).
func enroll(client *http.Client, apiAddr, hostname string) (testEnvoy, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return testEnvoy{}, fmt.Errorf("keygen: %w", err)
	}

	// Marshal ED25519 public key to PEM.
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return testEnvoy{}, fmt.Errorf("marshal pubkey: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	// Generate TLS key + CSR.
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

// runCheckins runs the check-in loop for one simulated envoy.
func runCheckins(ctx context.Context, grpcAddr string, e *testEnvoy, interval time.Duration, st *latencyStats) {
	cert, err := tls.X509KeyPair(e.certPEM, e.keyPEM)
	if err != nil {
		st.record(0, fmt.Errorf("parse cert: %w", err))
		return
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(e.caCertPEM)

	creds := credentials.NewTLS(&tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		InsecureSkipVerify: true, // server cert may use hostname, not IP
	})

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		st.record(0, fmt.Errorf("dial: %w", err))
		return
	}
	defer conn.Close()

	client := pb.NewVigoAgentClient(conn)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			// Sign: payload || timestamp (matching agent signer.rs)
			msg := append([]byte(""), []byte(ts)...)
			sig := ed25519.Sign(e.signingKey, msg)

			_, err := client.CheckIn(ctx, &pb.CheckInRequest{
				EnvoyId:          e.id,
				StateFingerprint: "",
				Signature:        sig,
				Timestamp:        timestamppb.Now(),
				AgentVersion:     "loadtest",
			})
			st.record(time.Since(start), err)
		}
	}
}

func revokeEnvoy(client *http.Client, apiAddr, adminToken, envoyID string) error {
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/envoys/%s", apiAddr, envoyID), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revoke %s: %s — %s", envoyID, resp.Status, string(body))
	}
	return nil
}

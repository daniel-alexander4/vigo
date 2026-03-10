// Package checkin implements the agent's gRPC client wrapper and daemon pull loop
// with jittered intervals and exponential backoff.
package checkin

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hobnob/hobnob/agent/auth"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client wraps the gRPC HobnobAgent client with ED25519 signing.
type Client struct {
	conn       *grpc.ClientConn
	agent      pb.HobnobAgentClient
	nodeID     string
	privateKey ed25519.PrivateKey
}

// NewClient creates a new gRPC client connection to the Hobnob server.
// It loads TLS credentials for mTLS. If clientCertFile is empty, it connects
// with only CA verification (dev mode) -- the server's signature interceptor
// provides real auth via ED25519.
func NewClient(serverAddr, caFile, clientCertFile, clientKeyFile, nodeID string, privateKey ed25519.PrivateKey) (*Client, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	tlsCfg := &tls.Config{
		RootCAs: certPool,
	}

	// Load client cert for mTLS if provided (production mode).
	if clientCertFile != "" && clientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client TLS keypair: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	conn, err := grpc.NewClient(serverAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		return nil, fmt.Errorf("create gRPC connection: %w", err)
	}

	return &Client{
		conn:       conn,
		agent:      pb.NewHobnobAgentClient(conn),
		nodeID:     nodeID,
		privateKey: privateKey,
	}, nil
}

// CheckIn calls the server's CheckIn RPC with an ED25519-signed request.
// If nailsJSON is non-nil, it is sent inline (replacing separate ReportNails).
// If policyVersion > 0, the agent signals it has a cached policy bundle at
// that version and the server can short-circuit.
// If cachedModuleHashes is non-nil, the server sends stubs for unchanged
// modules (delta transfer).
func (c *Client) CheckIn(ctx context.Context, stateFingerprint string, nailsJSON []byte, policyVersion uint64, cachedModuleHashes map[string]string) (*pb.CheckInResponse, error) {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload([]byte(stateFingerprint), ts, c.privateKey)

	resp, err := c.agent.CheckIn(ctx, &pb.CheckInRequest{
		NodeId:             c.nodeID,
		StateFingerprint:   stateFingerprint,
		Signature:          sig,
		Timestamp:          timestamppb.New(now),
		NailsJson:          nailsJSON,
		PolicyVersion:      policyVersion,
		PolicyMode:         true,
		CachedModuleHashes: cachedModuleHashes,
	})
	if err != nil {
		return nil, fmt.Errorf("check-in RPC: %w", err)
	}

	return resp, nil
}

// Register calls the server's Register RPC with hostname, IP, pubkey PEM, and token.
func (c *Client) Register(ctx context.Context, hostname, ipAddr, pubkeyPEM, token string) (*pb.RegisterResponse, error) {
	resp, err := c.agent.Register(ctx, &pb.RegisterRequest{
		Hostname:  hostname,
		IpAddress: ipAddr,
		PubkeyPem: pubkeyPEM,
		Token:     token,
	})
	if err != nil {
		return nil, fmt.Errorf("register RPC: %w", err)
	}
	return resp, nil
}

// ReportResult calls the server's ReportResult RPC with ED25519-signed results.
func (c *Client) ReportResult(ctx context.Context, runID string, results []*pb.ModuleResult) error {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload([]byte(runID), ts, c.privateKey)

	_, err := c.agent.ReportResult(ctx, &pb.ReportResultRequest{
		RunId:     runID,
		NodeId:    c.nodeID,
		Results:   results,
		Signature: sig,
		Timestamp: timestamppb.New(now),
	})
	if err != nil {
		return fmt.Errorf("report-result RPC: %w", err)
	}

	return nil
}

// ReportNails calls the server's ReportNails RPC with ED25519-signed facts JSON.
func (c *Client) ReportNails(ctx context.Context, nailsMap map[string]any) error {
	nailsJSON, err := json.Marshal(nailsMap)
	if err != nil {
		return fmt.Errorf("marshal nails: %w", err)
	}

	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload(nailsJSON, ts, c.privateKey)

	_, err = c.agent.ReportNails(ctx, &pb.ReportNailsRequest{
		NodeId:    c.nodeID,
		NailsJson: nailsJSON,
		Signature: sig,
		Timestamp: timestamppb.New(now),
	})
	if err != nil {
		return fmt.Errorf("report-facts RPC: %w", err)
	}

	return nil
}

// DownloadCustomNail retrieves a custom nail executable's bytes from the
// server by name. The request travels over the mTLS-authenticated channel.
func (c *Client) DownloadCustomNail(ctx context.Context, name string) ([]byte, error) {
	resp, err := c.agent.DownloadCustomNail(ctx, &pb.DownloadCustomNailRequest{
		Name: name,
	})
	if err != nil {
		return nil, fmt.Errorf("download custom nail %q: %w", name, err)
	}

	return resp.Data, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

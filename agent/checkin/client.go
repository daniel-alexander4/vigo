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
// If traitsJSON is non-nil, it is sent inline (replacing separate ReportTraits).
// If policyVersion > 0, the agent signals it has a cached policy bundle at
// that version and the server can short-circuit.
// If cachedModuleHashes is non-nil, the server sends stubs for unchanged
// modules (delta transfer).
func (c *Client) CheckIn(ctx context.Context, stateFingerprint string, traitsJSON []byte, policyVersion uint64, cachedModuleHashes map[string]string) (*pb.CheckInResponse, error) {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload([]byte(stateFingerprint), ts, c.privateKey)

	resp, err := c.agent.CheckIn(ctx, &pb.CheckInRequest{
		NodeId:             c.nodeID,
		StateFingerprint:   stateFingerprint,
		Signature:          sig,
		Timestamp:          timestamppb.New(now),
		TraitsJson:          traitsJSON,
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

// ReportTraits calls the server's ReportTraits RPC with ED25519-signed facts JSON.
func (c *Client) ReportTraits(ctx context.Context, traitsMap map[string]any) error {
	traitsJSON, err := json.Marshal(traitsMap)
	if err != nil {
		return fmt.Errorf("marshal traits: %w", err)
	}

	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload(traitsJSON, ts, c.privateKey)

	_, err = c.agent.ReportTraits(ctx, &pb.ReportTraitsRequest{
		NodeId:    c.nodeID,
		TraitsJson: traitsJSON,
		Signature: sig,
		Timestamp: timestamppb.New(now),
	})
	if err != nil {
		return fmt.Errorf("report-facts RPC: %w", err)
	}

	return nil
}

// DownloadCustomTrait retrieves a custom trait executable's bytes from the
// server by name. The request travels over the mTLS-authenticated channel.
func (c *Client) DownloadCustomTrait(ctx context.Context, name string) ([]byte, error) {
	resp, err := c.agent.DownloadCustomTrait(ctx, &pb.DownloadCustomTraitRequest{
		Name: name,
	})
	if err != nil {
		return nil, fmt.Errorf("download custom trait %q: %w", name, err)
	}

	return resp.Data, nil
}

// BuildCheckInRequest constructs a signed CheckInRequest without sending it.
// Used by the stream client to send check-ins over the bidirectional stream.
func (c *Client) BuildCheckInRequest(stateFingerprint string, traitsJSON []byte, policyVersion uint64, cachedModuleHashes map[string]string) *pb.CheckInRequest {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload([]byte(stateFingerprint), ts, c.privateKey)

	return &pb.CheckInRequest{
		NodeId:             c.nodeID,
		StateFingerprint:   stateFingerprint,
		Signature:          sig,
		Timestamp:          timestamppb.New(now),
		TraitsJson:         traitsJSON,
		PolicyVersion:      policyVersion,
		PolicyMode:         true,
		CachedModuleHashes: cachedModuleHashes,
	}
}

// BuildReportResultRequest constructs a signed ReportResultRequest without
// sending it. Used by the stream client to send results over the stream.
func (c *Client) BuildReportResultRequest(runID string, results []*pb.ModuleResult) *pb.ReportResultRequest {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload([]byte(runID), ts, c.privateKey)

	return &pb.ReportResultRequest{
		RunId:     runID,
		NodeId:    c.nodeID,
		Results:   results,
		Signature: sig,
		Timestamp: timestamppb.New(now),
	}
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

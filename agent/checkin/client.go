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

	"github.com/isogov/isogov/agent/auth"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client wraps the gRPC IsgovAgent client with ED25519 signing.
type Client struct {
	conn       *grpc.ClientConn
	agent      pb.IsgovAgentClient
	clientID   string
	privateKey ed25519.PrivateKey
}

// NewClient creates a new gRPC client connection to the Isogov server.
// It loads TLS credentials for mTLS. If clientCertFile is empty, it connects
// with only CA verification (dev mode) -- the server's signature interceptor
// provides real auth via ED25519.
func NewClient(serverAddr, caFile, clientCertFile, clientKeyFile, clientID string, privateKey ed25519.PrivateKey) (*Client, error) {
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
		agent:      pb.NewIsgovAgentClient(conn),
		clientID:   clientID,
		privateKey: privateKey,
	}, nil
}

// CheckIn calls the server's CheckIn RPC with an ED25519-signed request.
func (c *Client) CheckIn(ctx context.Context, stateFingerprint string) (*pb.CheckInResponse, error) {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload([]byte(stateFingerprint), ts, c.privateKey)

	resp, err := c.agent.CheckIn(ctx, &pb.CheckInRequest{
		ClientId:         c.clientID,
		StateFingerprint: stateFingerprint,
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
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
		ClientId:  c.clientID,
		Results:   results,
		Signature: sig,
		Timestamp: timestamppb.New(now),
	})
	if err != nil {
		return fmt.Errorf("report-result RPC: %w", err)
	}

	return nil
}

// ReportFacts calls the server's ReportFacts RPC with ED25519-signed facts JSON.
func (c *Client) ReportFacts(ctx context.Context, factsMap map[string]any) error {
	factsJSON, err := json.Marshal(factsMap)
	if err != nil {
		return fmt.Errorf("marshal facts: %w", err)
	}

	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := auth.SignPayload(factsJSON, ts, c.privateKey)

	_, err = c.agent.ReportFacts(ctx, &pb.ReportFactsRequest{
		ClientId:  c.clientID,
		FactsJson: factsJSON,
		Signature: sig,
		Timestamp: timestamppb.New(now),
	})
	if err != nil {
		return fmt.Errorf("report-facts RPC: %w", err)
	}

	return nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

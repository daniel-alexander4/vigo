// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IsopassProvider retrieves secrets from an isopass HTTP API instance.
// It uses service account bearer token authentication with automatic
// token exchange and renewal.
type IsopassProvider struct {
	baseURL     string
	bearerToken string
	token       string
	tokenExpiry time.Time
	httpClient  *http.Client
	mu          sync.Mutex
}

// searchResult represents a single secret returned from the isopass search API.
type searchResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	Version int    `json:"version"`
}

// tokenResponse is the JSON body returned by the token exchange/renew endpoints.
type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// NewIsopassProvider creates a provider that retrieves secrets from isopass
// at the given base URL using service account bearer token authentication.
// Set tlsSkipVerify to true when isopass uses a self-signed certificate.
func NewIsopassProvider(baseURL, bearerToken string, tlsSkipVerify bool) (*IsopassProvider, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	if tlsSkipVerify {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	return &IsopassProvider{
		baseURL:     strings.TrimRight(baseURL, "/"),
		bearerToken: bearerToken,
		httpClient:  client,
	}, nil
}

// Get retrieves a secret string by key path (e.g. "hobnob/db/dsn").
func (p *IsopassProvider) Get(ctx context.Context, key string) (string, error) {
	if err := p.ensureToken(ctx); err != nil {
		return "", fmt.Errorf("isopass auth: %w", err)
	}

	results, err := p.search(ctx, key)
	if err != nil {
		return "", err
	}

	for _, r := range results {
		if r.Name == key {
			return r.Value, nil
		}
	}

	if len(results) > 0 {
		return results[0].Value, nil
	}

	return "", fmt.Errorf("secret %q not found in isopass", key)
}

// GetBytes retrieves a binary secret as a byte slice.
func (p *IsopassProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	val, err := p.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return []byte(val), nil
}

// GetVersion returns the current version for a secret path.
// This is used by the rotation watcher to detect version changes.
// The version is extracted from the search response's version field.
func (p *IsopassProvider) GetVersion(ctx context.Context, key string) (int, error) {
	if err := p.ensureToken(ctx); err != nil {
		return 0, fmt.Errorf("isopass auth: %w", err)
	}

	results, err := p.search(ctx, key)
	if err != nil {
		return 0, err
	}

	for _, r := range results {
		if r.Name == key {
			return r.Version, nil
		}
	}

	if len(results) > 0 {
		return results[0].Version, nil
	}

	return 0, fmt.Errorf("secret %q not found in isopass", key)
}

// Close is a no-op; the HTTP client has no persistent connections to clean up.
func (p *IsopassProvider) Close() error {
	return nil
}

// ensureToken checks if the current short-lived token is valid and refreshes
// it if expired or about to expire (within 60s).
func (p *IsopassProvider) ensureToken(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Until(p.tokenExpiry) > 60*time.Second {
		return nil // token is still valid
	}

	return p.exchangeToken(ctx)
}

// exchangeToken exchanges the static bearer token for a short-lived API token.
// Must be called with p.mu held.
func (p *IsopassProvider) exchangeToken(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/api/auth/token", nil)
	if err != nil {
		return fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.bearerToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("isopass unreachable at %s -- is isopass running? %w",
			p.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("isopass authentication failed -- check bearer token")
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("isopass token exchange failed (status %d): %s",
			resp.StatusCode, string(body))
	}

	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}

	expiry, err := time.Parse(time.RFC3339, tok.ExpiresAt)
	if err != nil {
		return fmt.Errorf("parsing token expiry: %w", err)
	}

	p.token = tok.Token
	p.tokenExpiry = expiry
	return nil
}

// search calls the isopass service account search endpoint.
func (p *IsopassProvider) search(ctx context.Context, query string) ([]searchResult, error) {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshaling search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/api/sa/search", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("creating search request: %w", err)
	}

	p.mu.Lock()
	token := p.token
	p.mu.Unlock()

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("isopass request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("isopass authentication failed -- check bearer token")
	}
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("isopass search failed (status %d): %s",
			resp.StatusCode, string(respBody))
	}

	var results []searchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}

	return results, nil
}


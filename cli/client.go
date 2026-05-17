// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type apiClient struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
}

func newAPIClient() *apiClient {
	return newAPIClientFor(serverURL)
}

// newAPIClientFor builds an apiClient against an explicit base URL.
// Used by cross-vigosrv calls (notably `vigocli tungol ops-puddle
// join --from <other>`). Auth token resolution is the same as
// newAPIClient — the operator is expected to have run `vigocli
// auth login --server <baseURL>` against the target host first.
func newAPIClientFor(baseURL string) *apiClient {
	hc := &http.Client{Timeout: 30 * time.Second}
	if isLocalhost(baseURL) {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	c := &apiClient{
		baseURL:    baseURL,
		httpClient: hc,
	}
	c.authToken = loadAuthToken()
	return c
}

// isLocalhost returns true if the URL points to a loopback address.
func isLocalhost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// getRaw performs a GET and returns the raw response (caller must close body).
func (c *apiClient) getRaw(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuthHeader(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *apiClient) get(path string, result any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuthHeader(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

func (c *apiClient) post(path string, body, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

func (c *apiClient) put(path string, body, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

// postNoTimeout is post() with the client timeout disabled. Use for endpoints
// that can legitimately run for many minutes — VACUUM on a multi-GB DB,
// large db prune cascades, etc. The default 30s timeout would otherwise
// cancel the client side while the server keeps grinding.
func (c *apiClient) postNoTimeout(path string, body, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)

	longClient := *c.httpClient
	longClient.Timeout = 0
	resp, err := longClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

// postStream POSTs and reads the response body as newline-delimited JSON,
// invoking onMessage for each parsed object. Use for endpoints that emit
// progress ticks during a long-running operation. No client-side timeout.
// Returns when the server closes the body or onMessage returns an error.
func (c *apiClient) postStream(path string, body any, onMessage func(map[string]any) error) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)

	streamClient := *c.httpClient
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.decodeResponse(resp, nil)
	}

	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			return fmt.Errorf("decode stream message: %w", err)
		}
		if err := onMessage(msg); err != nil {
			return err
		}
	}
	return nil
}

// postRaw sends a raw body (e.g., a file) as a POST request with optional headers.
// Uses no timeout — uploads can be arbitrarily large and should not be cut short.
func (c *apiClient) postRaw(path string, body io.Reader, headers map[string]string, result any) error {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.setAuthHeader(req)

	// Use a separate client with no timeout for streaming uploads.
	uploadClient := *c.httpClient
	uploadClient.Timeout = 0
	resp, err := uploadClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

func (c *apiClient) delete(path string, result any) error {
	req, err := http.NewRequest(http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuthHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

func (c *apiClient) setAuthHeader(req *http.Request) {
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
}

func (c *apiClient) decodeResponse(resp *http.Response, result any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to extract a human-readable message from JSON error bodies.
		var errBody struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(body, &errBody) == nil {
			if errBody.Message != "" {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode, errBody.Message)
			}
			if errBody.Error != "" {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode, errBody.Error)
			}
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	if result != nil && len(body) > 0 {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}
	return nil
}

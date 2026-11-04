// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// defaultServerCAFile is the server CA the REST listener's certificate chains
// to (server/secrets/gencerts.go writes it next to the server cert).
const defaultServerCAFile = "/srv/vigo/tls/ca"

// caFileFlag and insecureTLS back the global --ca-file / --insecure flags.
var (
	caFileFlag  string
	insecureTLS bool
)

// resolvedCAFile returns the CA bundle path and whether the operator named it
// explicitly (--ca-file, then $VIGO_CA_FILE) rather than taking the default.
func resolvedCAFile() (string, bool) {
	if caFileFlag != "" {
		return caFileFlag, true
	}
	if env := os.Getenv("VIGO_CA_FILE"); env != "" {
		return env, true
	}
	return defaultServerCAFile, false
}

// cliTLSConfig is the one TLS trust decision every vigocli HTTPS call makes.
//
//   - --insecure: no verification (the operator asked for it by name).
//   - plain http: no TLS at all.
//   - a loopback URL: verify against the server CA (--ca-file, $VIGO_CA_FILE,
//     else /srv/vigo/tls/ca). An unreadable CA is an error, never a silent
//     downgrade: 8443 is an unprivileged port, so without verification any
//     local user who binds it while vigosrv is down collects the bearer token.
//   - any other URL: the system roots, or the named CA if one was given.
func cliTLSConfig(baseURL string) (*tls.Config, error) {
	if insecureTLS {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit --insecure
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil, nil
	}
	path, explicit := resolvedCAFile()
	if !explicit && !isLocalhost(baseURL) {
		return nil, nil
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the server CA %s to verify %s (%v) — pass --ca-file <path>, or --insecure to skip verification", path, baseURL, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("server CA %s holds no PEM certificate — pass --ca-file <path>, or --insecure to skip verification", path)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// errTransport fails every request with the TLS-setup error, so a client built
// without a usable trust anchor refuses to send anything (bearer token included).
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// newCLIHTTPClient builds an http.Client for baseURL with cliTLSConfig applied.
func newCLIHTTPClient(baseURL string, timeout time.Duration) *http.Client {
	hc := &http.Client{Timeout: timeout}
	cfg, err := cliTLSConfig(baseURL)
	switch {
	case err != nil:
		hc.Transport = errTransport{err: err}
	case cfg != nil:
		hc.Transport = &http.Transport{TLSClientConfig: cfg}
	}
	return hc
}

type apiClient struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
}

func newAPIClient() *apiClient {
	return newAPIClientFor(serverURL)
}

// newAPIClientFor builds an apiClient against an explicit base URL.
// Used by cross-vigosrv calls (notably `vigocli spanner ops-puddle
// join --from <other>`). Auth token resolution is the same as
// newAPIClient — the operator is expected to have run `vigocli
// auth login --server <baseURL>` against the target host first.
func newAPIClientFor(baseURL string) *apiClient {
	c := &apiClient{
		baseURL:    baseURL,
		httpClient: newCLIHTTPClient(baseURL, 30*time.Second),
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
		return nil, fmt.Errorf("cannot reach server: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body) // best-effort: the status code is the error; the body only adds detail
		resp.Body.Close()
		return nil, statusError(resp.StatusCode, body)
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
		return fmt.Errorf("cannot reach server: %w", err)
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
		return fmt.Errorf("cannot reach server: %w", err)
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
		return fmt.Errorf("cannot reach server: %w", err)
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
		return fmt.Errorf("cannot reach server: %w", err)
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
		return fmt.Errorf("cannot reach server: %w", err)
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
		return fmt.Errorf("cannot reach server: %w", err)
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
		return fmt.Errorf("cannot reach server: %w", err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, result)
}

func (c *apiClient) setAuthHeader(req *http.Request) {
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
}

// httpStatusError is the typed error returned for any non-2xx
// response. Callers that need the raw status code or body — the task
// dispatch path keys on a structured `blocked` payload to drive the
// guardrail-bypass confirmation — errors.As it; callers that just
// print fall through to Error(), which preserves the prior
// "server returned <code>: <message>" format.
type httpStatusError struct {
	Code int
	Body []byte
	msg  string
}

func (e *httpStatusError) Error() string { return e.msg }

// statusError builds the canonical non-2xx error, extracting a
// human-readable message from the server's JSON {message}/{error}
// envelope when present and falling back to the raw body. Shared by
// decodeResponse and getRaw so a failed GET surfaces the server's
// message instead of a bare "server returned <code>".
func statusError(code int, body []byte) *httpStatusError {
	msg := fmt.Sprintf("server returned %d: %s", code, string(body))
	var errBody struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &errBody) == nil {
		if errBody.Message != "" {
			msg = fmt.Sprintf("server returned %d: %s", code, errBody.Message)
		} else if errBody.Error != "" {
			msg = fmt.Sprintf("server returned %d: %s", code, errBody.Error)
		}
	}
	return &httpStatusError{Code: code, Body: body, msg: msg}
}

func (c *apiClient) decodeResponse(resp *http.Response, result any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp.StatusCode, body)
	}

	if result != nil && len(body) > 0 {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}
	return nil
}

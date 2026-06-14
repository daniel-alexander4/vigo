// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// VaultProvider resolves secrets from HashiCorp Vault KV v2 engine.
type VaultProvider struct {
	url    string
	token  string
	mount  string
	client *http.Client
}

// NewVaultProvider creates a provider that reads from Vault KV v2.
func NewVaultProvider(url, token, mount string, skipTLS bool) (*VaultProvider, error) {
	transport := &http.Transport{}
	if skipTLS {
		slog.Warn("vault TLS verification disabled — connections are vulnerable to MITM attacks; use a proper CA cert for production")
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &VaultProvider{
		url:    strings.TrimSuffix(url, "/"),
		token:  token,
		mount:  mount,
		client: &http.Client{Transport: transport},
	}, nil
}

func (v *VaultProvider) Get(ctx context.Context, key string) (string, error) {
	b, err := v.GetBytes(ctx, key)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (v *VaultProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	// KV v2 API: GET /v1/{mount}/data/{path}
	apiURL := fmt.Sprintf("%s/v1/%s/data/%s", v.url, v.mount, key)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("vault: request: %w", err)
	}
	req.Header.Set("X-Vault-Token", v.token)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault: http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("vault: %s returned %d: %s", key, resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vault: decode: %w", err)
	}

	// Convention: the secret value is in the "value" key, or the first key if only one exists.
	if val, ok := result.Data.Data["value"]; ok {
		return []byte(val), nil
	}
	for _, val := range result.Data.Data {
		return []byte(val), nil
	}
	return nil, fmt.Errorf("vault: no data at %s", key)
}

func (v *VaultProvider) Close() error { return nil }

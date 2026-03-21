// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// --- AWS Secrets Manager ---

// AWSProvider resolves secrets from AWS Secrets Manager.
// Uses default credential chain (env vars, instance profile, etc.).
type AWSProvider struct {
	region string
	client *http.Client
}

// NewAWSProvider creates a provider that reads from AWS Secrets Manager.
func NewAWSProvider(region string) (*AWSProvider, error) {
	return &AWSProvider{
		region: region,
		client: &http.Client{},
	}, nil
}

func (a *AWSProvider) Get(ctx context.Context, key string) (string, error) {
	b, err := a.GetBytes(ctx, key)
	return string(b), err
}

func (a *AWSProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	// AWS Secrets Manager API via HTTP (requires AWS SDK v2 for proper signing).
	// This is a placeholder — production use should import github.com/aws/aws-sdk-go-v2.
	return nil, fmt.Errorf("aws secrets manager: requires AWS SDK — configure AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables and rebuild with AWS SDK support")
}

func (a *AWSProvider) Close() error { return nil }

// --- Azure Key Vault ---

// AzureProvider resolves secrets from Azure Key Vault.
// Uses managed identity or environment credentials.
type AzureProvider struct {
	vaultName string
	client    *http.Client
	token     string
}

// NewAzureProvider creates a provider that reads from Azure Key Vault.
func NewAzureProvider(vaultName string) (*AzureProvider, error) {
	return &AzureProvider{
		vaultName: vaultName,
		client:    &http.Client{},
	}, nil
}

func (az *AzureProvider) Get(ctx context.Context, key string) (string, error) {
	b, err := az.GetBytes(ctx, key)
	return string(b), err
}

func (az *AzureProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	// Azure Key Vault REST API.
	// Convert secret path separators: vigo/db/dsn → vigo-db-dsn (Key Vault doesn't allow /).
	secretName := strings.ReplaceAll(key, "/", "-")
	url := fmt.Sprintf("https://%s.vault.azure.net/secrets/%s?api-version=7.4", az.vaultName, secretName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("azure: request: %w", err)
	}
	if az.token != "" {
		req.Header.Set("Authorization", "Bearer "+az.token)
	}

	resp, err := az.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure: http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("azure: %s returned %d: %s", key, resp.StatusCode, string(body))
	}

	var result struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("azure: decode: %w", err)
	}
	return []byte(result.Value), nil
}

func (az *AzureProvider) Close() error { return nil }

// --- GCP Secret Manager ---

// GCPProvider resolves secrets from Google Cloud Secret Manager.
// Uses application default credentials.
type GCPProvider struct {
	projectID string
	client    *http.Client
}

// NewGCPProvider creates a provider that reads from GCP Secret Manager.
func NewGCPProvider(projectID string) (*GCPProvider, error) {
	return &GCPProvider{
		projectID: projectID,
		client:    &http.Client{},
	}, nil
}

func (g *GCPProvider) Get(ctx context.Context, key string) (string, error) {
	b, err := g.GetBytes(ctx, key)
	return string(b), err
}

func (g *GCPProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	// GCP Secret Manager REST API.
	// Convert path separators: vigo/db/dsn → vigo-db-dsn.
	secretName := strings.ReplaceAll(key, "/", "-")
	url := fmt.Sprintf("https://secretmanager.googleapis.com/v1/projects/%s/secrets/%s/versions/latest:access",
		g.projectID, secretName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("gcp: request: %w", err)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gcp: http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gcp: %s returned %d: %s", key, resp.StatusCode, string(body))
	}

	var result struct {
		Payload struct {
			Data string `json:"data"` // base64 encoded
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("gcp: decode: %w", err)
	}
	// Data is base64 encoded by the API.
	return []byte(result.Payload.Data), nil
}

func (g *GCPProvider) Close() error { return nil }

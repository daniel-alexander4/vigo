// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OllamaProvider wraps OpenAICompatProvider targeting a local Ollama instance.
// It adds auto-pull on startup and Ollama-specific health checking.
type OllamaProvider struct {
	*OpenAICompatProvider
	ollamaBaseURL string // e.g. http://127.0.0.1:11434
}

// NewOllamaProvider creates an Ollama provider. The baseURL should be the
// Ollama API root (e.g. "http://127.0.0.1:11434"). The OpenAI-compat endpoint
// is at baseURL + "/v1".
func NewOllamaProvider(baseURL string) *OllamaProvider {
	// Trim trailing /v1 if user accidentally included it.
	ollamaBase := strings.TrimSuffix(baseURL, "/v1")

	p := &OllamaProvider{
		OpenAICompatProvider: NewOpenAICompatProvider(ollamaBase+"/v1", ""),
		ollamaBaseURL:        ollamaBase,
	}
	p.OpenAICompatProvider.name = "ollama"
	// Ollama on CPU can take 3-5 minutes for cold model load + tool-heavy prompts.
	p.OpenAICompatProvider.httpClient.Timeout = 5 * time.Minute
	return p
}

// EnsureModel checks if the configured model is available and pulls it if not.
// Retries connecting to Ollama with backoff to handle container startup ordering.
func (p *OllamaProvider) EnsureModel(ctx context.Context, model string) error {
	client := &http.Client{Timeout: 5 * time.Second}

	// Wait for Ollama to become reachable (up to ~60s with backoff).
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	backoff := 2 * time.Second
	for attempt := 0; attempt < 8; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		tagsReq, err := http.NewRequestWithContext(ctx, "GET", p.ollamaBaseURL+"/api/tags", nil)
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}

		resp, err := client.Do(tagsReq)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				if err := json.NewDecoder(io.LimitReader(resp.Body, maxProviderResponseBytes)).Decode(&tags); err != nil {
					resp.Body.Close()
					return fmt.Errorf("decoding model list: %w", err)
				}
				resp.Body.Close()
				break // Ollama is ready
			}
			resp.Body.Close()
		}

		if attempt < 7 {
			slog.Info("waiting for ollama", "attempt", attempt+1, "retry_in", backoff)
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, 15*time.Second)
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			return fmt.Errorf("ollama unreachable at %s after %d attempts", p.ollamaBaseURL, attempt+1)
		}
	}

	// Check if model is already downloaded.
	for _, m := range tags.Models {
		if m.Name == model || strings.HasPrefix(m.Name, model+":") {
			slog.Info("ollama model already available", "model", model)
			return nil
		}
	}

	// Pull the model.
	slog.Info("pulling ollama model (this may take a few minutes)", "model", model)

	pullBody, _ := json.Marshal(map[string]any{
		"name":   model,
		"stream": false,
	})

	pullCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	pullReq, _ := http.NewRequestWithContext(pullCtx, "POST", p.ollamaBaseURL+"/api/pull", bytes.NewReader(pullBody))
	pullReq.Header.Set("Content-Type", "application/json")

	pullClient := &http.Client{Timeout: 30 * time.Minute}
	pullResp, err := pullClient.Do(pullReq)
	if err != nil {
		return fmt.Errorf("pulling model %s: %w", model, err)
	}
	defer pullResp.Body.Close()

	if pullResp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull model returned %d", pullResp.StatusCode)
	}

	slog.Info("ollama model pulled successfully", "model", model)
	return nil
}

// WarmModel sends a trivial request to force Ollama to load the model into
// memory. This avoids a 60-90 second cold-load delay on the first real user
// request. Called in a background goroutine at startup.
func (p *OllamaProvider) WarmModel(model string) {
	slog.Info("warming ollama model", "model", model)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	resp, err := p.Chat(ctx, ChatRequest{
		Model:    model,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		slog.Warn("ollama model warm-up failed (will retry on first request)", "error", err)
		return
	}
	_ = resp
	slog.Info("ollama model warmed", "model", model)
}

// Chat overrides the base OpenAICompatProvider to omit max_tokens when tools
// are present. Ollama's OpenAI-compat endpoint hangs when both max_tokens
// and tools are sent in the same request.
func (p *OllamaProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	maxTokens := req.MaxTokens
	if len(req.Tools) > 0 {
		slog.Info("ollama: omitting max_tokens (incompatible with tools)", "original", req.MaxTokens, "tools", len(req.Tools))
		maxTokens = 0
	}
	return p.OpenAICompatProvider.chat(ctx, req, maxTokens)
}

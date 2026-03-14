// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ClaudeProvider implements Provider using the Anthropic Messages API.
// Uses raw HTTP to avoid a direct SDK dependency.
type ClaudeProvider struct {
	apiKey     string
	httpClient *http.Client
}

// NewClaudeProvider creates a Claude provider.
func NewClaudeProvider(apiKey string) *ClaudeProvider {
	return &ClaudeProvider{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (p *ClaudeProvider) Name() string { return "claude" }

// Claude API types.
type claudeRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []claudeMsg    `json:"messages"`
	Tools     []claudeTool   `json:"tools,omitempty"`
}

type claudeMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []claudeContentBlock
}

type claudeContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type claudeTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type claudeResponse struct {
	Content []struct {
		Type  string `json:"type"`
		Text  string `json:"text,omitempty"`
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
		Input any    `json:"input,omitempty"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *ClaudeProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Extract system message.
	var system string
	var messages []claudeMsg

	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			system = m.Content

		case RoleUser:
			messages = append(messages, claudeMsg{Role: "user", Content: m.Content})

		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				// Assistant message with tool use blocks.
				var blocks []claudeContentBlock
				if m.Content != "" {
					blocks = append(blocks, claudeContentBlock{Type: "text", Text: m.Content})
				}
				for _, tc := range m.ToolCalls {
					var input any
					json.Unmarshal([]byte(tc.Arguments), &input)
					blocks = append(blocks, claudeContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				messages = append(messages, claudeMsg{Role: "assistant", Content: blocks})
			} else {
				messages = append(messages, claudeMsg{Role: "assistant", Content: m.Content})
			}

		case RoleTool:
			// Claude expects tool results as user messages with tool_result content blocks.
			messages = append(messages, claudeMsg{
				Role: "user",
				Content: []claudeContentBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
		}
	}

	// Build tools.
	var tools []claudeTool
	for _, t := range req.Tools {
		tools = append(tools, claudeTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	body := claudeRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 4096
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Claude API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var cResp claudeResponse
	if err := json.Unmarshal(respBody, &cResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if cResp.Error != nil {
		return nil, fmt.Errorf("Claude API error: %s", cResp.Error.Message)
	}

	result := &ChatResponse{
		TokensUsed: cResp.Usage.InputTokens + cResp.Usage.OutputTokens,
	}

	for _, block := range cResp.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(args),
			})
		}
	}

	return result, nil
}

func (p *ClaudeProvider) ChatStream(ctx context.Context, req ChatRequest) (EventStream, error) {
	// Use non-streaming for now and wrap.
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &singleEventStream{resp: resp}, nil
}

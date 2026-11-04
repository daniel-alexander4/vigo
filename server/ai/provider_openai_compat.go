// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

// OpenAICompatProvider implements Provider using raw HTTP against any
// OpenAI-compatible /v1/chat/completions endpoint. Used for Groq,
// Together, llama.cpp, vLLM, and as the base for the Ollama provider.
type OpenAICompatProvider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	name       string
}

// NewOpenAICompatProvider creates a generic OpenAI-compatible provider.
func NewOpenAICompatProvider(baseURL, apiKey string) *OpenAICompatProvider {
	return &OpenAICompatProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		name: "openai-compat",
	}
}

func (p *OpenAICompatProvider) Name() string { return p.name }

// oaiMessage is the OpenAI message format.
type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string     `json:"type"`
	Function oaiToolDef `json:"function"`
}

type oaiToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []oaiTool    `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *OpenAICompatProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return p.chat(ctx, req, req.MaxTokens)
}

// chat is the shared implementation. maxTokens is passed separately so
// OllamaProvider can override it (Ollama hangs when max_tokens and tools
// are both present in the OpenAI-compat endpoint).
func (p *OpenAICompatProvider) chat(ctx context.Context, req ChatRequest, maxTokens int) (*ChatResponse, error) {
	// Convert to OpenAI format.
	var messages []oaiMessage
	for _, m := range req.Messages {
		msg := oaiMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, oaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: oaiToolFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		messages = append(messages, msg)
	}

	var tools []oaiTool
	for _, t := range req.Tools {
		tools = append(tools, oaiTool{
			Type:     "function",
			Function: oaiToolDef(t),
		})
	}

	body := oaiRequest{
		Model:     req.Model,
		Messages:  messages,
		Tools:     tools,
		MaxTokens: maxTokens,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := readProviderBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, providerStatusError(p.name, resp.StatusCode, respBody)
	}

	var oaiResp oaiResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if oaiResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", oaiResp.Error.Message)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := oaiResp.Choices[0]
	result := &ChatResponse{
		Content:    choice.Message.Content,
		TokensUsed: oaiResp.Usage.TotalTokens,
	}

	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return result, nil
}

func (p *OpenAICompatProvider) ChatStream(ctx context.Context, req ChatRequest) (EventStream, error) {
	// For simplicity, use non-streaming and wrap in a single-event stream.
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &singleEventStream{resp: resp}, nil
}

// singleEventStream wraps a single ChatResponse as an EventStream.
type singleEventStream struct {
	resp *ChatResponse
	done bool
}

func (s *singleEventStream) Next() (StreamEvent, error) {
	if s.done {
		return StreamEvent{}, io.EOF
	}
	s.done = true

	if len(s.resp.ToolCalls) > 0 {
		return StreamEvent{
			Type:     "tool_call",
			ToolCall: &s.resp.ToolCalls[0],
		}, nil
	}

	return StreamEvent{
		Type:       "done",
		Text:       s.resp.Content,
		TokensUsed: s.resp.TokensUsed,
	}, nil
}

func (s *singleEventStream) Close() error { return nil }

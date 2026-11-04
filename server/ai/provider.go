// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package ai provides an LLM-powered assistant for natural language queries
// about Vigo fleet state, configuration, and diagnostics. It wraps multiple
// LLM backends (Ollama, Claude, OpenAI, generic OpenAI-compat) behind a
// common Provider interface and uses tool-calling to access live server data.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// maxProviderResponseBytes bounds a provider response body read into
// memory. Generous for a chat completion; a body past it is refused rather
// than buffered.
const maxProviderResponseBytes = 8 << 20

// maxProviderErrorChars bounds the upstream error text carried in a
// non-200 error. That error reaches the browser over SSE, so it carries a
// short sanitized message, never the provider's raw body.
const maxProviderErrorChars = 300

// readProviderBody reads a provider response body, refusing one larger than
// maxProviderResponseBytes.
func readProviderBody(body io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxProviderResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxProviderResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxProviderResponseBytes)
	}
	return b, nil
}

// providerStatusError builds the error for a non-200 provider response:
// the status plus the provider's own error message when the body is the
// usual {"error": ...} JSON, else the body's text — whitespace and control
// characters collapsed, secrets scrubbed, truncated to maxProviderErrorChars.
func providerStatusError(provider string, status int, body []byte) error {
	msg := providerErrorMessage(body)
	if msg == "" {
		return fmt.Errorf("%s API returned %d", provider, status)
	}
	return fmt.Errorf("%s API returned %d: %s", provider, status, msg)
}

func providerErrorMessage(body []byte) string {
	text := string(body)
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &env) == nil {
		var nested struct {
			Message string `json:"message"`
		}
		var flat string
		switch {
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &nested) == nil && nested.Message != "":
			text = nested.Message
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &flat) == nil && flat != "":
			text = flat
		case env.Message != "":
			text = env.Message
		}
	}
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	text = stripSecrets(text)
	if r := []rune(text); len(r) > maxProviderErrorChars {
		text = string(r[:maxProviderErrorChars]) + "…"
	}
	return text
}

// Role identifies the sender of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single turn in a conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // set when Role == RoleTool
}

// ToolCall represents the LLM requesting a tool invocation.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON
}

// ToolDef describes a tool the LLM may call.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema object
}

// ChatRequest is the input to a provider chat call.
type ChatRequest struct {
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Model     string    `json:"model,omitempty"`
}

// ChatResponse is the output from a provider chat call.
type ChatResponse struct {
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	TokensUsed int        `json:"tokens_used"`
}

// StreamEvent is a single event in a streaming response.
type StreamEvent struct {
	Type       string    `json:"type"` // "delta", "tool_call", "done"
	Text       string    `json:"text,omitempty"`
	ToolCall   *ToolCall `json:"tool_call,omitempty"`
	TokensUsed int       `json:"tokens_used,omitempty"`
}

// EventStream yields streaming events from a chat completion.
type EventStream interface {
	Next() (StreamEvent, error)
	Close() error
}

// Provider is the interface all LLM backends implement.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (EventStream, error)
	Name() string
}

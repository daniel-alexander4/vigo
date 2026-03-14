// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package ai provides an LLM-powered assistant for natural language queries
// about Hobnob fleet state, configuration, and diagnostics. It wraps multiple
// LLM backends (Ollama, Claude, OpenAI, generic OpenAI-compat) behind a
// common Provider interface and uses tool-calling to access live server data.
package ai

import "context"

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
	Type      string `json:"type"` // "delta", "tool_call", "done"
	Text      string `json:"text,omitempty"`
	ToolCall  *ToolCall `json:"tool_call,omitempty"`
	TokensUsed int      `json:"tokens_used,omitempty"`
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

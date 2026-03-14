// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
)

// AskRequest is the input to the assistant.
type AskRequest struct {
	Question string `json:"question"`
	Context  struct {
		Thrall string `json:"thrall,omitempty"`
	} `json:"context,omitempty"`
	History []HistoryMessage `json:"history,omitempty"`
}

// HistoryMessage is a previous message in a multi-turn conversation.
type HistoryMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// AskResponse is the output from the assistant.
type AskResponse struct {
	Answer     string   `json:"answer"`
	ToolsUsed  []string `json:"tools_used"`
	TokensUsed int      `json:"tokens_used"`
}

// Assistant wraps an LLM provider with hobnob tools and system context.
type Assistant struct {
	provider     Provider
	tools        *toolDeps
	cfg          config.AISection
	rateLimiter  *rateLimiter
	systemPrompt string
}

// NewAssistant creates an assistant with the given provider and dependencies.
// The docsDir parameter specifies the path to docs for system prompt injection
// (controlled by cfg.DocsMode: "full", "condensed", or "off").
func NewAssistant(provider Provider, queries *db.Queries, configStore *config.ConfigStore, runStore db.RunStore, baseInterval time.Duration, cfg config.AISection, docsDir string) *Assistant {
	cfg = withDefaults(cfg)
	var rl *rateLimiter
	if cfg.RateLimit > 0 {
		rl = newRateLimiter(cfg.RateLimit)
	}

	// Load docs corpus for system prompt injection.
	docsMode := cfg.DocsMode
	if docsMode == "" {
		docsMode = "full"
	}
	var docsCorpus string
	if docsMode != "off" && docsDir != "" {
		full := LoadDocsCorpus(docsDir)
		if docsMode == "condensed" {
			docsCorpus = CondenseDocsCorpus(full)
		} else {
			docsCorpus = full
		}
		if docsCorpus != "" {
			slog.Info("ai: docs corpus loaded", "mode", docsMode, "bytes", len(docsCorpus))
		}
	}

	return &Assistant{
		provider: provider,
		tools: &toolDeps{
			queries:      queries,
			configStore:  configStore,
			runStore:     runStore,
			baseInterval: baseInterval,
		},
		cfg:          cfg,
		rateLimiter:  rl,
		systemPrompt: buildSystemPrompt(docsCorpus),
	}
}

// Ask processes a question through the LLM with tool-use loop.
func (a *Assistant) Ask(ctx context.Context, req AskRequest) (*AskResponse, error) {
	if a.rateLimiter != nil && !a.rateLimiter.allow() {
		return nil, fmt.Errorf("rate limit exceeded (%d requests/minute)", a.cfg.RateLimit)
	}

	aiRequestsTotal.Inc()
	start := time.Now()
	defer func() {
		aiRequestDuration.Observe(time.Since(start).Seconds())
	}()

	messages := []Message{
		{Role: RoleSystem, Content: a.systemPrompt},
	}

	// Augment question with thrall context if provided.
	question := req.Question
	if req.Context.Thrall != "" {
		question = fmt.Sprintf("[Context: thrall=%s] %s", req.Context.Thrall, question)
	}

	// Determine context mode.
	mode := a.cfg.ContextMode
	if mode == "auto" || mode == "prefetch" {
		prefetched := a.prefetchData(ctx, question, req.Context.Thrall)
		if prefetched != "" {
			question = question + "\n\n--- Prefetched Data ---\n" + prefetched
		}
	}

	// Add conversation history.
	for _, h := range req.History {
		role := RoleUser
		if h.Role == "assistant" {
			role = RoleAssistant
		}
		messages = append(messages, Message{Role: role, Content: h.Content})
	}

	messages = append(messages, Message{Role: RoleUser, Content: question})

	// Decide whether to send tools.
	var tools []ToolDef
	if mode == "tools" || mode == "auto" {
		tools = toolDefs()
	}

	totalTokens := 0
	var toolsUsed []string

	for round := 0; round < a.cfg.MaxToolRounds; round++ {
		resp, err := a.provider.Chat(ctx, ChatRequest{
			Messages:  messages,
			Tools:     tools,
			MaxTokens: a.cfg.MaxTokens,
			Model:     a.cfg.Model,
		})
		if err != nil {
			aiErrorsTotal.Inc()
			return nil, fmt.Errorf("LLM chat failed: %w", err)
		}
		totalTokens += resp.TokensUsed
		aiTokensTotal.Add(float64(resp.TokensUsed))

		// No tool calls — we have the final answer.
		if len(resp.ToolCalls) == 0 {
			return &AskResponse{
				Answer:     resp.Content,
				ToolsUsed:  toolsUsed,
				TokensUsed: totalTokens,
			}, nil
		}

		// In auto mode, if the first round produces malformed tool calls,
		// fall back to prefetch (no tools).
		if mode == "auto" && round == 0 && hasEmptyToolCalls(resp.ToolCalls) {
			slog.Info("ai: malformed tool calls detected, falling back to prefetch mode")
			tools = nil
			continue
		}

		// Process tool calls.
		assistantMsg := Message{
			Role:      RoleAssistant,
			ToolCalls: resp.ToolCalls,
			Content:   resp.Content,
		}
		messages = append(messages, assistantMsg)

		for _, tc := range resp.ToolCalls {
			aiToolCallsTotal.WithLabelValues(tc.Name).Inc()
			toolsUsed = append(toolsUsed, tc.Name)

			result, err := a.tools.dispatchTool(ctx, tc.Name, tc.Arguments)
			if err != nil {
				result = fmt.Sprintf(`{"error": %q}`, err.Error())
			}

			result = stripSecrets(result)

			messages = append(messages, Message{
				Role:       RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return &AskResponse{
		Answer:     "I reached the maximum number of tool rounds. Here's what I found so far based on the data gathered.",
		ToolsUsed:  toolsUsed,
		TokensUsed: totalTokens,
	}, nil
}

// AskStream processes a question with streaming output.
func (a *Assistant) AskStream(ctx context.Context, req AskRequest, send func(SSEEvent)) error {
	if a.rateLimiter != nil && !a.rateLimiter.allow() {
		return fmt.Errorf("rate limit exceeded (%d requests/minute)", a.cfg.RateLimit)
	}

	aiRequestsTotal.Inc()
	start := time.Now()
	defer func() {
		aiRequestDuration.Observe(time.Since(start).Seconds())
	}()

	messages := []Message{
		{Role: RoleSystem, Content: a.systemPrompt},
	}

	question := req.Question
	if req.Context.Thrall != "" {
		question = fmt.Sprintf("[Context: thrall=%s] %s", req.Context.Thrall, question)
	}

	mode := a.cfg.ContextMode
	if mode == "auto" || mode == "prefetch" {
		prefetched := a.prefetchData(ctx, question, req.Context.Thrall)
		if prefetched != "" {
			question = question + "\n\n--- Prefetched Data ---\n" + prefetched
		}
	}

	// Add conversation history.
	for _, h := range req.History {
		role := RoleUser
		if h.Role == "assistant" {
			role = RoleAssistant
		}
		messages = append(messages, Message{Role: role, Content: h.Content})
	}

	messages = append(messages, Message{Role: RoleUser, Content: question})

	var tools []ToolDef
	if mode == "tools" || mode == "auto" {
		tools = toolDefs()
	}

	totalTokens := 0
	var toolsUsed []string

	for round := 0; round < a.cfg.MaxToolRounds; round++ {
		resp, err := a.provider.Chat(ctx, ChatRequest{
			Messages:  messages,
			Tools:     tools,
			MaxTokens: a.cfg.MaxTokens,
			Model:     a.cfg.Model,
		})
		if err != nil {
			aiErrorsTotal.Inc()
			return fmt.Errorf("LLM chat failed: %w", err)
		}
		totalTokens += resp.TokensUsed
		aiTokensTotal.Add(float64(resp.TokensUsed))

		if len(resp.ToolCalls) == 0 {
			send(SSEEvent{Type: "delta", Text: resp.Content})
			send(SSEEvent{Type: "done", ToolsUsed: toolsUsed, TokensUsed: totalTokens})
			return nil
		}

		var toolNames []string
		for _, tc := range resp.ToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		send(SSEEvent{Type: "status", Status: "thinking", ToolsCalled: toolNames})

		assistantMsg := Message{
			Role:      RoleAssistant,
			ToolCalls: resp.ToolCalls,
			Content:   resp.Content,
		}
		messages = append(messages, assistantMsg)

		for _, tc := range resp.ToolCalls {
			aiToolCallsTotal.WithLabelValues(tc.Name).Inc()
			toolsUsed = append(toolsUsed, tc.Name)

			result, err := a.tools.dispatchTool(ctx, tc.Name, tc.Arguments)
			if err != nil {
				result = fmt.Sprintf(`{"error": %q}`, err.Error())
			}
			result = stripSecrets(result)

			messages = append(messages, Message{
				Role:       RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	send(SSEEvent{Type: "delta", Text: "I reached the maximum number of tool rounds."})
	send(SSEEvent{Type: "done", ToolsUsed: toolsUsed, TokensUsed: totalTokens})
	return nil
}

// prefetchData extracts entities from the question and pre-fetches relevant data.
func (a *Assistant) prefetchData(ctx context.Context, question, thrallHint string) string {
	var parts []string
	lower := strings.ToLower(question)

	hostname := thrallHint
	if hostname == "" {
		hostname = extractHostname(question)
	}
	if hostname != "" {
		if data, err := a.tools.getThrall(ctx, map[string]any{"hostname": hostname}); err == nil {
			parts = append(parts, "## Thrall: "+hostname+"\n"+data)
		}
		if data, err := a.tools.getTraits(ctx, map[string]any{"hostname": hostname}); err == nil {
			if len(data) > 4000 {
				data = data[:4000] + "\n...(truncated)"
			}
			parts = append(parts, "## Traits\n"+data)
		}
		if data, err := a.tools.listRuns(ctx, map[string]any{"hostname": hostname, "limit": float64(5)}); err == nil {
			parts = append(parts, "## Recent Runs\n"+data)
		}
		if data, err := a.tools.getConfig(ctx, map[string]any{"hostname": hostname}); err == nil {
			parts = append(parts, "## Config\n"+data)
		}
	}

	if containsAny(lower, "compliance", "compliant", "drifted", "failing", "stale") {
		if data, err := a.tools.getCompliance(ctx, map[string]any{}); err == nil {
			parts = append(parts, "## Compliance Summary\n"+data)
		}
	}

	if containsAny(lower, "module", "modules") {
		if data, err := a.tools.listModules(); err == nil {
			parts = append(parts, "## Modules\n"+data)
		}
	}
	if containsAny(lower, "role", "roles") {
		if data, err := a.tools.listRoles(); err == nil {
			parts = append(parts, "## Roles\n"+data)
		}
	}

	if containsAny(lower, "thrall", "thralls", "node", "nodes", "fleet", "how many") {
		if data, err := a.tools.listThralls(ctx, map[string]any{"limit": float64(20)}); err == nil {
			parts = append(parts, "## Thralls\n"+data)
		}
	}

	if containsAny(lower, "package", "drift", "version") {
		if data, err := a.tools.listPackageDrift(ctx, nil); err == nil {
			parts = append(parts, "## Package Drift\n"+data)
		}
	}

	return strings.Join(parts, "\n\n")
}

func extractHostname(question string) string {
	words := strings.Fields(question)
	for _, w := range words {
		w = strings.Trim(w, "\"'?,.")
		if (strings.Contains(w, ".") || strings.Contains(w, "-")) &&
			len(w) > 3 && !isCommonWord(w) {
			return w
		}
	}
	return ""
}

func isCommonWord(w string) bool {
	common := map[string]bool{
		"don't": true, "can't": true, "won't": true, "isn't": true,
		"what's": true, "that's": true, "it's": true, "i'm": true,
		"e.g.": true, "i.e.": true, "etc.": true, "vs.": true,
	}
	return common[strings.ToLower(w)]
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func hasEmptyToolCalls(calls []ToolCall) bool {
	for _, c := range calls {
		if c.Name == "" {
			return true
		}
	}
	return false
}

func stripSecrets(s string) string {
	s = strings.ReplaceAll(s, "secret:", "[REDACTED:")
	return s
}

// SSEEvent is a server-sent event for streaming responses.
type SSEEvent struct {
	Type        string   `json:"type"`
	Text        string   `json:"text,omitempty"`
	Status      string   `json:"status,omitempty"`
	ToolsCalled []string `json:"tools_called,omitempty"`
	ToolsUsed   []string `json:"tools_used,omitempty"`
	TokensUsed  int      `json:"tokens_used,omitempty"`
}

// --- rate limiter ---

type rateLimiter struct {
	mu       sync.Mutex
	tokens   int
	maxRate  int
	lastTick time.Time
}

func newRateLimiter(requestsPerMinute int) *rateLimiter {
	return &rateLimiter{
		tokens:   requestsPerMinute,
		maxRate:  requestsPerMinute,
		lastTick: time.Now(),
	}
}

func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(r.lastTick)
	if elapsed >= time.Minute {
		r.tokens = r.maxRate
		r.lastTick = now
	}

	if r.tokens <= 0 {
		return false
	}
	r.tokens--
	return true
}

// ProviderName returns the name of the underlying LLM provider.
func (a *Assistant) ProviderName() string {
	return a.provider.Name()
}

// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ai

import (
	"context"
)

// mockProvider is a test Provider that returns pre-configured responses.
type mockProvider struct {
	responses []*ChatResponse
	calls     []ChatRequest
	callIdx   int
}

func newMockProvider(responses ...*ChatResponse) *mockProvider {
	return &mockProvider{responses: responses}
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	m.calls = append(m.calls, req)
	if m.callIdx >= len(m.responses) {
		return &ChatResponse{Content: "no more responses configured"}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

func (m *mockProvider) ChatStream(_ context.Context, req ChatRequest) (EventStream, error) {
	resp, err := m.Chat(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return &singleEventStream{resp: resp}, nil
}

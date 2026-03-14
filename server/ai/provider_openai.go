// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

// OpenAIProvider wraps OpenAICompatProvider targeting the official OpenAI API.
type OpenAIProvider struct {
	*OpenAICompatProvider
}

// NewOpenAIProvider creates an OpenAI provider.
func NewOpenAIProvider(apiKey string) *OpenAIProvider {
	p := &OpenAIProvider{
		OpenAICompatProvider: NewOpenAICompatProvider("https://api.openai.com/v1", apiKey),
	}
	p.OpenAICompatProvider.name = "openai"
	return p
}

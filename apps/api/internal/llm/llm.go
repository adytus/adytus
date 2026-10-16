// Package llm abstracts chat completion so OpenRouter can be swapped for a
// local Ollama instance during development.
package llm

import (
	"context"
	"slices"
)

type Message struct {
	Role    string `json:"role" validate:"required,oneof=system user assistant"`
	Content string `json:"content" validate:"required"`
}

type ChatRequest struct {
	Model    string    `json:"model" validate:"required"`
	Messages []Message `json:"messages" validate:"required,min=1,dive"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CostMicros is the upstream cost in micro-USD when the provider reports
	// it (OpenRouter does); zero otherwise.
	CostMicros int64 `json:"cost_micros"`
}

type ChatResponse struct {
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
}

type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// Whitelist restricts which models users may call. Models are picked so that
// they are served by providers with no training and no data retention.
type Whitelist []string

func (w Whitelist) Allows(model string) bool { return slices.Contains(w, model) }

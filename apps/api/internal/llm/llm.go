// Package llm abstracts chat completion so OpenRouter can be swapped for a
// local Ollama instance during development.
package llm

import (
	"context"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	// Model is the upstream model name, not the catalog ID.
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CostMicros is the upstream cost in micro-USD when the provider reports
	// it (OpenRouter does); zero otherwise.
	CostMicros int64 `json:"cost_micros"`
	// Reported is false when the stream ended without a usage block (e.g. the
	// client disconnected and the request was cancelled).
	Reported bool `json:"-"`
}

type ChatResponse struct {
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
}

type Provider interface {
	// Stream sends a chat request and calls onDelta for each content chunk.
	// If onDelta returns an error the request is cancelled; the partial
	// response is still returned alongside that error.
	Stream(ctx context.Context, req ChatRequest, onDelta func(string) error) (*ChatResponse, error)
}

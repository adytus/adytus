// Package llm holds the model catalog, token estimates and the Eino chat
// model for OpenRouter or a local Ollama. Billing wraps the model in
// internal/meter; nothing here touches credit.
package llm

// Usage is what one model call consumed.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	// CostMicros is the upstream cost in micro-USD when the provider reports
	// it (OpenRouter does); zero otherwise.
	CostMicros int64
}

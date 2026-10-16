package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// OpenAICompatible talks to any /chat/completions endpoint. OpenRouter and
// Ollama both expose one; they differ only in the extra request fields.
type OpenAICompatible struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
	// Extra is merged into every request body.
	Extra map[string]any
}

// NewOpenRouter enforces private routing on every request: providers that
// collect data are excluded and only zero-data-retention endpoints are used.
func NewOpenRouter(baseURL, apiKey string) *OpenAICompatible {
	return &OpenAICompatible{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 5 * time.Minute},
		Extra: map[string]any{
			"provider": map[string]any{
				"data_collection": "deny",
				"zdr":             true,
			},
			"usage": map[string]any{"include": true},
		},
	}
}

func NewOllama(baseURL string) *OpenAICompatible {
	return &OpenAICompatible{BaseURL: baseURL, Client: &http.Client{Timeout: 5 * time.Minute}}
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
}

func (p *OpenAICompatible) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body := map[string]any{"model": req.Model, "messages": req.Messages}
	for k, v := range p.Extra {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := p.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm: upstream status %d: %s", resp.StatusCode, msg)
	}

	var out completionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: no choices in response")
	}
	return &ChatResponse{
		Content: out.Choices[0].Message.Content,
		Usage: Usage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			CostMicros:       int64(math.Round(out.Usage.Cost * 1e6)),
		},
	}, nil
}

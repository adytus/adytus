package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatible talks to any streaming /chat/completions endpoint.
// OpenRouter and Ollama both expose one; they differ only in the extra
// request fields.
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

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAICompatible) Stream(ctx context.Context, req ChatRequest, onDelta func(string) error) (*ChatResponse, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	body := map[string]any{
		"model":          req.Model,
		"messages":       req.Messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
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

	out := &ChatResponse{}
	var content strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Skip blank lines and SSE comments (OpenRouter sends keep-alives).
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			out.Content = content.String()
			return out, fmt.Errorf("llm: upstream error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			out.Usage = Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				CostMicros:       int64(math.Ceil(chunk.Usage.Cost * 1e6)),
				Reported:         true,
			}
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			delta := chunk.Choices[0].Delta.Content
			content.WriteString(delta)
			if err := onDelta(delta); err != nil {
				out.Content = content.String()
				return out, err
			}
		}
	}
	out.Content = content.String()
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// NewChatModel returns an Eino chat model for an OpenAI-compatible endpoint.
// The upstream model name and output limit are set per call (see meter), so
// one instance serves the whole catalog.
func NewChatModel(ctx context.Context, provider, baseURL, apiKey string) (model.ToolCallingChatModel, error) {
	if apiKey == "" {
		// Ollama ignores the key, but the client refuses to send without one.
		apiKey = "unused"
	}
	cfg := &openai.ChatModelConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: costTransport{http.DefaultTransport},
		},
	}
	if provider == "openrouter" {
		// Private routing on every request: providers that collect data are
		// excluded and only zero-data-retention endpoints are used.
		cfg.ExtraFields = map[string]any{
			"provider": map[string]any{"data_collection": "deny", "zdr": true},
			"usage":    map[string]any{"include": true},
		}
	}
	return openai.NewChatModel(ctx, cfg)
}

// CostSink receives the upstream-reported cost of a call. Eino's OpenAI
// adapter drops fields it doesn't know, so OpenRouter's usage.cost is read
// off the raw stream by costTransport.
type CostSink struct{ micros atomic.Int64 }

func (s *CostSink) Micros() int64 { return s.micros.Load() }

type costKey struct{}

// WithCostSink attaches a sink that collects the cost of requests made with
// the returned context.
func WithCostSink(ctx context.Context) (context.Context, *CostSink) {
	s := &CostSink{}
	return context.WithValue(ctx, costKey{}, s), s
}

type costTransport struct{ base http.RoundTripper }

func (t costTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if sink, ok := r.Context().Value(costKey{}).(*CostSink); ok && err == nil {
		resp.Body = &costReader{ReadCloser: resp.Body, sink: sink}
	}
	return resp, err
}

// costReader passes the body through while scanning complete SSE lines for
// a usage block with a cost.
type costReader struct {
	io.ReadCloser
	sink *CostSink
	line []byte
}

func (c *costReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.line = append(c.line, p[:n]...)
	for {
		i := bytes.IndexByte(c.line, '\n')
		if i < 0 {
			break
		}
		c.scan(c.line[:i])
		c.line = c.line[i+1:]
	}
	if err != nil && len(c.line) > 0 {
		c.scan(c.line) // non-streaming body without a trailing newline
		c.line = nil
	}
	return n, err
}

func (c *costReader) scan(line []byte) {
	line = bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data: "))
	if !bytes.Contains(line, []byte(`"cost"`)) {
		return
	}
	var body struct {
		Usage *struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &body) == nil && body.Usage != nil && body.Usage.Cost > 0 {
		c.sink.micros.Store(int64(math.Ceil(body.Usage.Cost * 1e6)))
	}
}

package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var testModel = Model{
	ID: "m", Upstream: "up", PromptUSDPerMTok: 0.5, CompletionUSDPerMTok: 1.5,
	ContextTokens: 200, MaxOutputTokens: 50,
}

func TestCost(t *testing.T) {
	// 1000 * 0.5 + 100 * 1.5 = 650 micro-USD
	if got := testModel.Cost(1000, 100); got != 650 {
		t.Fatalf("cost = %d", got)
	}
	// Fractions round up.
	if got := testModel.Cost(1, 0); got != 1 {
		t.Fatalf("cost = %d", got)
	}
	if got := testModel.ChargeFor(Usage{PromptTokens: 1000, CostMicros: 7}); got != 7 {
		t.Fatalf("reported cost should win, got %d", got)
	}
}

func TestFitContextDropsOldest(t *testing.T) {
	long := strings.Repeat("x", 210) // 70 tokens + 4 overhead
	msgs := []*schema.Message{
		schema.SystemMessage("be brief"),
		schema.UserMessage(long),
		schema.AssistantMessage(long, nil),
		schema.UserMessage("latest"),
	}
	kept, est, err := FitContext(testModel, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if kept[0].Role != schema.System || kept[len(kept)-1].Content != "latest" {
		t.Fatalf("unexpected kept: %+v", kept)
	}
	if len(kept) != 4-1 {
		t.Fatalf("expected oldest dropped, kept %d", len(kept))
	}
	if est+testModel.MaxOutputTokens > testModel.ContextTokens {
		t.Fatalf("estimate %d overflows context", est)
	}
}

func TestFitContextTooLong(t *testing.T) {
	_, _, err := FitContext(testModel, []*schema.Message{schema.UserMessage(strings.Repeat("x", 1000))})
	if !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("expected ErrPromptTooLong, got %v", err)
	}
}

func TestImagesCountTowardsEstimate(t *testing.T) {
	vision := testModel
	vision.ImageTokens = 40
	msg := &schema.Message{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "abc"},
		{Type: schema.ChatMessagePartTypeImageURL},
	}}
	if got := vision.EstimateMessages([]*schema.Message{msg}); got != perRequestTokens+perMessageTokens+1+40 {
		t.Fatalf("estimate = %d", got)
	}
}

// The Eino OpenAI adapter streams deltas and usage; costTransport adds
// OpenRouter's reported cost, which the adapter itself drops.
func TestChatModelStreamsWithUsageAndCost(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": OPENROUTER PROCESSING\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[{"index":0,"delta":{"content":"lo"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14,"cost":0.0000105}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cm, err := NewChatModel(context.Background(), "openrouter", srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, sink := WithCostSink(context.Background())
	sr, err := cm.Stream(ctx, []*schema.Message{schema.UserMessage("hi")}, model.WithModel("up"), model.WithMaxTokens(5))
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage *schema.TokenUsage
	for {
		m, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(m.Content)
		if m.ResponseMeta != nil && m.ResponseMeta.Usage != nil {
			usage = m.ResponseMeta.Usage
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("content %q", text.String())
	}
	if usage == nil || usage.PromptTokens != 12 || usage.CompletionTokens != 2 {
		t.Fatalf("usage %+v", usage)
	}
	if sink.Micros() != 11 {
		t.Fatalf("cost = %d", sink.Micros())
	}
	for _, want := range []string{`"model":"up"`, `"max_tokens":5`, `"zdr":true`, `"include_usage":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("request missing %s: %s", want, body)
		}
	}
}

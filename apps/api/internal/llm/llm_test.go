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
	// The catalog price is what users see, so a lower upstream cost doesn't win.
	if got := testModel.ChargeFor(Usage{PromptTokens: 1000, CostMicros: 7}); got != 500 {
		t.Fatalf("catalog price should win, got %d", got)
	}
	if got := testModel.ChargeFor(Usage{PromptTokens: 1000, CostMicros: 900}); got != 900 {
		t.Fatalf("a higher upstream cost should win, got %d", got)
	}
}

func TestCatalogSingleModel(t *testing.T) {
	a := Model{ID: "a", Provider: Ollama, Upstream: "up", ContextTokens: 200, MaxOutputTokens: 50}
	b := Model{ID: "b", Provider: Ollama, Upstream: "up", ContextTokens: 200, MaxOutputTokens: 50}
	cat, err := NewCatalog([]Model{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Single() != "" {
		t.Fatalf("single on by default: %q", cat.Single())
	}
	if got := cat.WithSingle("b").Single(); got != "b" {
		t.Fatalf("single = %q, want b", got)
	}
	// An unknown or empty id turns the mode off.
	if got := cat.WithSingle("nope").Single(); got != "" {
		t.Fatalf("single = %q, want off", got)
	}
	if got := cat.WithSingle("").Single(); got != "" {
		t.Fatalf("single = %q, want off", got)
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

// OpenRouter offers keep its ranking, take the cheapest ZDR price, and drop
// per-request routers.
func TestOpenRouterOffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if r.URL.Query().Get("sort") != "top-weekly" {
				t.Errorf("sort = %q", r.URL.Query().Get("sort"))
			}
			fmt.Fprint(w, `{"data":[
				{"id":"b/top","name":"Top","description":"First. Second.","context_length":1000,
				 "architecture":{"input_modalities":["text","image"]},"pricing":{"prompt":"0.000001","completion":"0.000002"},
				 "top_provider":{"max_completion_tokens":500},"supported_parameters":["tools"]},
				{"id":"openrouter/auto","name":"Auto","pricing":{"prompt":"-1","completion":"-1"}},
				{"id":"a/open","name":"Open","context_length":100,"pricing":{"prompt":"0","completion":"0"}}]}`)
		case "/endpoints/zdr":
			fmt.Fprint(w, `{"data":[
				{"model_id":"b/top","pricing":{"prompt":"0.000003","completion":"0.000004"}},
				{"model_id":"b/top","pricing":{"prompt":"0.0000015","completion":"0.0000025"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := Offers(context.Background(), Provider{ID: OpenRouter, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Upstream != "b/top" || got[1].Upstream != "a/open" {
		t.Fatalf("offers = %+v", got)
	}
	top := got[0]
	if !top.ZDR || !top.Vision || !top.Tools || top.Description != "First." || top.MaxOutputTokens != 500 {
		t.Fatalf("top = %+v", top)
	}
	if fmt.Sprintf("%.2f/%.2f", top.PromptUSDPerMTok, top.CompletionUSDPerMTok) != "1.50/2.50" {
		t.Fatalf("top price = %v/%v", top.PromptUSDPerMTok, top.CompletionUSDPerMTok)
	}
	if got[1].ZDR || got[1].Tools {
		t.Fatalf("open = %+v", got[1])
	}
}

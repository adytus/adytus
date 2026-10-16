package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	msgs := []Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: long},
		{Role: "assistant", Content: long},
		{Role: "user", Content: "latest"},
	}
	kept, est, err := FitContext(testModel, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if kept[0].Role != "system" || kept[len(kept)-1].Content != "latest" {
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
	_, _, err := FitContext(testModel, []Message{{Role: "user", Content: strings.Repeat("x", 1000)}})
	if !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("expected ErrPromptTooLong, got %v", err)
	}
}

func TestStreamParsesDeltasAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, ": OPENROUTER PROCESSING\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"lo"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"cost":0.0000105}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var got strings.Builder
	resp, err := NewOllama(srv.URL).Stream(context.Background(), ChatRequest{Model: "x"}, func(d string) error {
		got.WriteString(d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "Hello" || resp.Content != "Hello" {
		t.Fatalf("content %q / %q", got.String(), resp.Content)
	}
	if !resp.Usage.Reported || resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 2 || resp.Usage.CostMicros != 11 {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestStreamStopsWhenCallbackFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 5 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		}
	}))
	defer srv.Close()

	stop := errors.New("client gone")
	n := 0
	resp, err := NewOllama(srv.URL).Stream(context.Background(), ChatRequest{}, func(string) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || resp.Content != "aa" || resp.Usage.Reported {
		t.Fatalf("err=%v content=%q usage=%+v", err, resp.Content, resp.Usage)
	}
}

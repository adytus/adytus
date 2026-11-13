package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNearAIOffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/list" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[
			{"modelId":"vendor/proxied","inputCostPerToken":{"amount":3000,"scale":9},"outputCostPerToken":{"amount":15000,"scale":9},
			 "metadata":{"verifiable":false,"contextLength":200000,"modelDisplayName":"Proxied",
			  "architecture":{"inputModalities":["text"],"outputModalities":["text"]},"supportedFeatures":["tools"]}},
			{"modelId":"Qwen/Private","inputCostPerToken":{"amount":40,"scale":9},"outputCostPerToken":{"amount":1350,"scale":9},
			 "metadata":{"verifiable":true,"contextLength":262144,"maxOutputLength":65536,"modelDisplayName":"Private",
			  "modelDescription":"Runs in a TEE. More text.",
			  "architecture":{"inputModalities":["text","image"],"outputModalities":["text"]},"supportedFeatures":["tools","reasoning"]}},
			{"modelId":"Qwen/Embed","inputCostPerToken":{"amount":10,"scale":9},"outputCostPerToken":{"amount":10,"scale":9},
			 "metadata":{"verifiable":true,"architecture":{"inputModalities":["text"],"outputModalities":["embedding"]}}},
			{"modelId":"openai/whisper","inputCostPerToken":{"amount":10,"scale":9},"outputCostPerToken":{"amount":10,"scale":9},
			 "metadata":{"verifiable":true,"architecture":{"inputModalities":["audio"],"outputModalities":["text"]}}}]}`)
	}))
	defer srv.Close()

	got, err := Offers(context.Background(), Provider{ID: NearAI, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Upstream != "Qwen/Private" || got[1].Upstream != "vendor/proxied" {
		t.Fatalf("offers = %+v", got)
	}
	p := got[0]
	if !p.Verifiable || !p.ZDR || !p.Vision || !p.Tools || p.Description != "Runs in a TEE." ||
		p.ContextTokens != 262144 || p.MaxOutputTokens != 65536 {
		t.Fatalf("private = %+v", p)
	}
	if fmt.Sprintf("%.2f/%.2f", p.PromptUSDPerMTok, p.CompletionUSDPerMTok) != "0.04/1.35" {
		t.Fatalf("private price = %v/%v", p.PromptUSDPerMTok, p.CompletionUSDPerMTok)
	}
	if got[1].Verifiable || got[1].Vision || got[1].PromptUSDPerMTok != 3 || got[1].CompletionUSDPerMTok != 15 {
		t.Fatalf("proxied = %+v", got[1])
	}
}

func TestNearAIProbeChecksKey(t *testing.T) {
	const good = "sk-good"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			if r.Header.Get("Authorization") != "Bearer "+good {
				http.Error(w, `{"error":{"type":"invalid_api_key"}}`, http.StatusUnauthorized)
				return
			}
			http.Error(w, `{"error":{"type":"invalid_request"}}`, http.StatusBadRequest)
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"b"},{"id":"a"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ids, err := Probe(context.Background(), Provider{ID: NearAI, BaseURL: srv.URL, APIKey: good})
	if err != nil || strings.Join(ids, ",") != "a,b" {
		t.Fatalf("good key: ids = %v, err = %v", ids, err)
	}
	if _, err := Probe(context.Background(), Provider{ID: NearAI, BaseURL: srv.URL, APIKey: "sk-bad"}); err == nil ||
		!strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("bad key: err = %v", err)
	}
	if _, err := Probe(context.Background(), Provider{ID: NearAI, BaseURL: srv.URL}); err == nil {
		t.Fatal("no key: want an error")
	}
}

func TestNearAINeedsKey(t *testing.T) {
	cat, err := NewCatalog([]Model{{ID: "n", Provider: NearAI, Upstream: "Qwen/Private", ContextTokens: 100, MaxOutputTokens: 10}})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(DialOpenAI)
	if _, err := r.Build(context.Background(), cat, []Provider{{ID: NearAI, BaseURL: "https://x/v1"}}); err == nil ||
		!strings.Contains(err.Error(), "no API key") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Build(context.Background(), cat, []Provider{{ID: NearAI, BaseURL: "https://x/v1", APIKey: "k"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateInferenceIsNearAIOnly(t *testing.T) {
	m := Model{ID: "p", Provider: NearAI, Upstream: "Qwen/Private", ContextTokens: 100, MaxOutputTokens: 10, PrivateInference: true}
	if _, err := NewCatalog([]Model{m}); err != nil {
		t.Fatalf("NEAR AI model with private inference: %v", err)
	}
	m.Provider = OpenRouter
	if _, err := NewCatalog([]Model{m}); err == nil {
		t.Fatal("OpenRouter model accepted private inference")
	}
}

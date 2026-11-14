package attest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NearAI fetches signatures from NEAR AI Cloud's private-inference API.
type NearAI struct {
	BaseURL string // e.g. https://cloud-api.near.ai/v1
	APIKey  string
	HTTP    *http.Client
}

func NewNearAI(baseURL, apiKey string) *NearAI {
	return &NearAI{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Signature asks for the signature of a completion. model is the upstream
// model name the completion was made with.
func (n *NearAI) Signature(ctx context.Context, completionID, model string) (Signature, error) {
	u := fmt.Sprintf("%s/signature/%s?model=%s&signing_algo=ecdsa",
		n.BaseURL, url.PathEscape(completionID), url.QueryEscape(model))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Signature{}, err
	}
	if n.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+n.APIKey)
	}
	res, err := n.HTTP.Do(req)
	if err != nil {
		return Signature{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Signature{}, fmt.Errorf("signature: http %d", res.StatusCode)
	}
	var s Signature
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return Signature{}, fmt.Errorf("signature: %w", err)
	}
	if s.Text == "" || s.Signature == "" {
		return Signature{}, fmt.Errorf("signature: empty answer")
	}
	return s, nil
}

// SignatureWithRetry retries a few times, since a signature can lag the end
// of its stream by a moment.
func (n *NearAI) SignatureWithRetry(ctx context.Context, completionID, model string) (Signature, error) {
	var err error
	for attempt := range 4 {
		var s Signature
		if s, err = n.Signature(ctx, completionID, model); err == nil {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return Signature{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return Signature{}, err
}

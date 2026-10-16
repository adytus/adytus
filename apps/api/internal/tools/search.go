package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Search queries a SearXNG instance. SearXNG forwards queries to public
// engines without cookies or user identifiers; the query text itself does
// leave the server.
type Search struct {
	BaseURL string
	Client  *http.Client
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func NewSearch(baseURL string) *Search {
	return &Search{BaseURL: baseURL, Client: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Search) Query(ctx context.Context, q string, n int) ([]SearchResult, error) {
	if q == "" {
		return nil, errors.New("empty query")
	}
	u := s.BaseURL + "/search?" + url.Values{"q": {q}, "format": {"json"}, "safesearch": {"1"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search unavailable: status %d", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	out := make([]SearchResult, 0, n)
	for _, r := range body.Results {
		if len(out) == n {
			break
		}
		out = append(out, SearchResult{r.Title, r.URL, truncate(r.Content, 400)})
	}
	return out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

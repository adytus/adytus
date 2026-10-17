package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

var probeClient = &http.Client{Timeout: 15 * time.Second}

// Probe checks that a provider answers and accepts its key, and returns the
// upstream model names it offers.
func Probe(ctx context.Context, p Provider) ([]string, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	if p.ID == OpenRouter {
		// /models answers without a key, so check the key on its own.
		if err := probeGet(ctx, base+"/key", p.APIKey, nil); err != nil {
			return nil, err
		}
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := probeGet(ctx, base+"/models", p.APIKey, &list); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func probeGet(ctx context.Context, url, key string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s rejected the API key (%d)", url, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	case out == nil:
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: unexpected answer: %w", url, err)
	}
	return nil
}

package near

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Hermes reads a price from Pyth's Hermes service. NEAR has no Chainlink
// feed, and Pyth on NEAR itself is only as fresh as the last update someone
// paid to push, so the price is read off-chain from the signed feed instead.
type Hermes struct {
	URL  string
	HTTP *http.Client
}

func NewHermes(base string) *Hermes {
	return &Hermes{URL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// USD returns feed's price in micro-USD per whole unit. A price published
// longer ago than maxAge is refused, so a stalled feed can't price a payment.
func (h *Hermes) USD(ctx context.Context, feed string, maxAge time.Duration) (int64, error) {
	feed = strings.TrimPrefix(strings.ToLower(feed), "0x")
	u := h.URL + "/v2/updates/price/latest?parsed=true&encoding=hex&ids[]=" + url.QueryEscape(feed)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	res, err := h.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("price service: http %d", res.StatusCode)
	}
	var body struct {
		Parsed []struct {
			ID    string `json:"id"`
			Price struct {
				Price       string `json:"price"`
				Expo        int    `json:"expo"`
				PublishTime int64  `json:"publish_time"`
			} `json:"price"`
		} `json:"parsed"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("price service: %w", err)
	}
	for _, p := range body.Parsed {
		if strings.TrimPrefix(strings.ToLower(p.ID), "0x") != feed {
			continue
		}
		if time.Since(time.Unix(p.Price.PublishTime, 0)) > maxAge {
			return 0, errors.New("price feed is stale")
		}
		return scaleToMicros(p.Price.Price, p.Price.Expo)
	}
	return 0, errors.New("price service doesn't have that feed")
}

// scaleToMicros turns price × 10^expo into micro-USD, rounding down.
func scaleToMicros(price string, expo int) (int64, error) {
	n, err := strconv.ParseInt(price, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("price feed answered %q", price)
	}
	shift := expo + 6
	if shift > 18 || shift < -30 {
		return 0, fmt.Errorf("price feed exponent %d", expo)
	}
	v := big.NewInt(n)
	ten := big.NewInt(10)
	switch {
	case shift > 0:
		v.Mul(v, new(big.Int).Exp(ten, big.NewInt(int64(shift)), nil))
	case shift < 0:
		v.Quo(v, new(big.Int).Exp(ten, big.NewInt(int64(-shift)), nil))
	}
	if !v.IsInt64() || v.Sign() <= 0 {
		return 0, errors.New("price out of range")
	}
	return v.Int64(), nil
}

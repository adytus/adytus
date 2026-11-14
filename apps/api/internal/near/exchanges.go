package near

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExchangesSource is the price_url value that prices NEAR from public
// exchange tickers instead of a Pyth Hermes server. Pyth's public Hermes
// started refusing price requests without a key (HTTP 401), while these
// tickers answer without one.
const ExchangesSource = "exchanges"

// Pricer gives the price of NEAR in micro-USD.
type Pricer interface {
	USD(ctx context.Context, feed string, maxAge time.Duration) (int64, error)
}

// Ticker is one exchange's NEAR/USD spot price.
type Ticker struct {
	Name  string
	URL   string
	Parse func(body []byte) (float64, error)
}

// DefaultTickers are Coinbase, Kraken and CoinGecko, which need no key.
var DefaultTickers = []Ticker{
	{"Coinbase", "https://api.coinbase.com/v2/prices/NEAR-USD/spot", func(b []byte) (float64, error) {
		var v struct {
			Data struct {
				Amount string `json:"amount"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return 0, err
		}
		return strconv.ParseFloat(v.Data.Amount, 64)
	}},
	{"Kraken", "https://api.kraken.com/0/public/Ticker?pair=NEARUSD", func(b []byte) (float64, error) {
		var v struct {
			Error  []string `json:"error"`
			Result map[string]struct {
				C []string `json:"c"` // last trade: price, volume
			} `json:"result"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return 0, err
		}
		if len(v.Error) > 0 {
			return 0, errors.New(strings.Join(v.Error, "; "))
		}
		for _, r := range v.Result {
			if len(r.C) > 0 {
				return strconv.ParseFloat(r.C[0], 64)
			}
		}
		return 0, errors.New("no ticker")
	}},
	{"CoinGecko", "https://api.coingecko.com/api/v3/simple/price?ids=near&vs_currencies=usd&include_last_updated_at=true", func(b []byte) (float64, error) {
		var v struct {
			Near struct {
				USD     float64 `json:"usd"`
				Updated int64   `json:"last_updated_at"`
			} `json:"near"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return 0, err
		}
		if v.Near.Updated > 0 && time.Since(time.Unix(v.Near.Updated, 0)) > 15*time.Minute {
			return 0, errors.New("stale")
		}
		return v.Near.USD, nil
	}},
}

// maxSpread is how far apart the exchanges may be before no price is given.
const maxSpread = 0.03

// Exchanges takes the median of the tickers that answer. With two or more
// that disagree by more than 3% it refuses, so one broken or manipulated
// ticker can't set the price.
type Exchanges struct {
	Tickers []Ticker
	HTTP    *http.Client
}

func NewExchanges() *Exchanges {
	return &Exchanges{Tickers: DefaultTickers, HTTP: &http.Client{Timeout: 8 * time.Second}}
}

// USD ignores feed and maxAge: tickers are live.
func (e *Exchanges) USD(ctx context.Context, _ string, _ time.Duration) (int64, error) {
	type answer struct {
		name  string
		price float64
		err   error
	}
	out := make([]answer, len(e.Tickers))
	var wg sync.WaitGroup
	for i, t := range e.Tickers {
		wg.Go(func() {
			p, err := e.fetch(ctx, t)
			out[i] = answer{t.Name, p, err}
		})
	}
	wg.Wait()

	var prices []float64
	var failed []string
	for _, a := range out {
		if a.err != nil || !(a.price > 0) || math.IsInf(a.price, 0) {
			failed = append(failed, fmt.Sprintf("%s: %v", a.name, a.err))
			continue
		}
		prices = append(prices, a.price)
	}
	if len(prices) == 0 {
		return 0, fmt.Errorf("no exchange answered (%s)", strings.Join(failed, "; "))
	}
	sort.Float64s(prices)
	lo, hi := prices[0], prices[len(prices)-1]
	if len(prices) > 1 && (hi-lo)/lo > maxSpread {
		return 0, fmt.Errorf("exchanges disagree on NEAR: $%.4f to $%.4f", lo, hi)
	}
	var mid float64
	if n := len(prices); n%2 == 1 {
		mid = prices[n/2]
	} else {
		mid = (prices[n/2-1] + prices[n/2]) / 2
	}
	return int64(math.Floor(mid * 1e6)), nil
}

func (e *Exchanges) fetch(ctx context.Context, t Ticker) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := e.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return 0, err
	}
	return t.Parse(body)
}

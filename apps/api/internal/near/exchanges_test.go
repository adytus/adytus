package near

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// fakeTickers serves one price per ticker; a negative price is an outage.
func fakeTickers(t *testing.T, prices ...float64) []Ticker {
	var out []Ticker
	for i, p := range prices {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p < 0 {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			fmt.Fprintf(w, "%g", p)
		}))
		t.Cleanup(srv.Close)
		out = append(out, Ticker{Name: fmt.Sprintf("x%d", i), URL: srv.URL, Parse: func(b []byte) (float64, error) {
			return strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		}})
	}
	return out
}

func TestExchangesMedian(t *testing.T) {
	cases := []struct {
		prices []float64
		want   int64
		err    string
	}{
		{[]float64{3.40, 3.39, 3.41}, 3_400_000, ""},
		{[]float64{3.40, 3.42}, 3_410_000, ""},
		{[]float64{3.40, -1, -1}, 3_400_000, ""},     // one is enough
		{[]float64{3.40, 3.39, 4.00}, 0, "disagree"}, // one bad ticker can't set it
		{[]float64{-1, -1, -1}, 0, "no exchange answered"},
	}
	for _, c := range cases {
		e := &Exchanges{Tickers: fakeTickers(t, c.prices...), HTTP: http.DefaultClient}
		got, err := e.USD(context.Background(), "", 0)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v: got %d, %v; want error %q", c.prices, got, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%v: got %d, %v; want %d", c.prices, got, err, c.want)
		}
	}
}

func TestDefaultTickerParsers(t *testing.T) {
	bodies := map[string]string{
		"Coinbase":  `{"data":{"amount":"3.3947","base":"NEAR","currency":"USD"}}`,
		"Kraken":    `{"error":[],"result":{"NEARUSD":{"a":["3.39820","74","74.000"],"c":["3.39630","89.00000000"]}}}`,
		"CoinGecko": `{"near":{"usd":3.39}}`,
	}
	want := map[string]float64{"Coinbase": 3.3947, "Kraken": 3.3963, "CoinGecko": 3.39}
	for _, tk := range DefaultTickers {
		got, err := tk.Parse([]byte(bodies[tk.Name]))
		if err != nil || got != want[tk.Name] {
			t.Errorf("%s: %v, %v", tk.Name, got, err)
		}
	}
}

// TestExchangesLive asks the real exchanges (NEAR_LIVE=1).
func TestExchangesLive(t *testing.T) {
	if os.Getenv("NEAR_LIVE") == "" {
		t.Skip("NEAR_LIVE not set")
	}
	got, err := NewExchanges().USD(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NEAR = $%.4f", float64(got)/1e6)
}

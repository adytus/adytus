package near

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidAccountID(t *testing.T) {
	for _, ok := range []string{"pay.adytus.near", "adytus.testnet", "a1", "17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1", "my_app-1.near"} {
		if !ValidAccountID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "a", "Pay.near", "pay..near", ".near", "pay.near.", "pay--x.near", "has space.near", strings.Repeat("a", 65)} {
		if ValidAccountID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestScaleToMicros(t *testing.T) {
	cases := []struct {
		price string
		expo  int
		want  int64
	}{
		{"512345678", -8, 5_123_456}, // $5.12345678
		{"5", 0, 5_000_000},
		{"123", -6, 123},
		{"1", -9, 0},
	}
	for _, c := range cases {
		got, err := scaleToMicros(c.price, c.expo)
		if c.want == 0 {
			if err == nil {
				t.Errorf("%s e%d: want an error for a zero price, got %d", c.price, c.expo, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s e%d = %d, %v; want %d", c.price, c.expo, got, err, c.want)
		}
	}
	if _, err := scaleToMicros("-5", -8); err == nil {
		t.Error("negative price accepted")
	}
}

// rpc answers NEAR JSON-RPC calls from handlers keyed by method, or by
// "query:<request_type>[:<method_name>]" for queries.
func rpc(t *testing.T, handlers map[string]func(params map[string]any) (any, *rpcError)) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		key := req.Method
		if key == "query" {
			key += ":" + req.Params["request_type"].(string)
			if m, ok := req.Params["method_name"].(string); ok {
				key += ":" + m
			}
		}
		h, ok := handlers[key]
		if !ok {
			t.Errorf("unexpected call %s", key)
			http.Error(w, "no", 500)
			return
		}
		res, rerr := h(req.Params)
		out := map[string]any{"jsonrpc": "2.0", "id": "adytus"}
		if rerr != nil {
			out["error"] = rerr
		} else {
			out["result"] = res
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type rpcError struct {
	Name  string         `json:"name"`
	Cause map[string]any `json:"cause"`
}

// viewResult encodes v the way a node returns a view call: JSON as a byte array.
func viewResult(v any) map[string]any {
	raw, _ := json.Marshal(v)
	arr := make([]int, len(raw))
	for i, b := range raw {
		arr[i] = int(b)
	}
	return map[string]any{"result": arr, "logs": []string{}, "block_height": 1}
}

func TestClientReadsContract(t *testing.T) {
	srv := rpc(t, map[string]func(map[string]any) (any, *rpcError){
		"status": func(map[string]any) (any, *rpcError) { return map[string]any{"chain_id": "testnet"}, nil },
		"query:call_function:deposits_count": func(p map[string]any) (any, *rpcError) {
			if p["finality"] != "final" {
				t.Errorf("finality %v", p["finality"])
			}
			return viewResult(2), nil
		},
		"query:call_function:deposits": func(p map[string]any) (any, *rpcError) {
			args, _ := base64.StdEncoding.DecodeString(p["args_base64"].(string))
			if string(args) != `{"from_index":1,"limit":1}` {
				t.Errorf("args %s", args)
			}
			return viewResult([]Deposit{{Memo: "abc", Sender: "alice.testnet", Amount: "1000", BlockHeight: 7, TimestampMs: 1}}), nil
		},
		"query:call_function:ft_balance_of": func(map[string]any) (any, *rpcError) { return viewResult("2500000"), nil },
		"query:call_function:storage_balance_of": func(map[string]any) (any, *rpcError) {
			return viewResult(nil), nil
		},
		"query:view_account": func(map[string]any) (any, *rpcError) {
			return map[string]any{"amount": "3000000000000000000000000", "storage_usage": 512}, nil
		},
	})
	c := NewClient(srv.URL)
	ctx := context.Background()

	if id, err := c.ChainID(ctx); err != nil || id != "testnet" {
		t.Fatalf("chain id %q, %v", id, err)
	}
	if n, err := c.DepositCount(ctx, "pay.testnet"); err != nil || n != 2 {
		t.Fatalf("count %d, %v", n, err)
	}
	deps, err := c.Deposits(ctx, "pay.testnet", 1, 1)
	if err != nil || len(deps) != 1 || deps[0].Memo != "abc" || deps[0].Token != "" {
		t.Fatalf("deposits %+v, %v", deps, err)
	}
	if bal, err := c.TokenBalance(ctx, "usdc.testnet", "pay.testnet"); err != nil || bal.Int64() != 2_500_000 {
		t.Fatalf("token balance %v, %v", bal, err)
	}
	if reg, err := c.TokenRegistered(ctx, "usdc.testnet", "pay.testnet"); err != nil || reg {
		t.Fatalf("registered %v, %v", reg, err)
	}
	bal, storage, err := c.Balance(ctx, "pay.testnet")
	if err != nil || bal.String() != "3000000000000000000000000" || storage != 512 {
		t.Fatalf("balance %v %d, %v", bal, storage, err)
	}
}

func TestClientReportsErrors(t *testing.T) {
	srv := rpc(t, map[string]func(map[string]any) (any, *rpcError){
		"query:call_function:deposits_count": func(map[string]any) (any, *rpcError) {
			return nil, &rpcError{Name: "HANDLER_ERROR", Cause: map[string]any{"name": "UNKNOWN_ACCOUNT", "info": map[string]any{"requested_account_id": "nope.near"}}}
		},
		"query:call_function:tokens": func(map[string]any) (any, *rpcError) {
			// Older nodes report a failed view call inside the result.
			return map[string]any{"error": "wasm execution failed with error: MethodNotFound", "logs": []string{}}, nil
		},
	})
	c := NewClient(srv.URL)
	if _, err := c.DepositCount(context.Background(), "nope.near"); err == nil || !strings.Contains(err.Error(), "UNKNOWN_ACCOUNT") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.AcceptedTokens(context.Background(), "nope.near"); err == nil || !strings.Contains(err.Error(), "MethodNotFound") {
		t.Fatalf("got %v", err)
	}
}

func TestHermes(t *testing.T) {
	const feed = "c415de8d2eba7db216527dff4b60e8f3a5311c740dadb233e13e12547e226750"
	published := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/updates/price/latest" || r.URL.Query().Get("ids[]") != feed {
			t.Errorf("request %s", r.URL)
		}
		fmt.Fprintf(w, `{"parsed":[{"id":"%s","price":{"price":"512345678","conf":"1","expo":-8,"publish_time":%d}}]}`, feed, published.Unix())
	}))
	defer srv.Close()
	h := NewHermes(srv.URL + "/")
	got, err := h.USD(context.Background(), "0x"+strings.ToUpper(feed), time.Minute)
	if err != nil || got != 5_123_456 {
		t.Fatalf("got %d, %v", got, err)
	}
	published = time.Now().Add(-time.Hour)
	if _, err := h.USD(context.Background(), feed, time.Minute); err == nil {
		t.Fatal("stale price accepted")
	}
}

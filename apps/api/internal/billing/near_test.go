package billing

// The watcher tests need Postgres: set TEST_DATABASE_URL to a disposable
// database (its public schema is wiped), like the server tests.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/db"
	"github.com/reaganiwadha/adytus/apps/api/internal/near"
)

const (
	testFeed     = "c415de8d2eba7db216527dff4b60e8f3a5311c740dadb233e13e12547e226750"
	testContract = "pay.adytus.testnet"
	testUSDC     = "usdc.testnet"
)

func TestYoctoAndMicros(t *testing.T) {
	const rate = 5 * MicrosPerUSD // $5 per NEAR
	y := yoctoFor(10*MicrosPerUSD, rate)
	if want := new(big.Int).Mul(big.NewInt(2), near.YoctoPerNEAR); y.Cmp(want) != 0 {
		t.Fatalf("$10 at $5 = %s yocto, want 2 NEAR", y)
	}
	// A rate that doesn't divide evenly rounds the quote up, never short.
	y = yoctoFor(10*MicrosPerUSD, 3*MicrosPerUSD)
	if got := microsForYocto(y, 3*MicrosPerUSD); got.Int64() < 10*MicrosPerUSD {
		t.Fatalf("quote is short: %d micros", got.Int64())
	}
}

func validNear() NearSettings {
	return NearSettings{
		Network: "testnet", RPCURL: "https://rpc.testnet.example", Contract: testContract, USDC: testUSDC,
		PriceURL: "https://hermes.example", PriceFeed: testFeed, MaxPriceAgeSecs: 300, QuoteTTLSecs: 900,
		MinMicros: 5 * MicrosPerUSD, DustMicros: 100_000, ToleranceBps: 100, PollSecs: 5,
	}
}

func TestNearSettingsValidate(t *testing.T) {
	if err := validNear().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*NearSettings){
		"network":         func(s *NearSettings) { s.Network = "devnet" },
		"rpc":             func(s *NearSettings) { s.RPCURL = "ftp://x" },
		"contract":        func(s *NearSettings) { s.Contract = "Not An Account" },
		"on, no contract": func(s *NearSettings) { s.Enabled, s.Contract = true, "" },
		"usdc":            func(s *NearSettings) { s.USDC = "x" },
		"feed":            func(s *NearSettings) { s.PriceFeed = "abc" },
		"dust over min":   func(s *NearSettings) { s.DustMicros = s.MinMicros + 1 },
		"tolerance":       func(s *NearSettings) { s.ToleranceBps = 5_000 },
		"poll":            func(s *NearSettings) { s.PollSecs = 0 },
		"ttl":             func(s *NearSettings) { s.QuoteTTLSecs = 5 },
	}
	for name, mut := range bad {
		s := validNear()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Exchange pricing needs no feed.
	s := validNear()
	s.PriceURL, s.PriceFeed = "", ""
	s.normalize() // as SaveSettings and Check do
	if s.PriceURL != near.ExchangesSource {
		t.Errorf("an empty price service became %q", s.PriceURL)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("exchanges: %v", err)
	}
	// No USDC means NEAR only, which is fine.
	s = validNear()
	s.USDC = ""
	if err := s.Validate(); err != nil {
		t.Errorf("NEAR only: %v", err)
	}
}

// fakeNear is a NEAR node serving the deposit contract, plus a Hermes price.
type fakeNear struct {
	mu       sync.Mutex
	network  string
	deposits []near.Deposit
	price    int64 // 8 decimals
}

func (f *fakeNear) add(d near.Deposit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.TimestampMs == 0 {
		d.TimestampMs = time.Now().UnixMilli()
	}
	d.BlockHeight = int64(100 + len(f.deposits))
	f.deposits = append(f.deposits, d)
}

func (f *fakeNear) setPrice(p int64) {
	f.mu.Lock()
	f.price = p
	f.mu.Unlock()
}

func (f *fakeNear) serve(t *testing.T) *httptest.Server {
	view := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		arr := make([]int, len(raw))
		for i, b := range raw {
			arr[i] = int(b)
		}
		return map[string]any{"result": arr}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodGet { // Hermes
			fmt.Fprintf(w, `{"parsed":[{"id":"%s","price":{"price":"%d","expo":-8,"publish_time":%d}}]}`, testFeed, f.price, time.Now().Unix())
			return
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var res any
		switch req.Method {
		case "status":
			res = map[string]any{"chain_id": f.network}
		case "query":
			if req.Params["request_type"] == "view_account" {
				res = map[string]any{"amount": nearAmount(12, 50), "storage_usage": 20_000}
				break
			}
			var args map[string]any
			raw, _ := base64.StdEncoding.DecodeString(fmt.Sprint(req.Params["args_base64"]))
			_ = json.Unmarshal(raw, &args)
			switch req.Params["method_name"] {
			case "deposits_count":
				res = view(len(f.deposits))
			case "deposits":
				from, limit := int(args["from_index"].(float64)), int(args["limit"].(float64))
				res = view(f.deposits[from:min(from+limit, len(f.deposits))])
			case "ft_balance_of":
				res = view("7500000")
			default:
				t.Errorf("unexpected view %v", req.Params["method_name"])
			}
		default:
			t.Errorf("unexpected rpc %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "adytus", "result": res})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type nearEnv struct {
	n    *Near
	f    *fakeNear
	pool *pgxpool.Pool
	user uuid.UUID
	memo string
	s    NearSettings
}

func newNearEnv(t *testing.T) *nearEnv {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	reset, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reset.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	reset.Close()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, auth_verifier, kdf_salt, kdf_params, wrapped_mk)
		VALUES ('n@example.com', '\x00', '\x00', '{}', '\x00') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}

	f := &fakeNear{network: "testnet", price: 5_00000000} // $5
	srv := f.serve(t)
	n := &Near{
		DB: pool, Ledger: &Ledger{DB: pool},
		NewClient: func(string) *near.Client { return near.NewClient(srv.URL) },
		NewPrice:  func(NearSettings) near.Pricer { return near.NewHermes(srv.URL) },
	}
	s := validNear()
	s.Enabled = true
	if s, err = n.SaveSettings(ctx, s, user); err != nil {
		t.Fatal(err)
	}
	memo, err := n.Memo(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	return &nearEnv{n: n, f: f, pool: pool, user: user, memo: memo, s: s}
}

func (e *nearEnv) tick(t *testing.T) {
	t.Helper()
	if err := e.n.Tick(context.Background(), e.s); err != nil {
		t.Fatal(err)
	}
}

func (e *nearEnv) credit(t *testing.T) int64 {
	t.Helper()
	b, err := e.n.Ledger.Balance(context.Background(), e.user)
	if err != nil {
		t.Fatal(err)
	}
	return b.CreditMicros
}

func nearAmount(whole, hundredths int64) string {
	v := new(big.Int).Mul(big.NewInt(whole*100+hundredths), near.YoctoPerNEAR)
	return v.Quo(v, big.NewInt(100)).String()
}

func TestNearSettingsRefuseWrongNetwork(t *testing.T) {
	e := newNearEnv(t)
	s := e.s
	s.Network = "mainnet"
	if _, err := e.n.SaveSettings(context.Background(), s, e.user); err == nil {
		t.Fatal("saved mainnet settings against a testnet RPC")
	}
	// Off, the network isn't checked.
	s.Enabled = false
	if _, err := e.n.SaveSettings(context.Background(), s, e.user); err != nil {
		t.Fatal(err)
	}
}

func TestNearMemoIsStable(t *testing.T) {
	e := newNearEnv(t)
	again, err := e.n.Memo(context.Background(), e.user)
	if err != nil || again != e.memo || len(e.memo) != nearMemoLen {
		t.Fatalf("got %q, %v; first %q", again, err, e.memo)
	}
}

func TestNearUSDCDeposit(t *testing.T) {
	e := newNearEnv(t)
	e.f.add(near.Deposit{Memo: e.memo, Token: testUSDC, Sender: "alice.testnet", Amount: "7500000"})
	e.f.add(near.Deposit{Memo: "nobody", Token: testUSDC, Sender: "bob.testnet", Amount: "9000000"})
	e.f.add(near.Deposit{Memo: e.memo, Token: "fake-usdc.testnet", Sender: "eve.testnet", Amount: "9000000"})
	e.f.add(near.Deposit{Memo: e.memo, Token: testUSDC, Sender: "alice.testnet", Amount: "5000"}) // dust
	e.tick(t)
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("credit = %d", got)
	}
	e.tick(t) // nothing new
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("credit after a second tick = %d", got)
	}
	var read int64
	_ = e.pool.QueryRow(context.Background(), `SELECT next_index FROM near_cursors WHERE contract = $1`, testContract).Scan(&read)
	if read != 4 {
		t.Fatalf("cursor at %d, want 4", read)
	}
	var ignored int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM crypto_deposits WHERE chain = 'near' AND status = 'ignored'`).Scan(&ignored)
	if ignored != 1 {
		t.Fatalf("ignored rows = %d", ignored)
	}

	// Replaying the log from the start credits nothing twice.
	if _, err := e.pool.Exec(context.Background(), `UPDATE near_cursors SET next_index = 0`); err != nil {
		t.Fatal(err)
	}
	e.tick(t)
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("replay credited again: %d", got)
	}
}

func TestNearDepositAtLockedRate(t *testing.T) {
	e := newNearEnv(t)
	ctx := context.Background()
	q, err := e.n.Quote(ctx, e.user, 10*MicrosPerUSD)
	if err != nil {
		t.Fatal(err)
	}
	if q.RateMicros != 5*MicrosPerUSD || q.AmountYocto != nearAmount(2, 0) {
		t.Fatalf("quote %+v", q)
	}
	// The price doubles before the user pays.
	e.f.setPrice(10_00000000)
	e.n.rateMu.Lock()
	e.n.rateAt = time.Time{}
	e.n.rateMu.Unlock()

	// 0.5% short is inside the tolerance.
	e.f.add(near.Deposit{Memo: e.memo, Sender: "alice.testnet", Amount: nearAmount(1, 99)})
	e.tick(t)
	if got := e.credit(t); got != 10*MicrosPerUSD {
		t.Fatalf("credit = %d, want the quoted $10", got)
	}

	// With the quote used, the next payment is priced live ($10 per NEAR).
	e.f.add(near.Deposit{Memo: e.memo, Sender: "alice.testnet", Amount: nearAmount(1, 0)})
	e.tick(t)
	if got := e.credit(t); got != 20*MicrosPerUSD {
		t.Fatalf("credit = %d, want $20 total", got)
	}
}

func TestNearVoucher(t *testing.T) {
	e := newNearEnv(t)
	ctx := context.Background()
	v := &Vouchers{DB: e.pool}
	if err := v.CreateVoucher(ctx, "NEAR50", 50, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ClaimVoucher(ctx, e.user, "near50"); err != nil {
		t.Fatal(err)
	}
	e.f.add(near.Deposit{Memo: e.memo, Token: testUSDC, Sender: "alice.testnet", Amount: "5000000"})
	e.tick(t)
	if got := e.credit(t); got != 10*MicrosPerUSD {
		t.Fatalf("credit = %d, want $10 for $5 at 50%% off", got)
	}
	info, err := e.n.Info(ctx, e.user)
	if err != nil {
		t.Fatal(err)
	}
	if info.Voucher == nil || len(info.Deposits) != 1 || info.Deposits[0].Asset != "usdc" {
		t.Fatalf("info %+v", info)
	}
}

func TestNearOverview(t *testing.T) {
	e := newNearEnv(t)
	e.f.add(near.Deposit{Memo: e.memo, Token: testUSDC, Sender: "alice.testnet", Amount: "7500000"})
	e.f.add(near.Deposit{Memo: "nobody", Sender: "bob.testnet", Amount: nearAmount(1, 0)})
	e.tick(t)
	e.f.add(near.Deposit{Memo: e.memo, Sender: "alice.testnet", Amount: nearAmount(1, 0)})

	o, err := e.n.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o.BalanceError != "" {
		t.Fatal(o.BalanceError)
	}
	if o.Credited != 1 || o.CreditedMicros != 7_500_000 || o.NEARUSDMicros != 5*MicrosPerUSD ||
		o.NEARYocto != nearAmount(12, 50) || o.USDCMicros != 7_500_000 || o.Logged != 3 || o.Read != 2 {
		t.Fatalf("overview %+v", o)
	}
	// Newest first: not read yet, then the unknown memo, then the credit.
	want := []struct {
		status, email string
	}{{"pending", "n@example.com"}, {"unmatched", ""}, {"credited", "n@example.com"}}
	if len(o.Recent) != len(want) {
		t.Fatalf("recent %+v", o.Recent)
	}
	for i, w := range want {
		if p := o.Recent[i]; p.Status != w.status || p.Email != w.email || p.Index != int64(2-i) {
			t.Errorf("recent[%d] = %+v, want %s for %q", i, p, w.status, w.email)
		}
	}
}

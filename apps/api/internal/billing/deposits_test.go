package billing

// The watcher tests need Postgres: set TEST_DATABASE_URL to a disposable
// database (its public schema is wiped), like the server tests.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/base"
	"github.com/reaganiwadha/adytus/apps/api/internal/db"
)

func TestWeiAndMicros(t *testing.T) {
	const rate = 3_000 * MicrosPerUSD // $3000 per ETH
	wei := weiFor(5*MicrosPerUSD, rate)
	if got := microsFor(wei, rate); got.Int64() < 5*MicrosPerUSD {
		t.Fatalf("quote is short: %d micros", got.Int64())
	}
	// Exactly representable: $3000 buys 1 ETH.
	if got := weiFor(3_000*MicrosPerUSD, rate); got.Cmp(weiPerETH) != 0 {
		t.Fatalf("got %s wei", got)
	}
	if got := microsFor(weiPerETH, rate); got.Int64() != rate {
		t.Fatalf("got %d micros", got.Int64())
	}
}

// fakeChain is a JSON-RPC node with scripted state.
type fakeChain struct {
	mu       sync.Mutex
	head     int64
	balances map[string]*big.Int // lower-case address
	usdc     map[string]*big.Int // USDC balances, lower-case address
	logs     []base.Log
	// price is the feed answer (8 decimals) and updated its updatedAt.
	price   int64
	updated time.Time
}

func newFakeChain() *fakeChain {
	return &fakeChain{head: 1000, balances: map[string]*big.Int{}, usdc: map[string]*big.Int{}, price: 3_000_00000000, updated: time.Now()}
}

func (f *fakeChain) word(n *big.Int) []byte {
	b := make([]byte, 32)
	n.FillBytes(b)
	return b
}

func (f *fakeChain) serve(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var result any
		switch req.Method {
		case "eth_chainId":
			result = "0x2105"
		case "eth_blockNumber":
			result = base.Quantity(f.head)
		case "eth_getBalance":
			var addr string
			_ = json.Unmarshal(req.Params[0], &addr)
			bal := f.balances[strings.ToLower(addr)]
			if bal == nil {
				bal = new(big.Int)
			}
			result = "0x" + bal.Text(16)
		case "eth_getLogs":
			var filter struct {
				Topics []json.RawMessage `json:"topics"`
				From   string            `json:"fromBlock"`
				To     string            `json:"toBlock"`
			}
			_ = json.Unmarshal(req.Params[0], &filter)
			var want []string
			_ = json.Unmarshal(filter.Topics[2], &want)
			from, _ := base.ParseQuantity(filter.From)
			to, _ := base.ParseQuantity(filter.To)
			out := []base.Log{}
			for _, l := range f.logs {
				bn, _ := base.ParseQuantity(l.BlockNumber)
				if bn.Cmp(from) < 0 || bn.Cmp(to) > 0 {
					continue
				}
				for _, a := range want {
					if strings.EqualFold(a, l.Topics[2]) {
						out = append(out, l)
					}
				}
			}
			result = out
		case "eth_call":
			var call struct {
				Data string `json:"data"`
			}
			_ = json.Unmarshal(req.Params[0], &call)
			if strings.HasPrefix(call.Data, "0x70a08231") {
				bal := f.usdc["0x"+strings.ToLower(call.Data[len(call.Data)-40:])]
				if bal == nil {
					bal = new(big.Int)
				}
				result = "0x" + hex.EncodeToString(f.word(bal))
				break
			}
			var out []byte
			out = append(out, f.word(big.NewInt(1))...)
			out = append(out, f.word(big.NewInt(f.price))...)
			out = append(out, f.word(big.NewInt(f.updated.Unix()))...)
			out = append(out, f.word(big.NewInt(f.updated.Unix()))...)
			out = append(out, f.word(big.NewInt(1))...)
			result = "0x" + hex.EncodeToString(out)
		default:
			http.Error(w, "unknown method "+req.Method, 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
}

func (f *fakeChain) usdcTransfer(to string, micros int64, block int64, tx string, idx int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, base.Log{
		Topics: []string{
			base.TransferTopic,
			"0x" + strings.Repeat("0", 24) + strings.Repeat("a", 40),
			"0x" + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(to, "0x")),
		},
		Data:        "0x" + fmt.Sprintf("%064x", micros),
		BlockNumber: base.Quantity(block),
		TxHash:      tx,
		LogIndex:    base.Quantity(int64(idx)),
	})
}

func (f *fakeChain) setBalance(addr string, wei *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balances[strings.ToLower(addr)] = wei
}

func (f *fakeChain) setHead(n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head = n
}

type depositEnv struct {
	d     *Deposits
	chain *fakeChain
	pool  *pgxpool.Pool
	user  uuid.UUID
	addr  string
}

func newDepositEnv(t *testing.T) *depositEnv {
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
		VALUES ('a@example.com', '\x00', '\x00', '{}', '\x00') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}

	chain := newFakeChain()
	srv := chain.serve(t)
	t.Cleanup(srv.Close)

	// The hardhat account xpub,.
	deriver, err := base.NewDeriver(testXpub)
	if err != nil {
		t.Fatal(err)
	}
	d := &Deposits{
		DB: pool, Ledger: &Ledger{DB: pool}, Chain: base.NewClient(srv.URL), Deriver: deriver,
		Cfg: DepositConfig{
			ChainID: 8453, USDC: "0xUSDC", ETHUSDFeed: "0xFEED", Confirmations: 10,
			QuoteTTL: 15 * time.Minute, MinMicros: 5 * MicrosPerUSD, DustMicros: 100_000,
			ToleranceBps: 100, Poll: time.Second, FullScanEvery: time.Hour,
			MaxFeedAge: time.Hour,
		},
	}
	addr, err := d.Address(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	return &depositEnv{d: d, chain: chain, pool: pool, user: user, addr: addr}
}

// testXpub is the m/44'/60'/0'/0 xpub of the hardhat test mnemonic.
const testXpub = "xpub6DyUKdwoLWmUJ4Tn9Bbsdtx7B5Ws18mEN19e5HT52ikE53FiUheSQXrZUNPovqfyKmw4579A1Mm3GXXKM39N64uooBfJ4tNAzFsEbodRTx4"

func (e *depositEnv) credit(t *testing.T) int64 {
	t.Helper()
	b, err := e.d.Ledger.Balance(context.Background(), e.user)
	if err != nil {
		t.Fatal(err)
	}
	return b.CreditMicros
}

func (e *depositEnv) tick(t *testing.T, head int64) {
	t.Helper()
	e.chain.setHead(head)
	if err := e.d.tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func TestAddressIsStable(t *testing.T) {
	e := newDepositEnv(t)
	again, err := e.d.Address(context.Background(), e.user)
	if err != nil || again != e.addr {
		t.Fatalf("got %q, %v; want %q", again, err, e.addr)
	}
	if !strings.HasPrefix(e.addr, "0x") || len(e.addr) != 42 {
		t.Fatalf("bad address %q", e.addr)
	}
}

func TestUSDCDeposit(t *testing.T) {
	e := newDepositEnv(t)
	e.tick(t, 1000) // first start: cursor at 990

	e.chain.usdcTransfer(e.addr, 7_500_000, 995, "0xtx1", 3)
	e.chain.usdcTransfer(e.addr, 7_500_000, 995, "0xtx1", 3) // duplicate log: same transfer
	e.tick(t, 1000)                                          // block 995 is only 5 deep
	if got := e.credit(t); got != 0 {
		t.Fatalf("credited before confirmations: %d", got)
	}
	e.tick(t, 1010)
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("credit = %d", got)
	}
	e.tick(t, 1030) // rescans nothing
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("credit after rescan = %d", got)
	}

	// Dust is recorded but not credited.
	e.chain.usdcTransfer(e.addr, 5_000, 1035, "0xtx2", 0)
	e.tick(t, 1050)
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("dust credited: %d", got)
	}
	var ignored int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM crypto_deposits WHERE status = 'ignored'`).Scan(&ignored)
	if ignored != 1 {
		t.Fatalf("ignored rows = %d", ignored)
	}

	// Someone else's address is not ours.
	e.chain.usdcTransfer("0x"+strings.Repeat("b", 40), 9_000_000, 1045, "0xtx3", 0)
	e.tick(t, 1060)
	if got := e.credit(t); got != 7_500_000 {
		t.Fatalf("stranger's transfer credited: %d", got)
	}
}

func TestETHDepositAtLockedRate(t *testing.T) {
	e := newDepositEnv(t)
	ctx := context.Background()
	e.tick(t, 1000)

	q, err := e.d.Quote(ctx, e.user, 10*MicrosPerUSD)
	if err != nil {
		t.Fatal(err)
	}
	if q.RateMicros != 3_000*MicrosPerUSD {
		t.Fatalf("rate = %d", q.RateMicros)
	}
	// The price moves up 50% before the user pays.
	e.chain.mu.Lock()
	e.chain.price = 4_500_00000000
	e.chain.mu.Unlock()
	e.d.rateMu.Lock()
	e.d.rateAt = time.Time{}
	e.d.rateMu.Unlock()

	// 0.5% short of the quoted amount is inside the tolerance.
	wei, _ := new(big.Int).SetString(q.AmountWei, 10)
	paid := new(big.Int).Mul(wei, big.NewInt(995))
	paid.Quo(paid, big.NewInt(1000))
	e.chain.setBalance(e.addr, paid)
	e.tick(t, 1010)
	if got := e.credit(t); got != 10*MicrosPerUSD {
		t.Fatalf("credit = %d, want the quoted $10", got)
	}
	e.tick(t, 1020)
	if got := e.credit(t); got != 10*MicrosPerUSD {
		t.Fatalf("credited twice: %d", got)
	}

	// A sweep drops the balance and credits nothing.
	e.chain.setBalance(e.addr, new(big.Int))
	e.tick(t, 1030)
	if got := e.credit(t); got != 10*MicrosPerUSD {
		t.Fatalf("sweep changed credit: %d", got)
	}

	// With no quote left, the next payment is priced live ($4500 per ETH).
	e.chain.setBalance(e.addr, new(big.Int).Div(weiPerETH, big.NewInt(100))) // 0.01 ETH
	e.tick(t, 1040)
	if got := e.credit(t); got != 10*MicrosPerUSD+45*MicrosPerUSD {
		t.Fatalf("credit = %d, want $55 total", got)
	}
}

func TestVoucherCredit(t *testing.T) {
	for _, c := range []struct{ percent, paid, want int64 }{
		{0, 10_000_000, 10_000_000},
		{50, 10_000_000, 20_000_000},
		{20, 8_000_000, 10_000_000},
		{33, 100, 149},
	} {
		if got := (discount{percent: c.percent}).credit(c.paid); got != c.want {
			t.Fatalf("%d%% of %d: got %d, want %d", c.percent, c.paid, got, c.want)
		}
	}
}

func TestVoucherDeposits(t *testing.T) {
	e := newDepositEnv(t)
	ctx := context.Background()
	e.tick(t, 1000)

	if _, err := e.d.ClaimVoucher(ctx, e.user, "SOCIAL50"); err == nil {
		t.Fatal("claimed a code that doesn't exist")
	}
	if err := e.d.CreateVoucher(ctx, "Social50", 50, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.d.CreateVoucher(ctx, "SOCIAL50", 10, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("created a duplicate code")
	}
	if err := e.d.CreateVoucher(ctx, "FREE", 100, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("created a 100% voucher")
	}
	if _, err := e.d.ClaimVoucher(ctx, e.user, " social50 "); err != nil {
		t.Fatal(err)
	}
	info, err := e.d.Info(ctx, e.user)
	if err != nil || info.Voucher == nil || info.Voucher.Percent != 50 {
		t.Fatalf("info voucher = %+v, %v", info.Voucher, err)
	}

	// $10 of USDC is credited as $20, and the code keeps working.
	e.chain.usdcTransfer(e.addr, 10*MicrosPerUSD, 995, "0xtx1", 0)
	e.chain.usdcTransfer(e.addr, 5*MicrosPerUSD, 996, "0xtx2", 0)
	e.tick(t, 1010)
	if got := e.credit(t); got != 30*MicrosPerUSD {
		t.Fatalf("credit = %d, want $30", got)
	}

	// A quote for the discounted $5 credits the list $10.
	q, err := e.d.Quote(ctx, e.user, 5*MicrosPerUSD)
	if err != nil {
		t.Fatal(err)
	}
	wei, _ := new(big.Int).SetString(q.AmountWei, 10)
	e.chain.setBalance(e.addr, wei)
	e.tick(t, 1020)
	if got := e.credit(t); got != 40*MicrosPerUSD {
		t.Fatalf("credit = %d, want $40", got)
	}

	reports, err := e.d.VoucherReports(ctx)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %+v, %v", reports, err)
	}
	r := reports[0]
	if r.Code != "SOCIAL50" || r.Uses != 3 || r.PaidMicros != 20*MicrosPerUSD || r.BonusMicros != 20*MicrosPerUSD {
		t.Fatalf("report = %+v", r)
	}

	// Removed: full price again.
	if err := e.d.RemoveVoucher(ctx, e.user); err != nil {
		t.Fatal(err)
	}
	e.chain.usdcTransfer(e.addr, 5*MicrosPerUSD, 1021, "0xtx3", 0)
	e.tick(t, 1040)
	if got := e.credit(t); got != 45*MicrosPerUSD {
		t.Fatalf("credit = %d, want $45", got)
	}

	// Ended: a payment on its way keeps the discount, new claims are refused.
	if _, err := e.d.ClaimVoucher(ctx, e.user, "SOCIAL50"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.EndVoucher(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.ClaimVoucher(ctx, e.user, "SOCIAL50"); err == nil {
		t.Fatal("claimed an ended voucher")
	}
	if info, _ := e.d.Info(ctx, e.user); info.Voucher != nil {
		t.Fatalf("ended voucher still shown: %+v", info.Voucher)
	}
	e.chain.usdcTransfer(e.addr, 5*MicrosPerUSD, 1041, "0xtx4", 0)
	e.tick(t, 1060)
	if got := e.credit(t); got != 55*MicrosPerUSD {
		t.Fatalf("credit = %d, want $55 inside the grace period", got)
	}

	// Past the grace period the discount is gone.
	if _, err := e.pool.Exec(ctx, `UPDATE vouchers SET expires_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	e.chain.usdcTransfer(e.addr, 5*MicrosPerUSD, 1061, "0xtx5", 0)
	e.tick(t, 1080)
	if got := e.credit(t); got != 60*MicrosPerUSD {
		t.Fatalf("credit = %d, want $60 after expiry", got)
	}
}

func TestQuoteLimits(t *testing.T) {
	e := newDepositEnv(t)
	if _, err := e.d.Quote(context.Background(), e.user, MicrosPerUSD); err == nil {
		t.Fatal("quote below the minimum was accepted")
	}
	e.chain.mu.Lock()
	e.chain.updated = time.Now().Add(-2 * time.Hour)
	e.chain.mu.Unlock()
	if _, err := e.d.Quote(context.Background(), e.user, 10*MicrosPerUSD); err == nil {
		t.Fatal("quote on a stale feed was accepted")
	}
}

func TestOverview(t *testing.T) {
	e := newDepositEnv(t)
	ctx := context.Background()
	e.tick(t, 1000)
	e.chain.usdcTransfer(e.addr, 7_500_000, 995, "0xtx1", 0)
	e.tick(t, 1010)

	// 7.5 USDC and 0.01 ETH ($30) sit on the address.
	e.chain.mu.Lock()
	e.chain.usdc[strings.ToLower(e.addr)] = big.NewInt(7_500_000)
	e.chain.mu.Unlock()
	e.chain.setBalance(e.addr, new(big.Int).Div(weiPerETH, big.NewInt(100)))

	o, err := e.d.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Addresses) != 1 {
		t.Fatalf("addresses = %d", len(o.Addresses))
	}
	a := o.Addresses[0]
	if a.Email != "a@example.com" || a.AccountNumber != a.Index+1 || a.Address != e.addr {
		t.Fatalf("row = %+v", a)
	}
	if a.CreditedMicros != 7_500_000 || a.Deposits != 1 {
		t.Fatalf("credited = %d in %d", a.CreditedMicros, a.Deposits)
	}
	if a.USDCMicros != 7_500_000 || a.ETHUSDMicros != 30*MicrosPerUSD || a.BalanceError != "" {
		t.Fatalf("on-chain = %+v", a)
	}
	if o.Totals.OnChainMicros != 37_500_000 || o.Totals.CreditedMicros != 7_500_000 {
		t.Fatalf("totals = %+v", o.Totals)
	}
}

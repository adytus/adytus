package billing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/near"
)

// Near credits NEAR and USDC paid to the deposit contract on NEAR
// (contracts/near-deposit).
//
// NEAR keys are ed25519, which can't derive child addresses from a public
// key, so there is no address per user as on Base. Instead every user has a
// short memo, and pays the one contract with it: deposit(memo) for NEAR, or
// ft_transfer_call(msg = memo) for USDC. The contract appends each payment
// to a log, and the watcher reads that log in order at final finality. The
// contract holds no withdraw logic; funds leave with the account's own key,
// kept offline, like the keys behind BASE_XPUB.
//
// All of its settings live in near_settings and are edited in the
// backoffice. The watcher re-reads them every tick, so a change takes effect
// within one poll.
type Near struct {
	DB     *pgxpool.Pool
	Ledger *Ledger

	// NewClient and NewPrice build the chain and price clients for the
	// current settings; tests point them at fakes.
	NewClient func(rpcURL string) *near.Client
	NewPrice  func(priceURL string) *near.Hermes

	rateMu  sync.Mutex
	rate    int64
	rateAt  time.Time
	rateKey string

	wakeOnce sync.Once
	wake     chan struct{}
}

// NearSettings is the near_settings row.
type NearSettings struct {
	Enabled         bool      `json:"enabled"`
	Network         string    `json:"network"`
	RPCURL          string    `json:"rpc_url"`
	Contract        string    `json:"contract"`
	USDC            string    `json:"usdc"`
	PriceURL        string    `json:"price_url"`
	PriceFeed       string    `json:"price_feed"`
	MaxPriceAgeSecs int64     `json:"max_price_age_secs"`
	QuoteTTLSecs    int64     `json:"quote_ttl_secs"`
	MinMicros       int64     `json:"min_micros"`
	DustMicros      int64     `json:"dust_micros"`
	ToleranceBps    int64     `json:"tolerance_bps"`
	PollSecs        int64     `json:"poll_secs"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type NearQuote struct {
	ID          uuid.UUID `json:"id"`
	USDMicros   int64     `json:"usd_micros"`
	USDTokens   int64     `json:"usd_tokens"`
	AmountYocto string    `json:"amount_yocto"`
	RateMicros  int64     `json:"rate_micros"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type NearInfo struct {
	Enabled   bool           `json:"enabled"`
	Network   string         `json:"network,omitempty"`
	Contract  string         `json:"contract,omitempty"`
	USDC      string         `json:"usdc,omitempty"`
	Memo      string         `json:"memo,omitempty"`
	MinMicros int64          `json:"min_micros,omitempty"`
	MinTokens int64          `json:"min_tokens,omitempty"`
	Quote     *NearQuote     `json:"quote"`
	Voucher   *Voucher       `json:"voucher"`
	Deposits  []DepositEntry `json:"deposits"`
}

const (
	nearBatch      = 100
	nearMemoLen    = 10
	memoAlphabet   = "abcdefghijkmnpqrstuvwxyz23456789" // 32: no 0/o, 1/l
	nearRecent     = 50
	nearIdlePoll   = 30 * time.Second
	nearRPCTimeout = 15 * time.Second
)

var priceFeedRE = regexp.MustCompile(`^(0x)?[0-9a-fA-F]{64}$`)

func (n *Near) client(s NearSettings) *near.Client {
	if n.NewClient != nil {
		return n.NewClient(s.RPCURL)
	}
	return near.NewClient(s.RPCURL)
}

// Client is the chain client for the given settings.
func (n *Near) Client(s NearSettings) *near.Client { return n.client(s) }

func (n *Near) price(s NearSettings) *near.Hermes {
	if n.NewPrice != nil {
		return n.NewPrice(s.PriceURL)
	}
	return near.NewHermes(s.PriceURL)
}

// Settings reads the current settings.
func (n *Near) Settings(ctx context.Context) (NearSettings, error) {
	var s NearSettings
	err := n.DB.QueryRow(ctx, `
		SELECT enabled, network, rpc_url, contract, usdc, price_url, price_feed, max_price_age_secs,
			quote_ttl_secs, min_micros, dust_micros, tolerance_bps, poll_secs, updated_at
		FROM near_settings`).
		Scan(&s.Enabled, &s.Network, &s.RPCURL, &s.Contract, &s.USDC, &s.PriceURL, &s.PriceFeed, &s.MaxPriceAgeSecs,
			&s.QuoteTTLSecs, &s.MinMicros, &s.DustMicros, &s.ToleranceBps, &s.PollSecs, &s.UpdatedAt)
	return s, err
}

// Enabled reports whether NEAR payments are switched on. A read error counts
// as off.
func (n *Near) Enabled(ctx context.Context) bool {
	s, err := n.Settings(ctx)
	return err == nil && s.Enabled
}

func (s *NearSettings) normalize() {
	s.RPCURL = strings.TrimSpace(s.RPCURL)
	s.Contract = strings.ToLower(strings.TrimSpace(s.Contract))
	s.USDC = strings.ToLower(strings.TrimSpace(s.USDC))
	s.PriceURL = strings.TrimRight(strings.TrimSpace(s.PriceURL), "/")
	s.PriceFeed = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s.PriceFeed)), "0x")
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// Validate checks the settings make sense on their own; Check asks the chain.
func (s NearSettings) Validate() error {
	switch {
	case s.Network != "mainnet" && s.Network != "testnet":
		return errs.Validation.New("network must be mainnet or testnet")
	case !httpURL(s.RPCURL):
		return errs.Validation.New("the RPC URL must be an http(s) URL")
	case s.Contract != "" && !near.ValidAccountID(s.Contract):
		return errs.Validation.New("%q isn't a NEAR account ID", s.Contract)
	case s.Enabled && s.Contract == "":
		return errs.Validation.New("set the deposit contract before switching NEAR payments on")
	case s.USDC != "" && !near.ValidAccountID(s.USDC):
		return errs.Validation.New("%q isn't a NEAR account ID", s.USDC)
	case !httpURL(s.PriceURL):
		return errs.Validation.New("the price service URL must be an http(s) URL")
	case !priceFeedRE.MatchString(s.PriceFeed):
		return errs.Validation.New("the price feed is a 64-character hex ID")
	case s.MaxPriceAgeSecs < 10 || s.MaxPriceAgeSecs > 86_400:
		return errs.Validation.New("the price age limit is between 10 seconds and a day")
	case s.QuoteTTLSecs < 60 || s.QuoteTTLSecs > 86_400:
		return errs.Validation.New("a quote is held between 1 minute and a day")
	case s.MinMicros < MicrosPerUSD/100 || s.MinMicros > maxQuoteUSD:
		return errs.Validation.New("the smallest top-up is between $0.01 and $10,000")
	case s.DustMicros < 0 || s.DustMicros > s.MinMicros:
		return errs.Validation.New("the dust limit is between $0 and the smallest top-up")
	case s.ToleranceBps < 0 || s.ToleranceBps > 1_000:
		return errs.Validation.New("the tolerance is between 0 and 1,000 basis points (10%%)")
	case s.PollSecs < 1 || s.PollSecs > 600:
		return errs.Validation.New("the poll interval is between 1 second and 10 minutes")
	}
	return nil
}

// SaveSettings validates and stores the settings. Switching payments on also
// checks that the RPC serves the chosen network, so a mainnet contract is
// never watched through a testnet node.
func (n *Near) SaveSettings(ctx context.Context, s NearSettings, by uuid.UUID) (NearSettings, error) {
	s.normalize()
	if err := s.Validate(); err != nil {
		return s, err
	}
	if s.Enabled {
		cctx, cancel := context.WithTimeout(ctx, nearRPCTimeout)
		id, err := n.client(s).ChainID(cctx)
		cancel()
		if err != nil {
			return s, errs.Validation.New("the RPC didn't answer, so NEAR payments stay off: %v", err)
		}
		if id != s.Network {
			return s, errs.Validation.New("the RPC serves %s, not %s", id, s.Network)
		}
	}
	_, err := n.DB.Exec(ctx, `
		UPDATE near_settings SET enabled = $1, network = $2, rpc_url = $3, contract = $4, usdc = $5,
			price_url = $6, price_feed = $7, max_price_age_secs = $8, quote_ttl_secs = $9, min_micros = $10,
			dust_micros = $11, tolerance_bps = $12, poll_secs = $13, updated_by = $14, updated_at = now()`,
		s.Enabled, s.Network, s.RPCURL, s.Contract, s.USDC, s.PriceURL, s.PriceFeed, s.MaxPriceAgeSecs,
		s.QuoteTTLSecs, s.MinMicros, s.DustMicros, s.ToleranceBps, s.PollSecs, by)
	if err != nil {
		return s, err
	}
	n.nudge()
	return n.Settings(ctx)
}

// NearCheck is one line of the backoffice's connection test.
type NearCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Check tries settings against the chain and the price service without
// saving them, so the backoffice can test before switching anything on.
func (n *Near) Check(ctx context.Context, s NearSettings) ([]NearCheck, error) {
	s.normalize()
	s.Enabled = false // a check never needs the contract to be set
	if err := s.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*nearRPCTimeout)
	defer cancel()
	c := n.client(s)
	var out []NearCheck
	add := func(name string, err error, ok string) {
		if err != nil {
			out = append(out, NearCheck{Name: name, Detail: err.Error()})
			return
		}
		out = append(out, NearCheck{Name: name, OK: true, Detail: ok})
	}

	id, err := c.ChainID(ctx)
	if err == nil && id != s.Network {
		err = fmt.Errorf("it serves %s, not %s", id, s.Network)
	}
	add("RPC", err, "Serves "+s.Network+".")
	rpcOK := err == nil

	if s.Contract == "" {
		out = append(out, NearCheck{Name: "Deposit contract", Detail: "Not set. Deploy contracts/near-deposit and enter its account."})
	} else if rpcOK {
		count, err := c.DepositCount(ctx, s.Contract)
		add("Deposit contract", err, fmt.Sprintf("Answers; %d payment%s logged.", count, plural(count)))
		if err == nil && s.USDC != "" {
			tokens, err := c.AcceptedTokens(ctx, s.Contract)
			if err == nil && !contains(tokens, s.USDC) {
				err = fmt.Errorf("the contract doesn't accept %s; it takes %v. Call set_tokens from the contract account", s.USDC, tokens)
			}
			add("USDC accepted", err, "The contract takes "+s.USDC+".")
			reg, err := c.TokenRegistered(ctx, s.USDC, s.Contract)
			if err == nil && !reg {
				err = errors.New("the contract has no storage deposit with the USDC contract, so USDC payments bounce. Call storage_deposit on the USDC contract for it")
			}
			add("USDC storage", err, "The contract is registered with USDC.")
		}
	}

	rate, err := n.price(s).USD(ctx, s.PriceFeed, time.Duration(s.MaxPriceAgeSecs)*time.Second)
	add("NEAR price", err, fmt.Sprintf("$%.4f per NEAR.", float64(rate)/MicrosPerUSD))
	return out, nil
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Memo returns the user's payment memo, making one on first use.
func (n *Near) Memo(ctx context.Context, userID uuid.UUID) (string, error) {
	for range 5 {
		var memo string
		err := n.DB.QueryRow(ctx, `SELECT memo FROM near_memos WHERE user_id = $1`, userID).Scan(&memo)
		if err == nil {
			return memo, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		fresh, err := newMemo()
		if err != nil {
			return "", err
		}
		// A clash on user_id is a concurrent request; one on memo is chance.
		// Either way the next round reads or retries.
		if _, err := n.DB.Exec(ctx, `
			INSERT INTO near_memos (user_id, memo) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, fresh); err != nil {
			return "", err
		}
	}
	return "", errs.Internal.New("could not make a payment memo")
}

func newMemo() (string, error) {
	b := make([]byte, nearMemoLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = memoAlphabet[b[i]&31]
	}
	return string(b), nil
}

func (n *Near) Info(ctx context.Context, userID uuid.UUID) (NearInfo, error) {
	s, err := n.Settings(ctx)
	if err != nil {
		return NearInfo{}, err
	}
	if !s.Enabled {
		return NearInfo{Deposits: []DepositEntry{}}, nil
	}
	memo, err := n.Memo(ctx, userID)
	if err != nil {
		return NearInfo{}, err
	}
	info := NearInfo{
		Enabled:   true,
		Network:   s.Network,
		Contract:  s.Contract,
		USDC:      s.USDC,
		Memo:      memo,
		MinMicros: s.MinMicros,
		MinTokens: Tokens(s.MinMicros),
		Deposits:  []DepositEntry{},
	}

	var q NearQuote
	err = n.DB.QueryRow(ctx, `
		SELECT id, usd_micros, amount_atomic::text, rate_micros, expires_at
		FROM crypto_quotes WHERE user_id = $1 AND asset = 'near' AND status = 'open' AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1`, userID).
		Scan(&q.ID, &q.USDMicros, &q.AmountYocto, &q.RateMicros, &q.ExpiresAt)
	switch {
	case err == nil:
		q.USDTokens = Tokens(q.USDMicros)
		info.Quote = &q
	case !errors.Is(err, pgx.ErrNoRows):
		return info, err
	}

	if info.Voucher, err = (&Vouchers{DB: n.DB}).Claimed(ctx, userID); err != nil {
		return info, err
	}

	rows, err := n.DB.Query(ctx, `
		SELECT id, asset, amount_atomic::text, credit_micros, status, tx_hash, created_at
		FROM crypto_deposits WHERE user_id = $1 AND chain = 'near' ORDER BY created_at DESC LIMIT $2`, userID, recentLimit)
	if err != nil {
		return info, err
	}
	defer rows.Close()
	for rows.Next() {
		var e DepositEntry
		if err := rows.Scan(&e.ID, &e.Asset, &e.AmountAtomic, &e.CreditMicros, &e.Status, &e.TxHash, &e.CreatedAt); err != nil {
			return info, err
		}
		e.CreditTokens = Tokens(e.CreditMicros)
		info.Deposits = append(info.Deposits, e)
	}
	return info, rows.Err()
}

// Quote locks the NEAR price for a top-up of usdMicros.
func (n *Near) Quote(ctx context.Context, userID uuid.UUID, usdMicros int64) (NearQuote, error) {
	s, err := n.Settings(ctx)
	if err != nil {
		return NearQuote{}, err
	}
	if !s.Enabled {
		return NearQuote{}, errs.NotFound.New("NEAR payments are not enabled")
	}
	if usdMicros < s.MinMicros {
		return NearQuote{}, errs.Validation.New("the smallest top-up is %d tokens", Tokens(s.MinMicros))
	}
	if usdMicros > maxQuoteUSD {
		return NearQuote{}, errs.Validation.New("that amount is too large for one top-up")
	}
	if _, err := n.Memo(ctx, userID); err != nil {
		return NearQuote{}, err
	}
	rate, err := n.liveRate(ctx, s)
	if err != nil {
		return NearQuote{}, errs.Upstream.Wrap(err, "could not price NEAR right now")
	}
	q := NearQuote{
		USDMicros:   usdMicros,
		USDTokens:   Tokens(usdMicros),
		AmountYocto: yoctoFor(usdMicros, rate).String(),
		RateMicros:  rate,
		ExpiresAt:   time.Now().Add(time.Duration(s.QuoteTTLSecs) * time.Second),
	}
	err = n.DB.QueryRow(ctx, `
		INSERT INTO crypto_quotes (user_id, asset, usd_micros, amount_atomic, rate_micros, expires_at)
		VALUES ($1, 'near', $2, $3::numeric, $4, $5) RETURNING id`,
		userID, q.USDMicros, q.AmountYocto, q.RateMicros, q.ExpiresAt).Scan(&q.ID)
	return q, err
}

// yoctoFor is the yoctoNEAR worth usdMicros at rate micro-USD per NEAR,
// rounded up so a quote is never short.
func yoctoFor(usdMicros, rate int64) *big.Int {
	num := new(big.Int).Mul(big.NewInt(usdMicros), near.YoctoPerNEAR)
	q, m := new(big.Int).QuoRem(num, big.NewInt(rate), new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// microsForYocto is the micro-USD worth yocto at rate, rounded down.
func microsForYocto(yocto *big.Int, rate int64) *big.Int {
	v := new(big.Int).Mul(yocto, big.NewInt(rate))
	return v.Quo(v, near.YoctoPerNEAR)
}

func (n *Near) liveRate(ctx context.Context, s NearSettings) (int64, error) {
	key := s.PriceURL + "|" + s.PriceFeed
	n.rateMu.Lock()
	defer n.rateMu.Unlock()
	if n.rate > 0 && n.rateKey == key && time.Since(n.rateAt) < rateCacheTTL {
		return n.rate, nil
	}
	r, err := n.price(s).USD(ctx, s.PriceFeed, time.Duration(s.MaxPriceAgeSecs)*time.Second)
	if err != nil {
		return 0, err
	}
	n.rate, n.rateAt, n.rateKey = r, time.Now(), key
	return r, nil
}

func (n *Near) wakeCh() chan struct{} {
	n.wakeOnce.Do(func() { n.wake = make(chan struct{}, 1) })
	return n.wake
}

// nudge makes the watcher pick up new settings now instead of at its next poll.
func (n *Near) nudge() {
	select {
	case n.wakeCh() <- struct{}{}:
	default:
	}
}

// Run watches the deposit contract until ctx ends. It runs whether or not
// NEAR payments are on, idling while they're off.
func (n *Near) Run(ctx context.Context) {
	wake := n.wakeCh()
	for {
		wait := nearIdlePoll
		s, err := n.Settings(ctx)
		if err == nil && s.Enabled && s.Contract != "" {
			wait = time.Duration(s.PollSecs) * time.Second
			err = n.Tick(ctx, s)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("near watcher", "err", err)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// Tick credits every payment the contract has logged since the last one.
func (n *Near) Tick(ctx context.Context, s NearSettings) error {
	ctx, cancel := context.WithTimeout(ctx, 4*nearRPCTimeout)
	defer cancel()
	c := n.client(s)
	var next int64
	err := n.DB.QueryRow(ctx, `SELECT next_index FROM near_cursors WHERE contract = $1`, s.Contract).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		// Every payment to the contract is ours, so a new one is read from the
		// start: nothing paid before the watcher first ran is lost.
		_, err = n.DB.Exec(ctx, `INSERT INTO near_cursors (contract, next_index) VALUES ($1, 0) ON CONFLICT DO NOTHING`, s.Contract)
	}
	if err != nil {
		return err
	}
	count, err := c.DepositCount(ctx, s.Contract)
	if err != nil {
		return err
	}
	for next < count {
		batch, err := c.Deposits(ctx, s.Contract, next, min(nearBatch, count-next))
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return fmt.Errorf("contract returned no deposits at %d of %d", next, count)
		}
		for _, d := range batch {
			if err := n.credit(ctx, s, next, d); err != nil {
				return err
			}
			next++
		}
	}
	return nil
}

// credit handles the deposit at index of the log and moves the cursor past
// it, in one transaction. An error leaves the cursor, so it is tried again.
func (n *Near) credit(ctx context.Context, s NearSettings, index int64, d near.Deposit) error {
	key := fmt.Sprintf("%s:%d", s.Contract, index)
	amount, err := d.AmountInt()
	if err != nil {
		slog.Warn("near deposit skipped", "key", key, "err", err)
		return n.advance(ctx, n.DB, s.Contract, index)
	}

	var asset string
	switch {
	case d.Token == "":
		asset = "near"
	case s.USDC != "" && d.Token == s.USDC:
		asset = "usdc"
	default:
		slog.Warn("near deposit of a token we don't price", "key", key, "token", d.Token, "amount", d.Amount)
		return n.advance(ctx, n.DB, s.Contract, index)
	}

	// Price NEAR outside the transaction; only needed if there's no quote.
	rate, rateErr := int64(0), error(nil)
	if asset == "near" {
		rate, rateErr = n.liveRate(ctx, s)
	}

	return pgx.BeginFunc(ctx, n.DB, func(tx pgx.Tx) error {
		var userID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT user_id FROM near_memos WHERE memo = $1`, strings.ToLower(d.Memo)).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("near deposit with an unknown memo", "key", key, "memo", d.Memo, "token", d.Token, "amount", d.Amount, "sender", d.Sender)
			return n.advance(ctx, tx, s.Contract, index)
		}
		if err != nil {
			return err
		}

		var (
			micros  *big.Int
			used    any // rate_micros column; NULL for USDC
			quoteID any
		)
		if asset == "usdc" {
			// USDC has 6 decimals, so base units are micro-USD.
			micros = amount
		} else {
			var (
				qID  uuid.UUID
				qUSD int64
				qAmt string
				qRt  int64
			)
			err := tx.QueryRow(ctx, `
				SELECT id, usd_micros, amount_atomic::text, rate_micros FROM crypto_quotes
				WHERE user_id = $1 AND asset = 'near' AND status = 'open'
					AND expires_at + make_interval(secs => $2) > to_timestamp($3::double precision / 1000)
				ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, userID, quoteSlack.Seconds(), d.TimestampMs).
				Scan(&qID, &qUSD, &qAmt, &qRt)
			hasQuote := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			r := rate
			if hasQuote {
				r = qRt
			} else if rateErr != nil {
				return rateErr // try again next tick rather than guess a price
			}
			used = r
			micros = microsForYocto(amount, r)
			if hasQuote {
				want, _ := new(big.Int).SetString(qAmt, 10)
				floor := new(big.Int).Mul(want, big.NewInt(10_000-s.ToleranceBps))
				floor.Quo(floor, big.NewInt(10_000))
				if amount.Cmp(floor) >= 0 && micros.Cmp(big.NewInt(qUSD)) < 0 {
					micros = big.NewInt(qUSD)
				}
				quoteID = qID
			}
		}

		status := "ignored"
		var paid int64
		if micros.IsInt64() && micros.Int64() >= s.DustMicros && micros.Int64() > 0 {
			status, paid = "credited", micros.Int64()
		}
		// Like ETH, a quote prices one credited payment; the next goes at the
		// live rate. Dust doesn't use it up.
		if quoteID != nil && status == "credited" {
			if _, err := tx.Exec(ctx, `UPDATE crypto_quotes SET status = 'credited' WHERE id = $1`, quoteID); err != nil {
				return err
			}
		} else {
			quoteID = nil
		}
		var credit int64
		var voucherID any
		if status == "credited" {
			v, err := discountFor(ctx, tx, userID)
			if err != nil {
				return err
			}
			credit, voucherID = v.credit(paid), v.id
		}

		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO crypto_deposits (user_id, chain, asset, amount_atomic, credit_micros, rate_micros, quote_id,
				status, block_number, voucher_id, bonus_micros, source_key)
			VALUES ($1, 'near', $2, $3::numeric, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (source_key) DO NOTHING
			RETURNING id`,
			userID, asset, amount.String(), credit, used, quoteID, status, d.BlockHeight, voucherID, credit-paid, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return n.advance(ctx, tx, s.Contract, index) // already handled
		}
		if err != nil {
			return err
		}
		if status == "credited" {
			plan := "near"
			if asset == "usdc" {
				plan = "near-usdc"
			}
			if err := n.Ledger.SubscribeTx(ctx, tx, userID,
				Plan{ID: plan, PriceMicros: paid, CreditMicros: credit}, "near:"+key); err != nil {
				return err
			}
		}
		return n.advance(ctx, tx, s.Contract, index)
	})
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (n *Near) advance(ctx context.Context, q execer, contract string, index int64) error {
	_, err := q.Exec(ctx, `
		UPDATE near_cursors SET next_index = GREATEST(next_index, $2), updated_at = now() WHERE contract = $1`,
		contract, index+1)
	return err
}

// NearOverview is the operator's view of NEAR payments: the contract's
// holdings and its latest payments, matched to users.
type NearOverview struct {
	Enabled  bool   `json:"enabled"`
	Network  string `json:"network"`
	Contract string `json:"contract"`
	USDC     string `json:"usdc"`
	// NEARUSDMicros is the live price; 0 when it couldn't be read.
	NEARUSDMicros int64 `json:"near_usd_micros"`

	// On the contract now. BalanceError is set when the node couldn't be asked.
	NEARYocto    string `json:"near_yocto"`
	StorageBytes int64  `json:"storage_bytes"`
	USDCMicros   int64  `json:"usdc_micros"`
	BalanceError string `json:"balance_error,omitempty"`

	Logged int64 `json:"logged"`
	Read   int64 `json:"read"`

	CreditedMicros int64                 `json:"credited_micros"`
	Credited       int64                 `json:"credited"`
	Recent         []NearOverviewPayment `json:"recent"`
}

type NearOverviewPayment struct {
	Index        int64     `json:"index"`
	Memo         string    `json:"memo"`
	Email        string    `json:"email"`
	Asset        string    `json:"asset"`
	Token        string    `json:"token"`
	Sender       string    `json:"sender"`
	Amount       string    `json:"amount"`
	BlockHeight  int64     `json:"block_height"`
	At           time.Time `json:"at"`
	Status       string    `json:"status"` // credited, ignored, pending, unmatched
	CreditMicros int64     `json:"credit_micros"`
}

// Overview reads the contract's balances and its latest payments.
func (n *Near) Overview(ctx context.Context) (NearOverview, error) {
	s, err := n.Settings(ctx)
	if err != nil {
		return NearOverview{}, err
	}
	o := NearOverview{
		Enabled: s.Enabled, Network: s.Network, Contract: s.Contract, USDC: s.USDC,
		NEARYocto: "0", Recent: []NearOverviewPayment{},
	}
	if err := n.DB.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(credit_micros), 0) FROM crypto_deposits WHERE chain = 'near' AND status = 'credited'`).
		Scan(&o.Credited, &o.CreditedMicros); err != nil {
		return o, err
	}
	if s.Contract == "" {
		return o, nil
	}
	_ = n.DB.QueryRow(ctx, `SELECT next_index FROM near_cursors WHERE contract = $1`, s.Contract).Scan(&o.Read)

	ctx, cancel := context.WithTimeout(ctx, 2*nearRPCTimeout)
	defer cancel()
	c := n.client(s)
	if rate, err := n.liveRate(ctx, s); err == nil {
		o.NEARUSDMicros = rate
	}
	bal, storage, err := c.Balance(ctx, s.Contract)
	if err != nil {
		o.BalanceError = err.Error()
		return o, nil
	}
	o.NEARYocto, o.StorageBytes = bal.String(), storage
	if s.USDC != "" {
		if u, err := c.TokenBalance(ctx, s.USDC, s.Contract); err != nil {
			o.BalanceError = err.Error()
		} else if u.IsInt64() {
			o.USDCMicros = u.Int64()
		}
	}
	if o.Logged, err = c.DepositCount(ctx, s.Contract); err != nil {
		o.BalanceError = err.Error()
		return o, nil
	}
	from := max(0, o.Logged-nearRecent)
	deps, err := c.Deposits(ctx, s.Contract, from, o.Logged-from)
	if err != nil {
		o.BalanceError = err.Error()
		return o, nil
	}

	memos := make([]string, 0, len(deps))
	keys := make([]string, 0, len(deps))
	for i, d := range deps {
		memos = append(memos, strings.ToLower(d.Memo))
		keys = append(keys, fmt.Sprintf("%s:%d", s.Contract, from+int64(i)))
	}
	emails := map[string]string{}
	rows, err := n.DB.Query(ctx, `
		SELECT m.memo, u.email FROM near_memos m JOIN users u ON u.id = m.user_id WHERE m.memo = ANY($1)`, memos)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var m, e string
		if err := rows.Scan(&m, &e); err != nil {
			rows.Close()
			return o, err
		}
		emails[m] = e
	}
	rows.Close()
	type done struct {
		status string
		credit int64
	}
	handled := map[string]done{}
	rows, err = n.DB.Query(ctx, `SELECT source_key, status, credit_micros FROM crypto_deposits WHERE source_key = ANY($1)`, keys)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var k string
		var d done
		if err := rows.Scan(&k, &d.status, &d.credit); err != nil {
			rows.Close()
			return o, err
		}
		handled[k] = d
	}
	rows.Close()

	for i := len(deps) - 1; i >= 0; i-- {
		d := deps[i]
		idx := from + int64(i)
		p := NearOverviewPayment{
			Index: idx, Memo: d.Memo, Email: emails[strings.ToLower(d.Memo)], Token: d.Token, Sender: d.Sender,
			Amount: d.Amount, BlockHeight: d.BlockHeight, At: time.UnixMilli(d.TimestampMs).UTC(),
		}
		switch {
		case d.Token == "":
			p.Asset = "near"
		case d.Token == s.USDC:
			p.Asset = "usdc"
		default:
			p.Asset = "other"
		}
		if h, ok := handled[keys[i]]; ok {
			p.Status, p.CreditMicros = h.status, h.credit
		} else if idx >= o.Read {
			p.Status = "pending"
		} else {
			p.Status = "unmatched" // read, but no user or token to credit
		}
		o.Recent = append(o.Recent, p)
	}
	return o, nil
}

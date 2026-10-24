package billing

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/base"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
)

// Deposits credits USDC and ETH sent to a per-user address on Base.
//
// Every user has one deposit address derived from an extended public key, so
// a payment is attributed by where it lands, with no memo or exact amount to
// get wrong. A watcher credits it once it is Confirmations blocks deep:
//
//   - USDC: Transfer logs to any deposit address. 1 USDC base unit is exactly
//     1 micro-USD, so the amount is the credit.
//   - ETH: a balance above what was already credited. The balance is read
//     rather than scanning transactions, which would miss payments made by
//     smart-contract wallets. Priced at a rate the user locked with Quote if
//     the payment arrives in time, else at the live Chainlink rate.
//
// Sweeping an address between two polls can hide an ETH deposit made in the
// same window; sweep addresses that have no quote open.
type Deposits struct {
	DB      *pgxpool.Pool
	Ledger  *Ledger
	Chain   *base.Client
	Deriver *base.Deriver
	Cfg     DepositConfig

	rateMu sync.Mutex
	rate   int64
	rateAt time.Time
}

type DepositConfig struct {
	ChainID    int64
	USDC       string
	ETHUSDFeed string
	// Confirmations is how deep a payment must be before it is credited.
	Confirmations int64
	QuoteTTL      time.Duration
	// MinMicros is the smallest top-up Quote accepts; DustMicros is the
	// smallest payment that is credited at all.
	MinMicros  int64
	DustMicros int64
	// ToleranceBps is how far under a quote an ETH payment may fall and still
	// be credited the quoted amount.
	ToleranceBps int64
	Poll         time.Duration
	// Addresses with an open quote are polled every tick until the quote
	// expires (plus quoteSlack); other addresses are polled every
	// FullScanEvery.
	FullScanEvery time.Duration
	MaxFeedAge    time.Duration
}

const (
	quoteSlack   = time.Minute
	logSpan      = 2000
	logBatch     = 100
	maxQuoteUSD  = 10_000 * MicrosPerUSD
	rateCacheTTL = 10 * time.Second
	recentLimit  = 20
)

var weiPerETH = big.NewInt(1_000_000_000_000_000_000)

type Quote struct {
	ID          uuid.UUID `json:"id"`
	USDMicros   int64     `json:"usd_micros"`
	USDTokens   int64     `json:"usd_tokens"`
	AmountWei   string    `json:"amount_wei"`
	RateMicros  int64     `json:"rate_micros"`
	ExpiresAt   time.Time `json:"expires_at"`
	DepositAddr string    `json:"address"`
}

type DepositEntry struct {
	ID           uuid.UUID   `json:"id"`
	Asset        string      `json:"asset"`
	AmountAtomic string      `json:"amount_atomic"`
	CreditMicros int64       `json:"credit_micros"`
	CreditTokens int64       `json:"credit_tokens"`
	Status       string      `json:"status"`
	TxHash       null.String `json:"tx_hash"`
	CreatedAt    time.Time   `json:"created_at"`
}

type DepositInfo struct {
	Enabled       bool           `json:"enabled"`
	ChainID       int64          `json:"chain_id,omitempty"`
	Address       string         `json:"address,omitempty"`
	USDC          string         `json:"usdc,omitempty"`
	Confirmations int64          `json:"confirmations,omitempty"`
	MinMicros     int64          `json:"min_micros,omitempty"`
	MinTokens     int64          `json:"min_tokens,omitempty"`
	Quote         *Quote         `json:"quote"`
	Deposits      []DepositEntry `json:"deposits"`
}

// Address returns the user's deposit address, deriving it on first use.
func (d *Deposits) Address(ctx context.Context, userID uuid.UUID) (string, error) {
	if _, err := d.DB.Exec(ctx,
		`INSERT INTO deposit_addresses (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID); err != nil {
		return "", err
	}
	var idx int64
	var addr null.String
	if err := d.DB.QueryRow(ctx,
		`SELECT idx, address FROM deposit_addresses WHERE user_id = $1`, userID).Scan(&idx, &addr); err != nil {
		return "", err
	}
	if addr.Valid {
		return addr.String, nil
	}
	if idx > math.MaxInt32 {
		return "", errs.Internal.New("deposit address index out of range")
	}
	derived, err := d.Deriver.Address(uint32(idx))
	if err != nil {
		return "", err
	}
	// Two requests may race to here; both derive the same address.
	if _, err := d.DB.Exec(ctx,
		`UPDATE deposit_addresses SET address = $2 WHERE user_id = $1 AND address IS NULL`, userID, derived); err != nil {
		return "", err
	}
	return derived, nil
}

func (d *Deposits) Info(ctx context.Context, userID uuid.UUID) (DepositInfo, error) {
	addr, err := d.Address(ctx, userID)
	if err != nil {
		return DepositInfo{}, err
	}
	info := DepositInfo{
		Enabled:       true,
		ChainID:       d.Cfg.ChainID,
		Address:       addr,
		USDC:          d.Cfg.USDC,
		Confirmations: d.Cfg.Confirmations,
		MinMicros:     d.Cfg.MinMicros,
		MinTokens:     Tokens(d.Cfg.MinMicros),
		Deposits:      []DepositEntry{},
	}

	var q Quote
	err = d.DB.QueryRow(ctx, `
		SELECT id, usd_micros, amount_wei::text, rate_micros, expires_at
		FROM eth_quotes WHERE user_id = $1 AND status = 'open' AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1`, userID).
		Scan(&q.ID, &q.USDMicros, &q.AmountWei, &q.RateMicros, &q.ExpiresAt)
	switch {
	case err == nil:
		q.USDTokens = Tokens(q.USDMicros)
		q.DepositAddr = addr
		info.Quote = &q
	case !errors.Is(err, pgx.ErrNoRows):
		return info, err
	}

	rows, err := d.DB.Query(ctx, `
		SELECT id, asset, amount_atomic::text, credit_micros, status, tx_hash, created_at
		FROM crypto_deposits WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, recentLimit)
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

// Quote locks the ETH price for a top-up of usdMicros.
func (d *Deposits) Quote(ctx context.Context, userID uuid.UUID, usdMicros int64) (Quote, error) {
	if usdMicros < d.Cfg.MinMicros {
		return Quote{}, errs.Validation.New("the smallest top-up is %d tokens", Tokens(d.Cfg.MinMicros))
	}
	if usdMicros > maxQuoteUSD {
		return Quote{}, errs.Validation.New("that amount is too large for one top-up")
	}
	addr, err := d.Address(ctx, userID)
	if err != nil {
		return Quote{}, err
	}
	rate, err := d.liveRate(ctx)
	if err != nil {
		return Quote{}, errs.Upstream.Wrap(err, "could not price ETH right now")
	}
	q := Quote{
		USDMicros:   usdMicros,
		USDTokens:   Tokens(usdMicros),
		AmountWei:   weiFor(usdMicros, rate).String(),
		RateMicros:  rate,
		ExpiresAt:   time.Now().Add(d.Cfg.QuoteTTL),
		DepositAddr: addr,
	}
	err = d.DB.QueryRow(ctx, `
		INSERT INTO eth_quotes (user_id, usd_micros, amount_wei, rate_micros, expires_at)
		VALUES ($1, $2, $3::numeric, $4, $5) RETURNING id`,
		userID, q.USDMicros, q.AmountWei, q.RateMicros, q.ExpiresAt).Scan(&q.ID)
	return q, err
}

// weiFor is the wei worth usdMicros at rate micro-USD per ETH, rounded up so
// a quote is never short.
func weiFor(usdMicros, rate int64) *big.Int {
	n := new(big.Int).Mul(big.NewInt(usdMicros), weiPerETH)
	r := big.NewInt(rate)
	q, m := new(big.Int).QuoRem(n, r, new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// microsFor is the micro-USD worth wei at rate, rounded down.
func microsFor(wei *big.Int, rate int64) *big.Int {
	n := new(big.Int).Mul(wei, big.NewInt(rate))
	return n.Quo(n, weiPerETH)
}

func (d *Deposits) liveRate(ctx context.Context) (int64, error) {
	d.rateMu.Lock()
	defer d.rateMu.Unlock()
	if d.rate > 0 && time.Since(d.rateAt) < rateCacheTTL {
		return d.rate, nil
	}
	r, err := d.Chain.ETHUSD(ctx, d.Cfg.ETHUSDFeed, d.Cfg.MaxFeedAge)
	if err != nil {
		return 0, err
	}
	d.rate, d.rateAt = r, time.Now()
	return r, nil
}

// Run watches the chain until ctx ends.
func (d *Deposits) Run(ctx context.Context) {
	t := time.NewTicker(d.Cfg.Poll)
	defer t.Stop()
	var lastFull time.Time
	for {
		full := time.Since(lastFull) >= d.Cfg.FullScanEvery
		if err := d.tick(ctx, full); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("deposit watcher", "err", err)
		} else if full {
			lastFull = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Deposits) tick(ctx context.Context, full bool) error {
	head, err := d.Chain.BlockNumber(ctx)
	if err != nil {
		return err
	}
	safe := head - d.Cfg.Confirmations
	if safe < 0 {
		return nil
	}
	usdcErr := d.scanUSDC(ctx, safe)
	ethErr := d.scanETH(ctx, safe, full)
	return errors.Join(usdcErr, ethErr)
}

type watched struct {
	userID uuid.UUID
	addr   string
}

func (d *Deposits) scanUSDC(ctx context.Context, safe int64) error {
	var last int64
	err := d.DB.QueryRow(ctx, `SELECT last_block FROM chain_cursors WHERE chain_id = $1`, d.Cfg.ChainID).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		// First start: only payments from here on.
		_, err = d.DB.Exec(ctx,
			`INSERT INTO chain_cursors (chain_id, last_block) VALUES ($1, $2) ON CONFLICT DO NOTHING`, d.Cfg.ChainID, safe)
		return err
	}
	if err != nil {
		return err
	}
	for last < safe {
		to := min(safe, last+logSpan)
		users, err := d.addresses(ctx)
		if err != nil {
			return err
		}
		byAddr := make(map[string]uuid.UUID, len(users))
		addrs := make([]string, 0, len(users))
		for _, u := range users {
			byAddr[strings.ToLower(u.addr)] = u.userID
			addrs = append(addrs, u.addr)
		}
		for i := 0; i < len(addrs); i += logBatch {
			logs, err := d.Chain.TransferLogs(ctx, d.Cfg.USDC, addrs[i:min(i+logBatch, len(addrs))], last+1, to)
			if err != nil {
				return err
			}
			for _, l := range logs {
				if err := d.creditUSDC(ctx, byAddr, l); err != nil {
					return err
				}
			}
		}
		if _, err := d.DB.Exec(ctx, `
			UPDATE chain_cursors SET last_block = GREATEST(last_block, $2), updated_at = now()
			WHERE chain_id = $1`, d.Cfg.ChainID, to); err != nil {
			return err
		}
		last = to
	}
	return nil
}

func (d *Deposits) addresses(ctx context.Context) ([]watched, error) {
	rows, err := d.DB.Query(ctx, `SELECT user_id, address FROM deposit_addresses WHERE address IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []watched
	for rows.Next() {
		var w watched
		if err := rows.Scan(&w.userID, &w.addr); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *Deposits) creditUSDC(ctx context.Context, byAddr map[string]uuid.UUID, l base.Log) error {
	// ERC-20 Transfer has three topics; the ERC-721 event of the same shape has four.
	if l.Removed || len(l.Topics) != 3 || len(l.Topics[2]) != 66 {
		return nil
	}
	userID, ok := byAddr["0x"+strings.ToLower(l.Topics[2][26:])]
	if !ok {
		return nil
	}
	amount, err := base.ParseQuantity(l.Data)
	if err != nil {
		return nil
	}
	block, err := base.ParseQuantity(l.BlockNumber)
	if err != nil {
		return err
	}
	logIdx, err := base.ParseQuantity(l.LogIndex)
	if err != nil {
		return err
	}

	// USDC has 6 decimals, so base units are micro-USD.
	var micros int64
	status := "ignored"
	if amount.IsInt64() && amount.Int64() >= d.Cfg.DustMicros {
		micros, status = amount.Int64(), "credited"
	}
	return pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO crypto_deposits (user_id, asset, amount_atomic, credit_micros, status, tx_hash, log_index, block_number)
			VALUES ($1, 'usdc', $2::numeric, $3, $4, $5, $6, $7)
			ON CONFLICT (tx_hash, log_index) WHERE tx_hash IS NOT NULL DO NOTHING
			RETURNING id`,
			userID, amount.String(), micros, status, l.TxHash, logIdx.Int64(), block.Int64()).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already handled
		}
		if err != nil {
			return err
		}
		if status != "credited" {
			return nil
		}
		return d.Ledger.SubscribeTx(ctx, tx, userID,
			Plan{ID: "usdc", PriceMicros: micros, CreditMicros: micros}, "base:"+l.TxHash)
	})
}

func (d *Deposits) scanETH(ctx context.Context, safe int64, full bool) error {
	q := `SELECT user_id, address, eth_accounted_wei::text FROM deposit_addresses WHERE address IS NOT NULL`
	args := []any{}
	if !full {
		// Only follow addresses someone is actually paying: an open quote
		// stays hot until it expires, then falls back to the slow scan. Using
		// created_at here would keep every address created in the window hot
		// for hours after the quote was abandoned.
		q += ` AND EXISTS (SELECT 1 FROM eth_quotes q WHERE q.user_id = deposit_addresses.user_id
			AND q.status = 'open' AND q.expires_at + make_interval(secs => $1) > now())`
		args = append(args, quoteSlack.Seconds())
	}
	rows, err := d.DB.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	type row struct {
		userID  uuid.UUID
		addr    string
		counted *big.Int
	}
	var todo []row
	for rows.Next() {
		var r row
		var wm string
		if err := rows.Scan(&r.userID, &r.addr, &wm); err != nil {
			rows.Close()
			return err
		}
		r.counted, _ = new(big.Int).SetString(wm, 10)
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var errList []error
	for _, r := range todo {
		bal, err := d.Chain.BalanceAt(ctx, r.addr, safe)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		if bal.Cmp(r.counted) == 0 {
			continue
		}
		if err := d.creditETH(ctx, r.userID, bal, safe); err != nil {
			errList = append(errList, err)
		}
	}
	return errors.Join(errList...)
}

// creditETH reconciles an address with its balance at block safe: a rise is
// credited, a fall (a sweep) just moves the mark down.
func (d *Deposits) creditETH(ctx context.Context, userID uuid.UUID, bal *big.Int, safe int64) error {
	rate, rateErr := d.liveRate(ctx)
	return pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
		var wm string
		if err := tx.QueryRow(ctx,
			`SELECT eth_accounted_wei::text FROM deposit_addresses WHERE user_id = $1 FOR UPDATE`, userID).Scan(&wm); err != nil {
			return err
		}
		counted, _ := new(big.Int).SetString(wm, 10)
		switch bal.Cmp(counted) {
		case 0:
			return nil
		case -1:
			_, err := tx.Exec(ctx, `UPDATE deposit_addresses SET eth_accounted_wei = $2::numeric WHERE user_id = $1`,
				userID, bal.String())
			return err
		}
		delta := new(big.Int).Sub(bal, counted)

		var (
			quoteID  uuid.UUID
			quoteUSD int64
			quoteWei string
			quoteRt  int64
		)
		err := tx.QueryRow(ctx, `
			SELECT id, usd_micros, amount_wei::text, rate_micros FROM eth_quotes
			WHERE user_id = $1 AND status = 'open' AND expires_at + make_interval(secs => $2) > now()
			ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, userID, quoteSlack.Seconds()).
			Scan(&quoteID, &quoteUSD, &quoteWei, &quoteRt)
		hasQuote := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		used := rate
		if hasQuote {
			used = quoteRt
		} else if rateErr != nil {
			return rateErr // try again next tick rather than guess a price
		}
		credit := microsFor(delta, used)
		if hasQuote {
			want, _ := new(big.Int).SetString(quoteWei, 10)
			floor := new(big.Int).Mul(want, big.NewInt(10_000-d.Cfg.ToleranceBps))
			floor.Quo(floor, big.NewInt(10_000))
			if delta.Cmp(floor) >= 0 && credit.Cmp(big.NewInt(quoteUSD)) < 0 {
				credit = big.NewInt(quoteUSD)
			}
		}
		if !credit.IsInt64() || credit.Int64() < d.Cfg.DustMicros {
			return nil // too small to credit yet; it keeps adding up
		}
		micros := credit.Int64()

		var quoteArg any
		if hasQuote {
			quoteArg = quoteID
			if _, err := tx.Exec(ctx, `UPDATE eth_quotes SET status = 'credited' WHERE id = $1`, quoteID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO crypto_deposits (user_id, asset, amount_atomic, credit_micros, rate_micros, quote_id, status, block_number)
			VALUES ($1, 'eth', $2::numeric, $3, $4, $5, 'credited', $6)`,
			userID, delta.String(), micros, used, quoteArg, safe); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deposit_addresses SET eth_accounted_wei = $2::numeric WHERE user_id = $1`,
			userID, bal.String()); err != nil {
			return err
		}
		return d.Ledger.SubscribeTx(ctx, tx, userID,
			Plan{ID: "eth", PriceMicros: micros, CreditMicros: micros}, "base:eth:"+delta.String())
	})
}

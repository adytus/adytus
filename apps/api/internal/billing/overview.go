package billing

import (
	"context"
	"math/big"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// Overview is the operator's view of the money: what was credited to each
// user and what actually sits on each deposit address now.
type Overview struct {
	Enabled bool   `json:"enabled"`
	ChainID int64  `json:"chain_id,omitempty"`
	USDC    string `json:"usdc,omitempty"`
	// Path is the derivation path of address i, for a wallet that imports the seed.
	Path string `json:"path,omitempty"`
	// ETHUSDMicros is the live ETH price; 0 when the feed could not be read.
	ETHUSDMicros int64             `json:"eth_usd_micros"`
	Addresses    []AddressOverview `json:"addresses"`
	Totals       OverviewTotals    `json:"totals"`
}

type AddressOverview struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	// Index is the child index; AccountNumber is what a wallet calls it,
	// counting from 1 (index 0 is the wallet's first account and is unused).
	Index         int64  `json:"index"`
	AccountNumber int64  `json:"account_number"`
	Address       string `json:"address"`

	Deposits        int        `json:"deposits"`
	CreditedMicros  int64      `json:"credited_micros"`
	CreditedTokens  int64      `json:"credited_tokens"`
	LastDepositedAt *time.Time `json:"last_deposited_at"`

	// On-chain now. Empty when the node could not be asked (see BalanceError).
	USDCMicros   int64  `json:"usdc_micros"`
	ETHWei       string `json:"eth_wei"`
	ETHUSDMicros int64  `json:"eth_usd_micros"`
	BalanceError string `json:"balance_error,omitempty"`
}

type OverviewTotals struct {
	CreditedMicros int64  `json:"credited_micros"`
	USDCMicros     int64  `json:"usdc_micros"`
	ETHWei         string `json:"eth_wei"`
	ETHUSDMicros   int64  `json:"eth_usd_micros"`
	OnChainMicros  int64  `json:"on_chain_micros"`
}

const overviewLimit = 500

// Overview lists deposit addresses with credited totals and live balances.
func (d *Deposits) Overview(ctx context.Context) (Overview, error) {
	o := Overview{
		Enabled: true, ChainID: d.Cfg.ChainID, USDC: d.Cfg.USDC,
		Path: "m/44'/60'/0'/0/{index}", Addresses: []AddressOverview{},
	}
	rows, err := d.DB.Query(ctx, `
		SELECT a.user_id, u.email, a.idx, a.address,
			COUNT(c.id) FILTER (WHERE c.status = 'credited'),
			COALESCE(SUM(c.credit_micros) FILTER (WHERE c.status = 'credited'), 0),
			MAX(c.created_at) FILTER (WHERE c.status = 'credited')
		FROM deposit_addresses a
		JOIN users u ON u.id = a.user_id
		LEFT JOIN crypto_deposits c ON c.user_id = a.user_id
		WHERE a.address IS NOT NULL
		GROUP BY a.user_id, u.email, a.idx, a.address
		ORDER BY a.idx
		LIMIT $1`, overviewLimit)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AddressOverview
		if err := rows.Scan(&a.UserID, &a.Email, &a.Index, &a.Address,
			&a.Deposits, &a.CreditedMicros, &a.LastDepositedAt); err != nil {
			return o, err
		}
		a.AccountNumber = a.Index + 1
		a.CreditedTokens = Tokens(a.CreditedMicros)
		a.ETHWei = "0"
		o.Addresses = append(o.Addresses, a)
	}
	if err := rows.Err(); err != nil {
		return o, err
	}

	if rate, err := d.liveRate(ctx); err == nil {
		o.ETHUSDMicros = rate
	}

	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(8)
	for i := range o.Addresses {
		a := &o.Addresses[i]
		g.Go(func() error {
			usdc, uerr := d.Chain.TokenBalance(ctx, d.Cfg.USDC, a.Address)
			eth, eerr := d.Chain.Balance(ctx, a.Address)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case uerr != nil:
				a.BalanceError = uerr.Error()
			case eerr != nil:
				a.BalanceError = eerr.Error()
			default:
				if usdc.IsInt64() {
					a.USDCMicros = usdc.Int64()
				}
				a.ETHWei = eth.String()
				if o.ETHUSDMicros > 0 {
					if v := microsFor(eth, o.ETHUSDMicros); v.IsInt64() {
						a.ETHUSDMicros = v.Int64()
					}
				}
			}
			return nil
		})
	}
	_ = g.Wait()

	totalWei := new(big.Int)
	for _, a := range o.Addresses {
		o.Totals.CreditedMicros += a.CreditedMicros
		o.Totals.USDCMicros += a.USDCMicros
		o.Totals.ETHUSDMicros += a.ETHUSDMicros
		wei, _ := new(big.Int).SetString(a.ETHWei, 10)
		totalWei.Add(totalWei, wei)
	}
	o.Totals.ETHWei = totalWei.String()
	o.Totals.OnChainMicros = o.Totals.USDCMicros + o.Totals.ETHUSDMicros
	return o, nil
}

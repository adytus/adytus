package near

import (
	"context"
	"fmt"
	"math/big"
)

// Deposit is one payment as the deposit contract (contracts/near-deposit)
// logged it. Token is empty for native NEAR, else the NEP-141 contract that
// called ft_on_transfer, so a payment can't claim to be a token it isn't.
type Deposit struct {
	Memo        string `json:"memo"`
	Token       string `json:"token"`
	Sender      string `json:"sender"`
	Amount      string `json:"amount"`
	BlockHeight int64  `json:"block_height"`
	TimestampMs int64  `json:"timestamp_ms"`
}

// AmountInt is Amount as an integer of base units.
func (d Deposit) AmountInt() (*big.Int, error) {
	n, ok := new(big.Int).SetString(d.Amount, 10)
	if !ok || n.Sign() < 0 {
		return nil, fmt.Errorf("deposit amount %q", d.Amount)
	}
	return n, nil
}

// DepositCount is how many deposits the contract has logged.
func (c *Client) DepositCount(ctx context.Context, contract string) (int64, error) {
	var n int64
	err := c.View(ctx, contract, "deposits_count", map[string]any{}, &n)
	return n, err
}

// Deposits returns up to limit logged deposits, starting at index from.
func (c *Client) Deposits(ctx context.Context, contract string, from, limit int64) ([]Deposit, error) {
	var out []Deposit
	err := c.View(ctx, contract, "deposits", map[string]any{"from_index": from, "limit": limit}, &out)
	return out, err
}

// AcceptedTokens are the NEP-141 tokens the contract takes.
func (c *Client) AcceptedTokens(ctx context.Context, contract string) ([]string, error) {
	var out []string
	err := c.View(ctx, contract, "tokens", map[string]any{}, &out)
	return out, err
}

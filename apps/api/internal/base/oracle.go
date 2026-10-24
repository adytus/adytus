package base

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// latestRoundData() selector.
var latestRoundData = []byte{0xfe, 0xaf, 0x96, 0x8c}

// ETHUSD reads a Chainlink ETH/USD aggregator and returns the price in
// micro-USD per ETH. The feed reports 8 decimals. A round older than maxAge
// is refused, so a stalled feed cannot price a deposit.
func (c *Client) ETHUSD(ctx context.Context, feed string, maxAge time.Duration) (int64, error) {
	out, err := c.Call(ctx, feed, latestRoundData)
	if err != nil {
		return 0, err
	}
	// (roundId, answer, startedAt, updatedAt, answeredInRound), 32 bytes each.
	if len(out) < 5*32 {
		return 0, fmt.Errorf("price feed answered %d bytes", len(out))
	}
	answer := new(big.Int).SetBytes(out[32:64])
	if out[32]&0x80 != 0 || answer.Sign() == 0 {
		return 0, errors.New("price feed answered a non-positive price")
	}
	updated := new(big.Int).SetBytes(out[96:128])
	if !updated.IsInt64() || time.Since(time.Unix(updated.Int64(), 0)) > maxAge {
		return 0, errors.New("price feed is stale")
	}
	micros := new(big.Int).Div(answer, big.NewInt(100)) // 8 decimals -> 6
	if !micros.IsInt64() {
		return 0, errors.New("price out of range")
	}
	return micros.Int64(), nil
}

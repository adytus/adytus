// Package billing holds subscription plans and the payment gateway interface.
// Payments are mocked until a real gateway is chosen.
package billing

import (
	"context"

	"github.com/google/uuid"
)

const (
	MicrosPerUSD = 1_000_000
	// MicrosPerToken is the size of one user-facing credit token. Users see
	// balances and charges as whole tokens; money is still kept in micro-USD
	// in the ledger. $10 buys 10,000 tokens.
	MicrosPerToken = 1_000
)

// Tokens converts micro-USD to token credits, rounding away from zero so a
// charge is never smaller than the amount it covers.
func Tokens(micros int64) int64 {
	if micros >= 0 {
		return (micros + MicrosPerToken - 1) / MicrosPerToken
	}
	return -((-micros + MicrosPerToken - 1) / MicrosPerToken)
}

// RoundMicros rounds a micro-USD amount up to a whole token credit.
func RoundMicros(micros int64) int64 {
	return Tokens(micros) * MicrosPerToken
}

type Plan struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PriceMicros int64  `json:"price_micros"`
	// CreditMicros is what the user is credited, at the catalog prices they
	// see. It equals the price; the margin over the upstream cost lives in
	// those prices, so catalog prices are set above OpenRouter's.
	CreditMicros int64 `json:"-"`
	// CreditTokens is CreditMicros in the token credits users see.
	CreditTokens int64 `json:"credit_tokens"`
}

var Plans = []Plan{
	{
		ID:           "standard",
		Name:         "Standard",
		PriceMicros:  10 * MicrosPerUSD,
		CreditMicros: 10 * MicrosPerUSD,
		CreditTokens: Tokens(10 * MicrosPerUSD),
	},
}

func PlanByID(id string) (Plan, bool) {
	for _, p := range Plans {
		if p.ID == id {
			return p, true
		}
	}
	return Plan{}, false
}

type Gateway interface {
	// Charge collects payment and returns a reference to store with the
	// subscription.
	Charge(ctx context.Context, userID uuid.UUID, plan Plan) (ref string, err error)
}

// MockGateway approves every charge.
type MockGateway struct{}

func (MockGateway) Charge(context.Context, uuid.UUID, Plan) (string, error) {
	return "mock_" + uuid.NewString(), nil
}

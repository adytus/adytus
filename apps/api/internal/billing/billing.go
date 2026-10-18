// Package billing holds subscription plans and the payment gateway interface.
// Payments are mocked until a real gateway is chosen.
package billing

import (
	"context"

	"github.com/google/uuid"
)

const MicrosPerUSD = 1_000_000

type Plan struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PriceMicros int64  `json:"price_micros"`
	// CreditMicros is what the user is credited, at the catalog prices they
	// see. It equals the price; the margin over the upstream cost lives in
	// those prices, so catalog prices are set above OpenRouter's.
	CreditMicros int64 `json:"-"`
}

var Plans = []Plan{
	{ID: "standard", Name: "Standard", PriceMicros: 10 * MicrosPerUSD, CreditMicros: 10 * MicrosPerUSD},
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

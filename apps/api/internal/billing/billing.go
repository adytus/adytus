// Package billing holds the credit ledger and the crypto deposit watcher.
package billing

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

// Plan is a purchase that credits a user: Price is what was paid and Credit
// what the user receives. They are equal; the margin over the upstream cost
// lives in the catalog prices, which are set above OpenRouter's.
type Plan struct {
	ID           string
	PriceMicros  int64
	CreditMicros int64
}

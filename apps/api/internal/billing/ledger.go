package billing

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
)

// Ledger tracks credit in micro-USD. The flow for an LLM request is:
//
//	Reserve(estimated max cost) -> stream -> Settle(actual cost)
//
// Reserving checks both the available balance and the daily spend cap, under
// a per-user row lock so concurrent requests cannot overspend.
type Ledger struct {
	DB *pgxpool.Pool
	// DailyCapMicros limits usage per user per UTC day; 0 disables it.
	DailyCapMicros int64
	HoldTTL        time.Duration
}

type Balance struct {
	CreditMicros     int64 `json:"credit_micros"`
	HeldMicros       int64 `json:"held_micros"`
	AvailableMicros  int64 `json:"available_micros"`
	SpentTodayMicros int64 `json:"spent_today_micros"`
	DailyCapMicros   int64 `json:"daily_cap_micros"`
	// Lifetime totals: credit bought and credit used.
	PurchasedMicros int64 `json:"purchased_micros"`
	UsedMicros      int64 `json:"used_micros"`

	// The same amounts in user-facing token credits.
	CreditTokens     int64 `json:"credit_tokens"`
	HeldTokens       int64 `json:"held_tokens"`
	AvailableTokens  int64 `json:"available_tokens"`
	SpentTodayTokens int64 `json:"spent_today_tokens"`
	DailyCapTokens   int64 `json:"daily_cap_tokens"`
	PurchasedTokens  int64 `json:"purchased_tokens"`
	UsedTokens       int64 `json:"used_tokens"`
}

type Entry struct {
	ID               int64       `json:"id"`
	DeltaMicros      int64       `json:"delta_micros"`
	DeltaTokens      int64       `json:"delta_tokens"`
	Reason           string      `json:"reason"`
	Model            null.String `json:"model"`
	PromptTokens     null.Int32  `json:"prompt_tokens"`
	CompletionTokens null.Int32  `json:"completion_tokens"`
	CreatedAt        time.Time   `json:"created_at"`
}

type Usage struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	CostMicros       int64
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (l *Ledger) balance(ctx context.Context, q querier, userID uuid.UUID) (Balance, error) {
	b := Balance{DailyCapMicros: l.DailyCapMicros}
	err := q.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT SUM(delta_micros) FROM credit_ledger WHERE user_id = $1), 0),
			COALESCE((SELECT SUM(amount_micros) FROM credit_holds WHERE user_id = $1 AND expires_at > now()), 0),
			COALESCE((SELECT -SUM(delta_micros) FROM credit_ledger
				WHERE user_id = $1 AND reason = 'usage' AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0),
			COALESCE((SELECT SUM(delta_micros) FROM credit_ledger WHERE user_id = $1 AND reason = 'subscription'), 0),
			COALESCE((SELECT -SUM(delta_micros) FROM credit_ledger WHERE user_id = $1 AND reason = 'usage'), 0)`,
		userID).Scan(&b.CreditMicros, &b.HeldMicros, &b.SpentTodayMicros, &b.PurchasedMicros, &b.UsedMicros)
	b.AvailableMicros = b.CreditMicros - b.HeldMicros
	b.CreditTokens = Tokens(b.CreditMicros)
	b.HeldTokens = Tokens(b.HeldMicros)
	b.AvailableTokens = Tokens(b.AvailableMicros)
	b.SpentTodayTokens = Tokens(b.SpentTodayMicros)
	b.DailyCapTokens = Tokens(b.DailyCapMicros)
	b.PurchasedTokens = Tokens(b.PurchasedMicros)
	b.UsedTokens = Tokens(b.UsedMicros)
	return b, err
}

func (l *Ledger) Balance(ctx context.Context, userID uuid.UUID) (Balance, error) {
	return l.balance(ctx, l.DB, userID)
}

var (
	ErrNoCredit      = errs.BudgetExceeded.New("not enough credit for this request; top up your plan")
	ErrDailyCapSpent = errs.BudgetExceeded.New("daily usage cap reached; try again tomorrow")
)

// Reserve places a hold for the worst-case cost of a request.
func (l *Ledger) Reserve(ctx context.Context, userID uuid.UUID, amount int64) (uuid.UUID, error) {
	amount = RoundMicros(amount)
	if amount < MicrosPerToken {
		amount = MicrosPerToken
	}
	var holdID uuid.UUID
	err := pgx.BeginFunc(ctx, l.DB, func(tx pgx.Tx) error {
		// Serialize reservations per user.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM credit_holds WHERE user_id = $1 AND expires_at <= now()`, userID); err != nil {
			return err
		}
		b, err := l.balance(ctx, tx, userID)
		if err != nil {
			return err
		}
		if b.AvailableMicros < amount {
			return ErrNoCredit
		}
		if l.DailyCapMicros > 0 && b.SpentTodayMicros+b.HeldMicros+amount > l.DailyCapMicros {
			return ErrDailyCapSpent
		}
		return tx.QueryRow(ctx, `
			INSERT INTO credit_holds (user_id, amount_micros, expires_at)
			VALUES ($1, $2, now() + make_interval(secs => $3)) RETURNING id`,
			userID, amount, l.HoldTTL.Seconds()).Scan(&holdID)
	})
	return holdID, err
}

// Settle replaces a hold with the actual usage charge. The charge may exceed
// the hold slightly if the estimate was low; the balance can then dip below
// zero and the next Reserve will refuse.
func (l *Ledger) Settle(ctx context.Context, userID, holdID uuid.UUID, u Usage) error {
	charge := RoundMicros(u.CostMicros)
	return pgx.BeginFunc(ctx, l.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM credit_holds WHERE id = $1`, holdID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO credit_ledger (user_id, delta_micros, reason, model, prompt_tokens, completion_tokens)
			VALUES ($1, $2, 'usage', $3, $4, $5)`,
			userID, -charge, u.Model, u.PromptTokens, u.CompletionTokens)
		return err
	})
}

// Release drops a hold without charging (the request never reached the
// provider).
func (l *Ledger) Release(ctx context.Context, holdID uuid.UUID) error {
	_, err := l.DB.Exec(ctx, `DELETE FROM credit_holds WHERE id = $1`, holdID)
	return err
}

// Subscribe records a paid plan and credits the user.
func (l *Ledger) Subscribe(ctx context.Context, userID uuid.UUID, plan Plan, paymentRef string) error {
	return pgx.BeginFunc(ctx, l.DB, func(tx pgx.Tx) error {
		return l.SubscribeTx(ctx, tx, userID, plan, paymentRef)
	})
}

// SubscribeTx is Subscribe inside the caller's transaction, so a payment can
// be marked as handled and credited atomically.
func (l *Ledger) SubscribeTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, plan Plan, paymentRef string) error {
	var subID uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO subscriptions (user_id, plan, paid_micros, credit_micros, payment_ref)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		userID, plan.ID, plan.PriceMicros, plan.CreditMicros, paymentRef).Scan(&subID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO credit_ledger (user_id, delta_micros, reason, subscription_id)
		VALUES ($1, $2, 'subscription', $3)`, userID, plan.CreditMicros, subID)
	return err
}

func (l *Ledger) History(ctx context.Context, userID uuid.UUID, limit int) ([]Entry, error) {
	rows, err := l.DB.Query(ctx, `
		SELECT id, delta_micros, reason, model, prompt_tokens, completion_tokens, created_at
		FROM credit_ledger WHERE user_id = $1 ORDER BY id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.DeltaMicros, &e.Reason, &e.Model, &e.PromptTokens, &e.CompletionTokens, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.DeltaTokens = Tokens(e.DeltaMicros)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

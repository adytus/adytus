package billing

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
)

// voucherGrace keeps a voucher good for payments already on their way when it
// expires; it covers an ETH quote locked at the last moment.
const voucherGrace = 30 * time.Minute

var voucherCode = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

// Voucher is a code as the user who applied it sees it.
type Voucher struct {
	Code      string    `json:"code"`
	Percent   int64     `json:"percent"`
	ExpiresAt time.Time `json:"expires_at"`
}

// VoucherReport is a voucher with what it has cost so far.
type VoucherReport struct {
	ID uuid.UUID `json:"id"`
	Voucher
	CreatedAt   time.Time `json:"created_at"`
	Uses        int64     `json:"uses"`
	PaidMicros  int64     `json:"paid_micros"`
	BonusMicros int64     `json:"bonus_micros"`
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ClaimVoucher applies a code to the user's deposits, replacing any other.
func (d *Deposits) ClaimVoucher(ctx context.Context, userID uuid.UUID, code string) (Voucher, error) {
	var id uuid.UUID
	var v Voucher
	err := d.DB.QueryRow(ctx, `
		SELECT id, code, percent, expires_at FROM vouchers WHERE code = $1 AND expires_at > now()`,
		normalizeCode(code)).Scan(&id, &v.Code, &v.Percent, &v.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errs.NotFound.New("that code isn't valid or has expired")
	}
	if err != nil {
		return v, err
	}
	_, err = d.DB.Exec(ctx, `
		INSERT INTO voucher_claims (user_id, voucher_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET voucher_id = EXCLUDED.voucher_id, created_at = now()`, userID, id)
	return v, err
}

func (d *Deposits) RemoveVoucher(ctx context.Context, userID uuid.UUID) error {
	_, err := d.DB.Exec(ctx, `DELETE FROM voucher_claims WHERE user_id = $1`, userID)
	return err
}

// claimed is the unexpired voucher the user has applied, or nil.
func (d *Deposits) claimed(ctx context.Context, userID uuid.UUID) (*Voucher, error) {
	var v Voucher
	err := d.DB.QueryRow(ctx, `
		SELECT v.code, v.percent, v.expires_at
		FROM voucher_claims c JOIN vouchers v ON v.id = c.voucher_id
		WHERE c.user_id = $1 AND v.expires_at > now()`, userID).Scan(&v.Code, &v.Percent, &v.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// discount is the voucher a deposit is credited under; the zero value is none.
type discount struct {
	id      any
	percent int64
}

// credit is the list price that paying micros covers, rounded down.
func (v discount) credit(micros int64) int64 {
	if v.percent == 0 {
		return micros
	}
	return micros/(100-v.percent)*100 + micros%(100-v.percent)*100/(100-v.percent)
}

func discountFor(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (discount, error) {
	var id uuid.UUID
	var v discount
	err := tx.QueryRow(ctx, `
		SELECT v.id, v.percent
		FROM voucher_claims c JOIN vouchers v ON v.id = c.voucher_id
		WHERE c.user_id = $1 AND v.expires_at + make_interval(secs => $2) > now()`,
		userID, voucherGrace.Seconds()).Scan(&id, &v.percent)
	if errors.Is(err, pgx.ErrNoRows) {
		return discount{}, nil
	}
	if err != nil {
		return discount{}, err
	}
	v.id = id
	return v, nil
}

// CreateVoucher adds a code. Codes are stored upper-case and matched that way.
func (d *Deposits) CreateVoucher(ctx context.Context, code string, percent int64, expires time.Time) error {
	code = normalizeCode(code)
	if !voucherCode.MatchString(code) {
		return errs.Validation.New("codes are 3 to 32 letters, digits, dashes or underscores")
	}
	if percent < 1 || percent > 90 {
		return errs.Validation.New("the discount must be between 1 and 90 percent")
	}
	if !expires.After(time.Now()) {
		return errs.Validation.New("the expiry must be in the future")
	}
	tag, err := d.DB.Exec(ctx, `
		INSERT INTO vouchers (code, percent, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (code) DO NOTHING`, code, percent, expires)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.Conflict.New("%s already exists", code)
	}
	return nil
}

// EndVoucher expires a voucher now. Payments already on their way still get
// voucherGrace, as with any expiry.
func (d *Deposits) EndVoucher(ctx context.Context, id uuid.UUID) error {
	tag, err := d.DB.Exec(ctx, `UPDATE vouchers SET expires_at = now() WHERE id = $1 AND expires_at > now()`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound.New("no running voucher with that id")
	}
	return nil
}

func (d *Deposits) VoucherReports(ctx context.Context) ([]VoucherReport, error) {
	rows, err := d.DB.Query(ctx, `
		SELECT v.id, v.code, v.percent, v.expires_at, v.created_at, count(c.id),
			COALESCE(SUM(c.credit_micros - c.bonus_micros), 0)::bigint, COALESCE(SUM(c.bonus_micros), 0)::bigint
		FROM vouchers v LEFT JOIN crypto_deposits c ON c.voucher_id = v.id AND c.status = 'credited'
		GROUP BY v.id ORDER BY v.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VoucherReport{}
	for rows.Next() {
		var r VoucherReport
		if err := rows.Scan(&r.ID, &r.Code, &r.Percent, &r.ExpiresAt, &r.CreatedAt, &r.Uses, &r.PaidMicros, &r.BonusMicros); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

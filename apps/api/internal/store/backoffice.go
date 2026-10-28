package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

// IsAdmin reports whether the user may open the backoffice. It's read on
// every backoffice request, so revoking it takes effect at once.
func (s *Store) IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	var admin bool
	err := s.DB.QueryRow(ctx, `SELECT is_admin FROM users WHERE id = $1`, userID).Scan(&admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errs.NotFound.New("user not found")
	}
	return admin, err
}

// --- providers ---

type ProviderRow struct {
	ID        string
	BaseURL   string
	SealedKey []byte
	UpdatedAt time.Time
}

func Providers(ctx context.Context, q DBTX) ([]ProviderRow, error) {
	rows, err := q.Query(ctx, `SELECT id, base_url, sealed_key, updated_at FROM llm_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ProviderRow])
}

// SeedProvider inserts a provider unless it's already there.
func SeedProvider(ctx context.Context, q DBTX, p ProviderRow) error {
	_, err := q.Exec(ctx, `
		INSERT INTO llm_providers (id, base_url, sealed_key) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING`, p.ID, p.BaseURL, p.SealedKey)
	return err
}

// UpdateProvider sets the base URL and, when setKey is true, the key (nil
// clears it).
func UpdateProvider(ctx context.Context, q DBTX, by uuid.UUID, id, baseURL string, setKey bool, sealedKey []byte) error {
	tag, err := q.Exec(ctx, `
		UPDATE llm_providers
		SET base_url = $3, sealed_key = CASE WHEN $4 THEN $5 ELSE sealed_key END, updated_by = $2, updated_at = now()
		WHERE id = $1`, id, by, baseURL, setKey, sealedKey)
	if err == nil && tag.RowsAffected() == 0 {
		return errs.NotFound.New("no provider %q", id)
	}
	return err
}

// --- models ---

type ModelRow struct {
	Spec    llm.Model
	Enabled bool
	Default bool
	// Single marks single model mode's one model.
	Single    bool
	UpdatedAt time.Time
}

// Models returns every model, enabled or not, in picker order.
func Models(ctx context.Context, q DBTX) ([]ModelRow, error) {
	rows, err := q.Query(ctx, `
		SELECT spec, enabled, is_default, is_single, updated_at FROM llm_models ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ModelRow])
}

func CountModels(ctx context.Context, q DBTX) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM llm_models`).Scan(&n)
	return n, err
}

// UpsertModel saves a model's spec and switch. A new model goes last.
// Disabling the one single model turns single model mode off.
func UpsertModel(ctx context.Context, q DBTX, by uuid.UUID, m llm.Model, enabled bool) error {
	_, err := q.Exec(ctx, `
		INSERT INTO llm_models (id, provider, spec, enabled, position, updated_by)
		VALUES ($1, $2, $3, $4, (SELECT coalesce(max(position), 0) + 1 FROM llm_models), $5)
		ON CONFLICT (id) DO UPDATE
		SET provider = excluded.provider, spec = excluded.spec, enabled = excluded.enabled,
		    is_single = llm_models.is_single AND excluded.enabled,
		    updated_by = excluded.updated_by, updated_at = now()`,
		m.ID, m.Provider, m, enabled, uuid.NullUUID{UUID: by, Valid: by != uuid.Nil})
	return err
}

func DeleteModel(ctx context.Context, q DBTX, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM llm_models WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errs.NotFound.New("no model %q", id)
	}
	return err
}

// SetSingleModel makes id the only model users are offered; an empty id turns
// single model mode off.
func SetSingleModel(ctx context.Context, q DBTX, id string) error {
	if id == "" {
		_, err := q.Exec(ctx, `UPDATE llm_models SET is_single = false WHERE is_single`)
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE llm_models SET is_single = false WHERE is_single AND id <> $1`, id); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE llm_models SET is_single = true WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errs.NotFound.New("no model %q", id)
	}
	return err
}

func SetDefaultModel(ctx context.Context, q DBTX, id string) error {
	if _, err := q.Exec(ctx, `UPDATE llm_models SET is_default = false WHERE is_default AND id <> $1`, id); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE llm_models SET is_default = true WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errs.NotFound.New("no model %q", id)
	}
	return err
}

// --- users ---

// AdminUser is one account's usage summary for the backoffice. It contains no
// chats or files, only billing and token counts.
type AdminUser struct {
	ID               uuid.UUID
	Email            string
	IsAdmin          bool
	CreatedAt        time.Time
	CreditMicros     int64
	HeldMicros       int64
	PurchasedMicros  int64
	UsedMicros       int64
	SpentTodayMicros int64
	PromptTokens     int64
	CompletionTokens int64
	Requests         int64
}

func Users(ctx context.Context, q DBTX) ([]AdminUser, error) {
	rows, err := q.Query(ctx, `
		SELECT u.id, u.email, u.is_admin, u.created_at,
			COALESCE(l.credit_micros, 0), COALESCE(h.held_micros, 0),
			COALESCE(l.purchased_micros, 0), COALESCE(l.used_micros, 0),
			COALESCE(l.spent_today_micros, 0), COALESCE(l.prompt_tokens, 0),
			COALESCE(l.completion_tokens, 0), COALESCE(l.requests, 0)
		FROM users u
		LEFT JOIN (
			SELECT user_id,
				SUM(delta_micros) AS credit_micros,
				COALESCE(SUM(delta_micros) FILTER (WHERE reason = 'subscription'), 0) AS purchased_micros,
				COALESCE(-SUM(delta_micros) FILTER (WHERE reason = 'usage'), 0) AS used_micros,
				COALESCE(-SUM(delta_micros) FILTER (WHERE reason = 'usage'
					AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0) AS spent_today_micros,
				COALESCE(SUM(prompt_tokens) FILTER (WHERE reason = 'usage'), 0) AS prompt_tokens,
				COALESCE(SUM(completion_tokens) FILTER (WHERE reason = 'usage'), 0) AS completion_tokens,
				COUNT(*) FILTER (WHERE reason = 'usage') AS requests
			FROM credit_ledger GROUP BY user_id
		) l ON l.user_id = u.id
		LEFT JOIN (
			SELECT user_id, SUM(amount_micros) AS held_micros
			FROM credit_holds WHERE expires_at > now() GROUP BY user_id
		) h ON h.user_id = u.id
		ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Email, &u.IsAdmin, &u.CreatedAt,
			&u.CreditMicros, &u.HeldMicros, &u.PurchasedMicros, &u.UsedMicros,
			&u.SpentTodayMicros, &u.PromptTokens, &u.CompletionTokens, &u.Requests); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

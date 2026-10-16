// Package store holds the Postgres queries. It only ever sees ciphertext and
// wrapped keys.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
)

// DBTX is satisfied by both the pool and a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct{ DB *pgxpool.Pool }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- users ---

type User struct {
	ID                uuid.UUID
	Email             string
	AuthVerifier      []byte
	KDFSalt           []byte
	KDF               crypto.KDFParams
	WrappedMK         []byte
	RecoveryWrappedMK []byte
	CreatedAt         time.Time
}

const userCols = `id, email, auth_verifier, kdf_salt, kdf_params, wrapped_mk, recovery_wrapped_mk, created_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	var kdf []byte
	err := row.Scan(&u.ID, &u.Email, &u.AuthVerifier, &u.KDFSalt, &kdf, &u.WrappedMK, &u.RecoveryWrappedMK, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.NotFound.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(kdf, &u.KDF); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	kdf, _ := json.Marshal(u.KDF)
	err := s.DB.QueryRow(ctx, `
		INSERT INTO users (id, email, auth_verifier, kdf_salt, kdf_params, wrapped_mk, recovery_wrapped_mk)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at`,
		u.ID, u.Email, u.AuthVerifier, u.KDFSalt, kdf, u.WrappedMK, u.RecoveryWrappedMK,
	).Scan(&u.CreatedAt)
	if isUniqueViolation(err) {
		return errs.Conflict.New("an account with this email already exists")
	}
	return err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email))
}

func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

// UpdateUserKeys stores re-wrapped keys after a password change or recovery.
func (s *Store) UpdateUserKeys(ctx context.Context, u *User) error {
	kdf, _ := json.Marshal(u.KDF)
	_, err := s.DB.Exec(ctx, `
		UPDATE users SET auth_verifier = $2, kdf_salt = $3, kdf_params = $4, wrapped_mk = $5, recovery_wrapped_mk = $6
		WHERE id = $1`,
		u.ID, u.AuthVerifier, u.KDFSalt, kdf, u.WrappedMK, u.RecoveryWrappedMK)
	return err
}

// --- objects ---

const (
	KindChat    = "chat"
	KindMessage = "message"
	KindFile    = "file"
)

type Object struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	ParentID         uuid.NullUUID
	Kind             string
	WrappedDEK       []byte
	ServerWrappedDEK []byte
	Ciphertext       []byte
	S3Key            null.String
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const objectCols = `id, user_id, parent_id, kind, wrapped_dek, server_wrapped_dek, ciphertext, s3_key, created_at, updated_at`

func scanObject(row pgx.Row) (*Object, error) {
	var o Object
	err := row.Scan(&o.ID, &o.UserID, &o.ParentID, &o.Kind, &o.WrappedDEK, &o.ServerWrappedDEK, &o.Ciphertext, &o.S3Key, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.NotFound.New("not found")
	}
	return &o, err
}

func InsertObject(ctx context.Context, q DBTX, o *Object) error {
	return q.QueryRow(ctx, `
		INSERT INTO objects (id, user_id, parent_id, kind, wrapped_dek, server_wrapped_dek, ciphertext, s3_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`,
		o.ID, o.UserID, o.ParentID, o.Kind, o.WrappedDEK, o.ServerWrappedDEK, o.Ciphertext, o.S3Key,
	).Scan(&o.CreatedAt, &o.UpdatedAt)
}

func (s *Store) InsertObject(ctx context.Context, o *Object) error { return InsertObject(ctx, s.DB, o) }

// GetObject returns one of the user's objects. Objects of other users are
// reported as not found.
func (s *Store) GetObject(ctx context.Context, userID, id uuid.UUID, kind string) (*Object, error) {
	return scanObject(s.DB.QueryRow(ctx,
		`SELECT `+objectCols+` FROM objects WHERE id = $1 AND user_id = $2 AND kind = $3`, id, userID, kind))
}

func (s *Store) collect(ctx context.Context, sql string, args ...any) ([]*Object, error) {
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Object
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListObjects returns the user's top-level objects of a kind, most recently
// updated first.
func (s *Store) ListObjects(ctx context.Context, userID uuid.UUID, kind string) ([]*Object, error) {
	return s.collect(ctx, `SELECT `+objectCols+` FROM objects
		WHERE user_id = $1 AND kind = $2 AND parent_id IS NULL
		ORDER BY updated_at DESC`, userID, kind)
}

// Children returns child objects in creation order (IDs are UUIDv7).
func (s *Store) Children(ctx context.Context, userID, parentID uuid.UUID) ([]*Object, error) {
	return s.collect(ctx, `SELECT `+objectCols+` FROM objects
		WHERE user_id = $1 AND parent_id = $2
		ORDER BY id`, userID, parentID)
}

// UpdateCiphertext replaces an object's contents. It leaves updated_at
// alone: renaming a chat is not activity, so it shouldn't move the chat up
// the list. Touch marks activity.
func UpdateCiphertext(ctx context.Context, q DBTX, id uuid.UUID, ciphertext []byte) error {
	_, err := q.Exec(ctx, `UPDATE objects SET ciphertext = $2 WHERE id = $1`, id, ciphertext)
	return err
}

func (s *Store) UpdateCiphertext(ctx context.Context, id uuid.UUID, ciphertext []byte) error {
	return UpdateCiphertext(ctx, s.DB, id, ciphertext)
}

func Touch(ctx context.Context, q DBTX, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE objects SET updated_at = now() WHERE id = $1`, id)
	return err
}

func SetServerWrappedDEK(ctx context.Context, q DBTX, id uuid.UUID, wrapped []byte) error {
	_, err := q.Exec(ctx, `UPDATE objects SET server_wrapped_dek = $2 WHERE id = $1`, id, wrapped)
	return err
}

// DeleteObject deletes an object and its children and returns the S3 keys
// that should be removed afterwards.
func (s *Store) DeleteObject(ctx context.Context, userID, id uuid.UUID, kind string) ([]string, error) {
	var keys []string
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s3_key FROM objects
			WHERE user_id = $1 AND (id = $2 OR parent_id = $2) AND s3_key IS NOT NULL`, userID, id)
		if err != nil {
			return err
		}
		keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM objects WHERE id = $1 AND user_id = $2 AND kind = $3`, id, userID, kind)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errs.NotFound.New("not found")
		}
		return nil
	})
	return keys, err
}

func (s *Store) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.DB, fn)
}

// --- settings ---

type Settings struct {
	Tools map[string]bool `json:"tools"`
	// AutoTitle has the model name new chats; nil means on.
	AutoTitle *bool `json:"auto_title,omitempty"`
}

func (st Settings) AutoTitleOn() bool { return st.AutoTitle == nil || *st.AutoTitle }

func (s *Store) Settings(ctx context.Context, userID uuid.UUID) (Settings, error) {
	var st Settings
	err := s.DB.QueryRow(ctx, `SELECT settings FROM users WHERE id = $1`, userID).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, errs.NotFound.New("user not found")
	}
	if st.Tools == nil {
		st.Tools = map[string]bool{}
	}
	return st, err
}

func (s *Store) SetSettings(ctx context.Context, userID uuid.UUID, st Settings) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET settings = $2 WHERE id = $1`, userID, st)
	return err
}

package server

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/near"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

const sessionCookie = "adytus_session"

type authCtx struct {
	UserID uuid.UUID
	// MK is the user's master key for this request; zeroed after the handler.
	MK    []byte
	Token string
}

func mustAuth(c fiber.Ctx) *authCtx { return c.Locals("auth").(*authCtx) }

// isBackoffice reports whether the request belongs to an admin. It works on
// routes without requireSession too, so public responses can keep their
// admin-only details hidden.
func (s *Server) isBackoffice(c fiber.Ctx) bool {
	if a, ok := c.Locals("auth").(*authCtx); ok {
		admin, err := s.Store.IsAdmin(c.Context(), a.UserID)
		return err == nil && admin
	}
	userID, mk, ok := s.Sessions.Get(c.Cookies(sessionCookie))
	if !ok {
		return false
	}
	crypto.Zero(mk)
	admin, err := s.Store.IsAdmin(c.Context(), userID)
	return err == nil && admin
}

func (s *Server) requireSession(c fiber.Ctx) error {
	token := c.Cookies(sessionCookie)
	userID, mk, ok := s.Sessions.Get(token)
	if !ok {
		return errs.Unauthorized.New("not logged in")
	}
	c.Locals("auth", &authCtx{UserID: userID, MK: mk, Token: token})
	defer crypto.Zero(mk)
	return c.Next()
}

func (s *Server) setSessionCookie(c fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HTTPOnly: true,
		Secure:   s.Config.CookieSecure,
		SameSite: fiber.CookieSameSiteStrictMode,
		Expires:  time.Now().Add(s.Config.SessionTTL),
	})
}

// deriveKeys runs Argon2id with bounded concurrency.
func (s *Server) deriveKeys(c fiber.Ctx, password string, salt []byte, p crypto.KDFParams) ([]byte, []byte, error) {
	select {
	case s.kdfSlots <- struct{}{}:
	case <-c.Context().Done():
		return nil, nil, c.Context().Err()
	}
	defer func() { <-s.kdfSlots }()
	auth, kek := crypto.DeriveKeys(password, salt, p)
	return auth, kek, nil
}

type credentials struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=12,max=256"`
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// wrapUserKeys (re)derives everything that depends on the password and the
// recovery code: verifier, salt, wrapped MK and recovery-wrapped MK. It
// returns the new recovery code to show the user once.
func (s *Server) wrapUserKeys(c fiber.Ctx, u *store.User, password string, mk []byte) (string, error) {
	salt, err := crypto.RandomBytes(crypto.SaltSize)
	if err != nil {
		return "", err
	}
	authKey, kek, err := s.deriveKeys(c, password, salt, s.Config.KDF)
	if err != nil {
		return "", err
	}
	defer crypto.Zero(kek)

	code, err := crypto.NewRecoveryCode()
	if err != nil {
		return "", err
	}
	recKey := crypto.RecoveryKey(code)
	defer crypto.Zero(recKey)

	u.KDFSalt, u.KDF, u.AuthVerifier = salt, s.Config.KDF, crypto.AuthVerifier(authKey)
	if u.WrappedMK, err = crypto.Wrap(kek, mk, vault.AADUserMK(u.ID)); err != nil {
		return "", err
	}
	if u.RecoveryWrappedMK, err = crypto.Wrap(recKey, mk, vault.AADRecovery(u.ID)); err != nil {
		return "", err
	}
	return code, nil
}

type userView struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func viewUser(u *store.User) userView { return userView{u.ID, u.Email, u.CreatedAt} }

func (s *Server) signup(c fiber.Ctx) error {
	var req credentials
	if err := c.Bind().Body(&req); err != nil {
		return err
	}

	mk, err := crypto.NewKey()
	if err != nil {
		return err
	}
	defer crypto.Zero(mk)

	u := &store.User{ID: uuid.Must(uuid.NewV7()), Email: normalizeEmail(req.Email)}
	code, err := s.wrapUserKeys(c, u, req.Password, mk)
	if err != nil {
		return err
	}
	if err := s.Store.CreateUser(c.Context(), u); err != nil {
		return err
	}

	token, err := s.Sessions.Create(u.ID, mk)
	if err != nil {
		return err
	}
	s.setSessionCookie(c, token)
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"user": viewUser(u), "recovery_code": code})
}

var errBadLogin = errs.Unauthorized.New("wrong email or password")

// dummySalt keeps login timing the same for unknown emails.
var dummySalt = make([]byte, crypto.SaltSize)

func (s *Server) login(c fiber.Ctx) error {
	var req struct {
		Email    string `json:"email" validate:"required,email"`
		Password string `json:"password" validate:"required,max=256"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}

	u, err := s.Store.UserByEmail(c.Context(), normalizeEmail(req.Email))
	if err != nil {
		if _, _, kdfErr := s.deriveKeys(c, req.Password, dummySalt, s.Config.KDF); kdfErr != nil {
			return kdfErr
		}
		if errs.IsNotFound(err) {
			return errBadLogin
		}
		return err
	}

	authKey, kek, err := s.deriveKeys(c, req.Password, u.KDFSalt, u.KDF)
	if err != nil {
		return err
	}
	defer crypto.Zero(kek)
	if !crypto.VerifyAuth(authKey, u.AuthVerifier) {
		return errBadLogin
	}
	mk, err := crypto.Unwrap(kek, u.WrappedMK, vault.AADUserMK(u.ID))
	if err != nil {
		return errs.Internal.Wrap(err, "unwrap master key")
	}
	defer crypto.Zero(mk)

	token, err := s.Sessions.Create(u.ID, mk)
	if err != nil {
		return err
	}
	s.setSessionCookie(c, token)
	return c.JSON(fiber.Map{"user": viewUser(u)})
}

func (s *Server) logout(c fiber.Ctx) error {
	s.Sessions.Delete(c.Cookies(sessionCookie))
	c.ClearCookie(sessionCookie)
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) me(c fiber.Ctx) error {
	a := mustAuth(c)
	u, err := s.Store.UserByID(c.Context(), a.UserID)
	if err != nil {
		return err
	}
	privs, err := s.privileges(c, a)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": viewUser(u), "privileges": privs})
}

// changePassword re-wraps the master key; no object needs re-encrypting.
// Other sessions are logged out and a new recovery code is issued.
func (s *Server) changePassword(c fiber.Ctx) error {
	var req struct {
		Current string `json:"current_password" validate:"required,max=256"`
		New     string `json:"new_password" validate:"required,min=12,max=256"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	a := mustAuth(c)
	u, err := s.Store.UserByID(c.Context(), a.UserID)
	if err != nil {
		return err
	}
	authKey, kek, err := s.deriveKeys(c, req.Current, u.KDFSalt, u.KDF)
	if err != nil {
		return err
	}
	crypto.Zero(kek)
	if !crypto.VerifyAuth(authKey, u.AuthVerifier) {
		return errs.Forbidden.New("current password is wrong")
	}

	code, err := s.wrapUserKeys(c, u, req.New, a.MK)
	if err != nil {
		return err
	}
	if err := s.Store.UpdateUserKeys(c.Context(), u); err != nil {
		return err
	}
	s.Sessions.DeleteUser(u.ID, a.Token)
	return c.JSON(fiber.Map{"recovery_code": code})
}

// recover sets a new password using the recovery code. This is the only way
// back in after a lost password; nobody else can unlock the data.
func (s *Server) recover(c fiber.Ctx) error {
	var req struct {
		// An email, or a NEAR account for accounts made with Login with NEAR.
		Email        string `json:"email" validate:"required,max=254"`
		RecoveryCode string `json:"recovery_code" validate:"required,max=64"`
		NewPassword  string `json:"new_password" validate:"required,min=12,max=256"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	errBadRecovery := errs.Unauthorized.New("wrong email or recovery code")
	if id := normalizeEmail(req.Email); !strings.Contains(id, "@") && !near.ValidAccountID(id) {
		return errs.Validation.New("enter your email or NEAR account")
	}

	u, err := s.Store.UserByEmail(c.Context(), normalizeEmail(req.Email))
	if errs.IsNotFound(err) || (err == nil && u.RecoveryWrappedMK == nil) {
		return errBadRecovery
	}
	if err != nil {
		return err
	}
	recKey := crypto.RecoveryKey(req.RecoveryCode)
	mk, err := crypto.Unwrap(recKey, u.RecoveryWrappedMK, vault.AADRecovery(u.ID))
	crypto.Zero(recKey)
	if err != nil {
		return errBadRecovery
	}
	defer crypto.Zero(mk)

	code, err := s.wrapUserKeys(c, u, req.NewPassword, mk)
	if err != nil {
		return err
	}
	if err := s.Store.UpdateUserKeys(c.Context(), u); err != nil {
		return err
	}
	s.Sessions.DeleteUser(u.ID, "")
	token, err := s.Sessions.Create(u.ID, mk)
	if err != nil {
		return err
	}
	s.setSessionCookie(c, token)
	return c.JSON(fiber.Map{"user": viewUser(u), "recovery_code": code})
}

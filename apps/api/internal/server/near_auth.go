package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/near"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// Login with NEAR. The wallet signs a one-time challenge (NEP-413) with a
// full-access key of the account, which proves who the user is. It can't
// open their data: that still takes a passphrase, which wraps the master key
// exactly as a password does. So a NEAR account is an account whose sign-in
// name is "alice.near" (kept in users.email; it has no "@", so it never
// clashes with an email) and whose sign-in needs both the wallet and the
// passphrase.
//
// The network and RPC are the ones set for NEAR payments in the backoffice.

const (
	nearLoginMessage = "Sign in to Adytus"
	nearRecipient    = "adytus"
	nearNonceTTL     = 5 * time.Minute
)

// nearNonces remembers challenges already used to sign in, until they expire.
type nearNonces struct {
	mu   sync.Mutex
	used map[string]time.Time
}

func (n *nearNonces) take(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if n.used == nil {
		n.used = map[string]time.Time{}
	}
	for k, exp := range n.used {
		if now.After(exp) {
			delete(n.used, k)
		}
	}
	if _, ok := n.used[nonce]; ok {
		return false
	}
	n.used[nonce] = now.Add(nearNonceTTL)
	return true
}

func (s *Server) nonceKey() []byte {
	m := hmac.New(sha256.New, s.Config.ServerKey)
	m.Write([]byte("adytus near-login nonce"))
	return m.Sum(nil)
}

// newNearNonce is 8 bytes of time, 8 random and a 16-byte MAC over both, so
// the server can check a challenge without storing it.
func (s *Server) newNearNonce() ([]byte, error) {
	b := make([]byte, 32)
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().Unix()))
	if _, err := rand.Read(b[8:16]); err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, s.nonceKey())
	m.Write(b[:16])
	copy(b[16:], m.Sum(nil)[:16])
	return b, nil
}

func (s *Server) checkNearNonce(b []byte) bool {
	if len(b) != 32 {
		return false
	}
	m := hmac.New(sha256.New, s.nonceKey())
	m.Write(b[:16])
	if !hmac.Equal(b[16:], m.Sum(nil)[:16]) {
		return false
	}
	age := time.Since(time.Unix(int64(binary.BigEndian.Uint64(b[:8])), 0))
	return age >= -time.Minute && age <= nearNonceTTL
}

var errNearLoginOff = errs.NotFound.New("Login with NEAR is switched off")

// nearLoginStatus tells the login page whether to offer Login with NEAR.
func (s *Server) nearLoginStatus(c fiber.Ctx) error {
	settings, err := s.Near.Settings(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"enabled": settings.LoginEnabled, "network": settings.Network})
}

// nearChallenge hands out what the wallet should sign.
func (s *Server) nearChallenge(c fiber.Ctx) error {
	settings, err := s.Near.Settings(c.Context())
	if err != nil {
		return err
	}
	if !settings.LoginEnabled {
		return errNearLoginOff
	}
	nonce, err := s.newNearNonce()
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{
		"message":   nearLoginMessage,
		"recipient": nearRecipient,
		"nonce":     base64.StdEncoding.EncodeToString(nonce),
		"network":   settings.Network,
	})
}

// nearLogin signs in, or with create set makes the account, after checking
// the wallet's signature and the passphrase.
func (s *Server) nearLogin(c fiber.Ctx) error {
	var req struct {
		near.SignedMessage
		Nonce       string `json:"nonce" validate:"required"`
		CallbackURL string `json:"callback_url" validate:"max=2048"`
		Passphrase  string `json:"passphrase" validate:"required,max=256"`
		Create      bool   `json:"create"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	settings, err := s.Near.Settings(c.Context())
	if err != nil {
		return err
	}
	if !settings.LoginEnabled {
		return errNearLoginOff
	}
	account := strings.ToLower(strings.TrimSpace(req.AccountID))
	if !near.ValidAccountID(account) {
		return errs.Validation.New("that isn't a NEAR account")
	}
	nonce, err := base64.StdEncoding.DecodeString(req.Nonce)
	if err != nil || !s.checkNearNonce(nonce) {
		return errs.Unauthorized.New("the sign-in request expired; try again")
	}
	if err := near.VerifyNEP413(req.SignedMessage, nearLoginMessage, nonce, nearRecipient, nil); err != nil {
		cb := req.CallbackURL
		if cb == "" || near.VerifyNEP413(req.SignedMessage, nearLoginMessage, nonce, nearRecipient, &cb) != nil {
			return errs.Unauthorized.New("the wallet's signature didn't check out")
		}
	}

	ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
	full, err := s.Near.Client(settings).IsFullAccessKey(ctx, account, req.PublicKey)
	cancel()
	if err != nil {
		return errs.Upstream.Wrap(err, "couldn't reach NEAR to check the key")
	}
	if !full {
		return errs.Unauthorized.New("that key isn't a full-access key of %s on %s", account, settings.Network)
	}

	u, err := s.Store.UserByEmail(c.Context(), account)
	switch {
	case errs.IsNotFound(err) && !req.Create:
		return errs.NotFound.New("there's no Adytus account for %s yet", account)
	case errs.IsNotFound(err):
		if len(req.Passphrase) < 12 {
			return errs.Validation.New("the passphrase is at least 12 characters")
		}
		if !s.nearUsed.take(req.Nonce) {
			return errs.Unauthorized.New("the sign-in request was already used; try again")
		}
		return s.createNearUser(c, account, req.Passphrase)
	case err != nil:
		return err
	}

	authKey, kek, err := s.deriveKeys(c, req.Passphrase, u.KDFSalt, u.KDF)
	if err != nil {
		return err
	}
	defer crypto.Zero(kek)
	if !crypto.VerifyAuth(authKey, u.AuthVerifier) {
		return errs.Unauthorized.New("wrong passphrase")
	}
	if !s.nearUsed.take(req.Nonce) {
		return errs.Unauthorized.New("the sign-in request was already used; try again")
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

func (s *Server) createNearUser(c fiber.Ctx, account, passphrase string) error {
	mk, err := crypto.NewKey()
	if err != nil {
		return err
	}
	defer crypto.Zero(mk)
	u := &store.User{ID: uuid.Must(uuid.NewV7()), Email: account}
	code, err := s.wrapUserKeys(c, u, passphrase, mk)
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

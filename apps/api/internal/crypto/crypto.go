// Package crypto implements the Adytus key hierarchy:
//
//	password --Argon2id--> auth key + KEK
//	KEK wraps the user's master key (MK)
//	MK wraps each object's data key (DEK)
//	DEK encrypts the object
//
// Optionally, a DEK is also wrapped with the server key so background jobs can
// read that object while the user is offline (opt-in per object).
package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	KeySize  = chacha20poly1305.KeySize
	SaltSize = 16
)

var ErrDecrypt = errors.New("crypto: decryption failed")

// KDFParams are stored per user so they can be raised later without breaking
// existing accounts.
type KDFParams struct {
	Time      uint32 `json:"t"`
	MemoryKiB uint32 `json:"m"`
	Threads   uint8  `json:"p"`
}

var DefaultKDF = KDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

// DeriveKeys runs Argon2id once and splits the output into an auth key (used
// only to verify the password) and a KEK (used only to wrap the master key).
func DeriveKeys(password string, salt []byte, p KDFParams) (authKey, kek []byte) {
	out := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, 2*KeySize)
	return out[:KeySize], out[KeySize:]
}

// AuthVerifier is what gets stored to check a login. It is a hash of the auth
// key, so a database leak does not reveal the KEK.
func AuthVerifier(authKey []byte) []byte {
	sum := sha256.Sum256(authKey)
	return sum[:]
}

func VerifyAuth(authKey, verifier []byte) bool {
	return subtle.ConstantTimeCompare(AuthVerifier(authKey), verifier) == 1
}

func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func NewKey() ([]byte, error) { return RandomBytes(KeySize) }

// Seal encrypts with XChaCha20-Poly1305 and returns nonce || ciphertext.
// aad binds the ciphertext to its context (e.g. object ID) so blobs cannot be
// swapped between rows.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce, err := RandomBytes(aead.NonceSize())
	if err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func Open(key, sealed, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, ct := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Wrap and Unwrap are Seal/Open applied to keys; kept separate for readability
// at call sites.
func Wrap(kek, key, aad []byte) ([]byte, error)       { return Seal(kek, key, aad) }
func Unwrap(kek, wrapped, aad []byte) ([]byte, error) { return Open(kek, wrapped, aad) }

// Zero overwrites key material once it is no longer needed.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

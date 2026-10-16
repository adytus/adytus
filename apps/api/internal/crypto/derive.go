package crypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryCode returns a 160-bit code formatted for humans, e.g.
// "ABCD-EFGH-...". It can unwrap the master key if the password is lost.
func NewRecoveryCode() (string, error) {
	raw, err := RandomBytes(20)
	if err != nil {
		return "", err
	}
	enc := recoveryEncoding.EncodeToString(raw) // 32 chars
	var groups []string
	for i := 0; i < len(enc); i += 4 {
		groups = append(groups, enc[i:i+4])
	}
	return strings.Join(groups, "-"), nil
}

// RecoveryKey derives the key that wraps the master key from a recovery
// code. The code carries 160 bits of entropy, so a fast KDF is enough.
// Dashes, spaces and case are ignored.
func RecoveryKey(code string) []byte {
	norm := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	key, err := hkdf.Key(sha256.New, []byte(norm), nil, "adytus recovery v1", KeySize)
	if err != nil {
		panic(err) // only fails for invalid lengths
	}
	return key
}

// SessionKey derives the key that seals a session's in-memory copy of the
// master key from the session token, which only the browser holds.
func SessionKey(token []byte) []byte {
	key, err := hkdf.Key(sha256.New, token, nil, "adytus session v1", KeySize)
	if err != nil {
		panic(err)
	}
	return key
}

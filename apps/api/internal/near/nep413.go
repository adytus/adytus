package near

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
)

// nep413Tag prefixes a signed message, 2^31 + 413, so it can never be a
// valid transaction.
const nep413Tag uint32 = 1<<31 + 413

// SignedMessage is a wallet's answer to a NEP-413 sign-message request.
type SignedMessage struct {
	AccountID string `json:"account_id"`
	// PublicKey is "ed25519:<base58>".
	PublicKey string `json:"public_key"`
	// Signature is base64.
	Signature string `json:"signature"`
}

// VerifyNEP413 checks sig is the signature of the NEP-413 payload
// (message, nonce, recipient, callbackURL) by sig.PublicKey. Some wallets
// sign the callback URL they were given and some don't, so callbackURL may
// be checked both ways by the caller.
func VerifyNEP413(sig SignedMessage, message string, nonce []byte, recipient string, callbackURL *string) error {
	if len(nonce) != 32 {
		return errors.New("the nonce is 32 bytes")
	}
	pub, err := ParseEd25519Key(sig.PublicKey)
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("the signature isn't a base64 ed25519 signature")
	}
	hash := sha256.Sum256(nep413Payload(message, nonce, recipient, callbackURL))
	if !ed25519.Verify(pub, hash[:], raw) {
		return errors.New("the signature doesn't match")
	}
	return nil
}

// nep413Payload is the borsh encoding of
// { tag: u32, message: string, nonce: [u8; 32], recipient: string, callbackUrl: Option<string> }.
func nep413Payload(message string, nonce []byte, recipient string, callbackURL *string) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, nep413Tag)
	b = borshString(b, message)
	b = append(b, nonce...)
	b = borshString(b, recipient)
	if callbackURL == nil {
		b = append(b, 0)
	} else {
		b = append(b, 1)
		b = borshString(b, *callbackURL)
	}
	return b
}

func borshString(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// ParseEd25519Key reads a NEAR public key, "ed25519:<base58>".
func ParseEd25519Key(s string) (ed25519.PublicKey, error) {
	rest, ok := strings.CutPrefix(s, "ed25519:")
	if !ok {
		return nil, errors.New("only ed25519 keys are supported")
	}
	raw := base58.Decode(rest)
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("the public key isn't a valid ed25519 key")
	}
	return ed25519.PublicKey(raw), nil
}

// IsFullAccessKey reports whether publicKey is a full-access key of account.
// Only a full-access key proves the account itself signed; a function-call
// key can be held by any app the user ever logged in to.
func (c *Client) IsFullAccessKey(ctx context.Context, account, publicKey string) (bool, error) {
	var r struct {
		Permission json.RawMessage `json:"permission"`
	}
	err := c.call(ctx, "query", map[string]any{
		"request_type": "view_access_key",
		"finality":     "final",
		"account_id":   account,
		"public_key":   publicKey,
	}, &r)
	if err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_ACCESS_KEY") || strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}
		return false, err
	}
	var perm string
	if json.Unmarshal(r.Permission, &perm) == nil {
		return perm == "FullAccess", nil
	}
	return false, nil // {"FunctionCall": {...}}
}


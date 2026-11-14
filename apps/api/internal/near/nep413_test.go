package near

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
)

func nearKey(seed byte) (ed25519.PrivateKey, string) {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}
	priv := ed25519.NewKeyFromSeed(s)
	return priv, "ed25519:" + base58.Encode(priv.Public().(ed25519.PublicKey))
}

func signNEP413(priv ed25519.PrivateKey, msg string, nonce []byte, recipient string, cb *string) string {
	h := sha256.Sum256(nep413Payload(msg, nonce, recipient, cb))
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, h[:]))
}

func TestVerifyNEP413(t *testing.T) {
	priv, pub := nearKey(7)
	nonce := make([]byte, 32)
	nonce[0] = 9
	sig := SignedMessage{AccountID: "alice.testnet", PublicKey: pub, Signature: signNEP413(priv, "Sign in to Adytus", nonce, "adytus", nil)}
	if err := VerifyNEP413(sig, "Sign in to Adytus", nonce, "adytus", nil); err != nil {
		t.Fatal(err)
	}
	if VerifyNEP413(sig, "Sign in to Adytus", nonce, "evil", nil) == nil {
		t.Fatal("other recipient accepted")
	}
	other := append([]byte{}, nonce...)
	other[1] = 1
	if VerifyNEP413(sig, "Sign in to Adytus", other, "adytus", nil) == nil {
		t.Fatal("other nonce accepted")
	}
	cb := "https://adytus.app/login"
	if VerifyNEP413(sig, "Sign in to Adytus", nonce, "adytus", &cb) == nil {
		t.Fatal("signature without a callback accepted as one with it")
	}
	sig.Signature = signNEP413(priv, "Sign in to Adytus", nonce, "adytus", &cb)
	if err := VerifyNEP413(sig, "Sign in to Adytus", nonce, "adytus", &cb); err != nil {
		t.Fatal(err)
	}
	_, otherPub := nearKey(8)
	sig.PublicKey = otherPub
	if VerifyNEP413(sig, "Sign in to Adytus", nonce, "adytus", &cb) == nil {
		t.Fatal("other key accepted")
	}
	if _, err := ParseEd25519Key("secp256k1:abc"); err == nil {
		t.Fatal("secp256k1 key accepted")
	}
}

func TestIsFullAccessKey(t *testing.T) {
	srv := rpc(t, map[string]func(map[string]any) (any, *rpcError){
		"query:view_access_key": func(p map[string]any) (any, *rpcError) {
			switch p["public_key"] {
			case "ed25519:full":
				return map[string]any{"nonce": 1, "permission": "FullAccess"}, nil
			case "ed25519:fc":
				return map[string]any{"nonce": 1, "permission": map[string]any{"FunctionCall": map[string]any{"receiver_id": "app.near"}}}, nil
			}
			return nil, &rpcError{Name: "HANDLER_ERROR", Cause: map[string]any{"name": "UNKNOWN_ACCESS_KEY"}}
		},
	})
	c := NewClient(srv.URL)
	for key, want := range map[string]bool{"ed25519:full": true, "ed25519:fc": false, "ed25519:none": false} {
		got, err := c.IsFullAccessKey(context.Background(), "alice.near", key)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v", key, got, err)
		}
	}
}

// TestPrintNEP413Vector prints a signature for the JS cross-check
// (NEP413_VECTOR=1); it checks nothing on its own.
func TestPrintNEP413Vector(t *testing.T) {
	priv, pub := nearKey(7)
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	cb := "https://adytus.app/login"
	t.Logf("VECTOR %s %s %s %s", pub, base64.StdEncoding.EncodeToString(nonce),
		signNEP413(priv, "Sign in to Adytus", nonce, "adytus", nil),
		signNEP413(priv, "Sign in to Adytus", nonce, "adytus", &cb))
}

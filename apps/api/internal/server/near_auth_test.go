package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"

	"github.com/reaganiwadha/adytus/apps/api/internal/near"
)

// nearWallet signs NEP-413 challenges like a wallet would.
type nearWallet struct {
	priv ed25519.PrivateKey
	pub  string
}

func newNearWallet(seed byte) nearWallet {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}
	priv := ed25519.NewKeyFromSeed(s)
	return nearWallet{priv, "ed25519:" + base58.Encode(priv.Public().(ed25519.PublicKey))}
}

func (w nearWallet) sign(message, recipient string, nonce []byte) string {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 1<<31+413)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(message)))
	b = append(b, message...)
	b = append(b, nonce...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(recipient)))
	b = append(b, recipient...)
	b = append(b, 0) // no callback URL
	h := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(w.priv, h[:]))
}

// fullAccessNode answers view_access_key: keys in full are full-access.
func fullAccessNode(t *testing.T, full map[string]bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		key, _ := req.Params["public_key"].(string)
		if full[key] {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"adytus","result":{"nonce":1,"permission":"FullAccess"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"adytus","error":{"name":"HANDLER_ERROR","cause":{"name":"UNKNOWN_ACCESS_KEY"}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginWithNEAR(t *testing.T) {
	e := newEnv(t)
	alice := newNearWallet(1)
	mallory := newNearWallet(2)
	node := fullAccessNode(t, map[string]bool{alice.pub: true})
	e.srv.Near.NewClient = func(string) *near.Client { return near.NewClient(node.URL) }

	challenge := func() (string, []byte, map[string]any) {
		ch := e.must(200, "GET", "/api/auth/near/challenge", nil)
		nonce, _ := base64.StdEncoding.DecodeString(ch["nonce"].(string))
		return ch["nonce"].(string), nonce, ch
	}
	login := func(status int, w nearWallet, account, pass string, create bool) map[string]any {
		t.Helper()
		nb64, nonce, ch := challenge()
		return e.must(status, "POST", "/api/auth/near", map[string]any{
			"account_id": account, "public_key": w.pub,
			"signature": w.sign(ch["message"].(string), ch["recipient"].(string), nonce),
			"nonce":     nb64, "passphrase": pass, "create": create,
		})
	}

	// Not signed up yet: the UI is told so, and asks for a passphrase.
	login(404, alice, "alice.testnet", "correct horse battery", false)
	login(400, alice, "alice.testnet", "short", true)
	out := login(201, alice, "alice.testnet", "correct horse battery", true)
	if out["user"].(map[string]any)["email"] != "alice.testnet" || out["recovery_code"] == "" {
		t.Fatalf("signup %v", out)
	}
	e.must(200, "GET", "/api/me", nil)
	e.must(204, "POST", "/api/auth/logout", nil)

	// Signing in needs the wallet and the passphrase.
	login(401, alice, "alice.testnet", "wrong passphrase!!", false)
	login(200, alice, "alice.testnet", "correct horse battery", false)
	e.must(200, "GET", "/api/me", nil)
	e.must(204, "POST", "/api/auth/logout", nil)

	// Another key, even with the right passphrase, is refused: it isn't a
	// full-access key of the account.
	login(401, mallory, "alice.testnet", "correct horse battery", false)

	// A signature over one challenge doesn't work twice.
	nb64, nonce, ch := challenge()
	body := map[string]any{
		"account_id": "alice.testnet", "public_key": alice.pub,
		"signature": alice.sign(ch["message"].(string), ch["recipient"].(string), nonce),
		"nonce":     nb64, "passphrase": "correct horse battery",
	}
	e.must(200, "POST", "/api/auth/near", body)
	e.must(401, "POST", "/api/auth/near", body)

	// A made-up nonce isn't one the server issued.
	fake := make([]byte, 32)
	body["nonce"] = base64.StdEncoding.EncodeToString(fake)
	body["signature"] = alice.sign(ch["message"].(string), ch["recipient"].(string), fake)
	e.must(401, "POST", "/api/auth/near", body)

	// The recovery code works with the NEAR account as the name.
	e.must(204, "POST", "/api/auth/logout", nil)
	e.must(200, "POST", "/api/auth/recover", map[string]any{
		"email": "alice.testnet", "recovery_code": out["recovery_code"], "new_password": "another long passphrase",
	})
	login(200, alice, "alice.testnet", "another long passphrase", false)

	// Switched off in the backoffice, it's gone: no button, no challenge.
	if st := e.must(200, "GET", "/api/auth/near", nil); st["enabled"] != true {
		t.Fatalf("status %v", st)
	}
	settings, err := e.srv.Near.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.LoginEnabled = false
	if _, err := e.srv.Near.SaveSettings(context.Background(), settings, e.userID(t)); err != nil {
		t.Fatal(err)
	}
	if st := e.must(200, "GET", "/api/auth/near", nil); st["enabled"] != false {
		t.Fatalf("status after switching off %v", st)
	}
	e.must(404, "GET", "/api/auth/near/challenge", nil)
	e.must(404, "POST", "/api/auth/near", body)
}

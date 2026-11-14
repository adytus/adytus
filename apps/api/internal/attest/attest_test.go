package attest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// Hardhat's account #0: a published key with a known Ethereum address.
const (
	hardhatKey  = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	hardhatAddr = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
)

// sign makes a personal_sign signature (r || s || v, v = 27/28), as an
// Ethereum wallet or NEAR AI's enclave would.
func sign(t *testing.T, text string) string {
	t.Helper()
	raw, _ := hex.DecodeString(hardhatKey)
	priv, _ := btcec.PrivKeyFromBytes(raw)
	compact := ecdsa.SignCompact(priv, personalHash(text), false)
	// btcec gives v || r || s with v = 27 + id.
	sig := append(append([]byte{}, compact[1:]...), compact[0])
	return "0x" + hex.EncodeToString(sig)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestRecoverAddress(t *testing.T) {
	sig := sign(t, "hello")
	got, err := RecoverAddress("hello", sig)
	if err != nil || got != hardhatAddr {
		t.Fatalf("got %s, %v; want %s", got, err, hardhatAddr)
	}
	// v as 0/1 works too.
	b, _ := hex.DecodeString(sig[2:])
	b[64] -= 27
	if got, err := RecoverAddress("hello", hex.EncodeToString(b)); err != nil || got != hardhatAddr {
		t.Fatalf("v=0/1: got %s, %v", got, err)
	}
	// A different message recovers a different signer.
	if got, _ := RecoverAddress("hellO", sig); got == hardhatAddr {
		t.Fatal("tampered text recovered the same signer")
	}
	if _, err := RecoverAddress("hello", "0x1234"); err == nil {
		t.Fatal("short signature accepted")
	}
}

func TestVerify(t *testing.T) {
	req, resp := sha(`{"model":"m"}`), sha("data: {}\n\ndata: [DONE]\n\n")
	text := req + ":" + resp
	good := Signature{Text: text, Signature: sign(t, text), SigningAddress: strings.ToLower(hardhatAddr), SigningAlgo: "ecdsa"}

	if r := Verify(req, resp, good); r.Status != Verified || r.Signer != hardhatAddr {
		t.Fatalf("good: %+v", r)
	}
	cases := map[string]struct {
		req, resp string
		sig       Signature
		want      string
	}{
		"other request":  {sha("x"), resp, good, "request hash"},
		"other response": {req, sha("x"), good, "response hash"},
		"other signer":   {req, resp, Signature{Text: text, Signature: good.Signature, SigningAddress: "0x0000000000000000000000000000000000000001"}, "signed by"},
		"bad signature":  {req, resp, Signature{Text: text, Signature: "0xzz", SigningAddress: hardhatAddr}, "doesn't parse"},
		"bad text":       {req, resp, Signature{Text: "nocolon", Signature: sign(t, "nocolon"), SigningAddress: hardhatAddr}, "isn't request_hash"},
	}
	for name, c := range cases {
		r := Verify(c.req, c.resp, c.sig)
		if r.Status != Mismatch || !strings.Contains(r.Detail, c.want) {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := Verify(req, resp, Signature{Text: text, Signature: good.Signature, SigningAlgo: "ed25519"}); r.Status != Unavailable {
		t.Errorf("ed25519: %+v", r)
	}
}

func TestNearAISignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/signature/chatcmpl-1" || r.URL.Query().Get("model") != "deepseek/v4" ||
			r.URL.Query().Get("signing_algo") != "ecdsa" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("request %s %v", r.URL, r.Header)
		}
		w.Write([]byte(`{"text":"a:b","signature":"0x01","signing_address":"0xabc","signing_algo":"ecdsa"}`))
	}))
	defer srv.Close()
	s, err := NewNearAI(srv.URL+"/v1/", "k").Signature(context.Background(), "chatcmpl-1", "deepseek/v4")
	if err != nil || s.Text != "a:b" || s.SigningAddress != "0xabc" {
		t.Fatalf("%+v, %v", s, err)
	}
}

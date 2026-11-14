package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"golang.org/x/crypto/sha3"

	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/attest"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

// fakeSignatures answers with a canned signature per completion ID.
type fakeSignatures map[string]attest.Signature

func (f fakeSignatures) SignatureWithRetry(_ context.Context, id, _ string) (attest.Signature, error) {
	if s, ok := f[id]; ok {
		return s, nil
	}
	return attest.Signature{}, errors.New("http 404")
}

func TestInferenceReceipts(t *testing.T) {
	e := newEnv(t)
	e.signup("r@example.com")
	e.fund()
	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "cheap"})["chat"].(map[string]any)["id"].(string)
	_, events := e.sendMessage(chatID, "hi")
	msgID := uuid.MustParse(last(events).Data["message"].(map[string]any)["id"].(string))

	// The signer can't be faked in this package; a signature that doesn't
	// parse is enough to show a mismatch is stored as one.
	e.srv.Signatures = func(llm.Provider) SignatureSource {
		return fakeSignatures{"cmpl-bad": {Text: "a:b", Signature: "0x00", SigningAddress: "0x01", SigningAlgo: "ecdsa"}}
	}
	e.srv.recordReceipts(msgID, e.userID(t), llm.Provider{ID: llm.NearAI}, []llm.Call{
		{Model: "up", CompletionID: "cmpl-bad", RequestSHA256: "a", ResponseSHA256: "b", Complete: true},
		{Model: "up", CompletionID: "cmpl-gone", RequestSHA256: "c", ResponseSHA256: "d", Complete: true},
		{Model: "up", CompletionID: "cmpl-cut", RequestSHA256: "e", ResponseSHA256: "f", Complete: false},
	})

	rs := e.must(200, "GET", "/api/chats/"+chatID+"/receipts", nil)["receipts"].([]any)
	if len(rs) != 3 {
		t.Fatalf("receipts %v", rs)
	}
	want := []string{attest.Mismatch, attest.Unavailable, attest.Unavailable}
	for i, r := range rs {
		m := r.(map[string]any)
		if m["status"] != want[i] || m["message_id"] != msgID.String() || num(m["seq"]) != int64(i) || m["detail"] == "" {
			t.Errorf("receipt %d: %v", i, m)
		}
	}

	// Recording again changes nothing.
	e.srv.recordReceipts(msgID, e.userID(t), llm.Provider{ID: llm.NearAI}, []llm.Call{{CompletionID: "x", Complete: true}})
	if rs := e.must(200, "GET", "/api/chats/"+chatID+"/receipts", nil)["receipts"].([]any); len(rs) != 3 {
		t.Fatalf("receipts after a repeat %d", len(rs))
	}

	// Someone else sees none of them.
	e.must(204, "POST", "/api/auth/logout", nil)
	e.signup("other@example.com")
	if rs := e.must(200, "GET", "/api/chats/"+chatID+"/receipts", nil)["receipts"].([]any); len(rs) != 0 {
		t.Fatalf("another user saw %d receipts", len(rs))
	}

	// Deleting the chat takes its receipts with it.
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM objects WHERE id = $1`, chatID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM inference_receipts`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d receipts outlived their chat", n)
	}
}

// fakeReports answers every report request with one enclave entry for
// signer, its quote bound to whatever nonce was asked for.
type fakeReports struct {
	signer string
	calls  *int
}

func (f fakeReports) Report(_ context.Context, model, nonce string) (*attest.Report, error) {
	*f.calls++
	quote := make([]byte, 632)
	addr, _ := hex.DecodeString(strings.TrimPrefix(strings.ToLower(f.signer), "0x"))
	n, _ := hex.DecodeString(nonce)
	copy(quote[568:], addr)
	copy(quote[600:], n)
	return &attest.Report{Models: []attest.Attestation{{ModelName: model, SigningAddress: f.signer, IntelQuote: hex.EncodeToString(quote)}}}, nil
}

// signWith makes a personal_sign signature (r || s || v) with a test key.
func signWith(t *testing.T, keyHex, text string) string {
	t.Helper()
	raw, _ := hex.DecodeString(keyHex)
	priv, _ := btcec.PrivKeyFromBytes(raw)
	msg := "\x19Ethereum Signed Message:\n" + strconv.Itoa(len(text)) + text
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(msg))
	c := ecdsa.SignCompact(priv, h.Sum(nil), false)
	return "0x" + hex.EncodeToString(append(append([]byte{}, c[1:]...), c[0]))
}

func TestPublicVerify(t *testing.T) {
	const key = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80" // hardhat #0
	const signer = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
	e := newEnv(t)
	e.signup("v@example.com")
	e.fund()
	chatID := e.must(201, "POST", "/api/chats", map[string]string{"model": "cheap"})["chat"].(map[string]any)["id"].(string)
	_, events := e.sendMessage(chatID, "a secret question")
	msgID := uuid.MustParse(last(events).Data["message"].(map[string]any)["id"].(string))

	reqH, respH := strings.Repeat("a", 64), strings.Repeat("b", 64)
	text := reqH + ":" + respH
	e.srv.Signatures = func(llm.Provider) SignatureSource {
		return fakeSignatures{"cmpl-ok": {Text: text, Signature: signWith(t, key, text), SigningAddress: signer, SigningAlgo: "ecdsa"}}
	}
	calls := 0
	e.srv.Attestations = func() AttestationSource { return fakeReports{signer: signer, calls: &calls} }
	e.srv.recordReceipts(msgID, e.userID(t), llm.Provider{ID: llm.NearAI}, []llm.Call{
		{Model: "up", CompletionID: "cmpl-ok", RequestSHA256: reqH, ResponseSHA256: respH, Complete: true},
	})

	// No session needed, and the chat itself isn't in the answer.
	e.must(204, "POST", "/api/auth/logout", nil)
	resp, out := e.do("GET", "/api/verify/"+msgID.String(), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), chatID) {
		t.Fatalf("the proof leaks the chat: %s", raw)
	}
	rs := out["receipts"].([]any)
	if len(rs) != 1 {
		t.Fatalf("receipts %v", rs)
	}
	ch := rs[0].(map[string]any)["checks"].(map[string]any)
	for _, k := range []string{"signature", "attested", "hashes"} {
		if ok, _ := ch[k].(map[string]any)["ok"].(bool); !ok {
			t.Errorf("%s check failed: %v", k, ch[k])
		}
	}

	// The report is cached per model.
	e.must(200, "GET", "/api/verify/"+msgID.String(), nil)
	if calls != 1 {
		t.Fatalf("fetched the report %d times", calls)
	}

	// Unknown and malformed IDs are not found.
	e.must(404, "GET", "/api/verify/"+uuid.NewString(), nil)
	e.must(404, "GET", "/api/verify/nope", nil)
}

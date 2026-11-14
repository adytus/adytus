package server

import (
	"context"
	"errors"
	"testing"

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

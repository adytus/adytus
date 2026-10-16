package vault

import (
	"testing"

	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
)

type note struct{ Text string }

func TestOpenWithMasterKey(t *testing.T) {
	mk, _ := crypto.NewKey()
	o, dek, err := New(mk, nil, uuid.New(), store.KindChat, uuid.NullUUID{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Ciphertext, err = Seal(dek, o.ID, note{"secret"}); err != nil {
		t.Fatal(err)
	}

	var got note
	if err := Open(mk, o, &got); err != nil || got.Text != "secret" {
		t.Fatalf("got %+v, %v", got, err)
	}

	otherMK, _ := crypto.NewKey()
	if err := Open(otherMK, o, &got); err == nil {
		t.Fatal("another user's MK must not open the object")
	}
}

func TestOfflineAccessIsOptIn(t *testing.T) {
	mk, _ := crypto.NewKey()
	serverKey, _ := crypto.NewKey()

	private, _, _ := New(mk, nil, uuid.New(), store.KindFile, uuid.NullUUID{})
	if _, err := OfflineDEK(serverKey, private); err == nil {
		t.Fatal("server must not read objects that are not opted in")
	}

	optedIn, dek, _ := New(mk, serverKey, uuid.New(), store.KindFile, uuid.NullUUID{})
	got, err := OfflineDEK(serverKey, optedIn)
	if err != nil || string(got) != string(dek) {
		t.Fatalf("server should read opted-in object: %v", err)
	}

	// Opting in later yields an equivalent wrap.
	private.ServerWrappedDEK, err = WrapForServer(mk, serverKey, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OfflineDEK(serverKey, private); err != nil {
		t.Fatalf("opt-in after creation failed: %v", err)
	}
}

func TestSwappedCiphertextFails(t *testing.T) {
	mk, _ := crypto.NewKey()
	a, dekA, _ := New(mk, nil, uuid.New(), store.KindMessage, uuid.NullUUID{})
	b, _, _ := New(mk, nil, uuid.New(), store.KindMessage, uuid.NullUUID{})
	a.Ciphertext, _ = Seal(dekA, a.ID, note{"a"})

	// Move a's ciphertext and wrapped key onto b's row.
	b.Ciphertext, b.WrappedDEK = a.Ciphertext, a.WrappedDEK
	var got note
	if err := Open(mk, b, &got); err == nil {
		t.Fatal("ciphertext moved to another row must not decrypt")
	}
}

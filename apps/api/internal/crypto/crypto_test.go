package crypto

import (
	"bytes"
	"testing"
)

// Cheap params so tests stay fast.
var testKDF = KDFParams{Time: 1, MemoryKiB: 1024, Threads: 1}

func TestKeyHierarchyRoundTrip(t *testing.T) {
	salt, _ := RandomBytes(SaltSize)
	authKey, kek := DeriveKeys("correct horse", salt, testKDF)
	verifier := AuthVerifier(authKey)

	mk, _ := NewKey()
	wrappedMK, err := Wrap(kek, mk, []byte("user:1"))
	if err != nil {
		t.Fatal(err)
	}

	dek, _ := NewKey()
	wrappedDEK, _ := Wrap(mk, dek, []byte("object:42"))
	blob, _ := Seal(dek, []byte("hello"), []byte("object:42"))

	// Log in again with the same password.
	authKey2, kek2 := DeriveKeys("correct horse", salt, testKDF)
	if !VerifyAuth(authKey2, verifier) {
		t.Fatal("password should verify")
	}
	mk2, err := Unwrap(kek2, wrappedMK, []byte("user:1"))
	if err != nil {
		t.Fatal(err)
	}
	dek2, err := Unwrap(mk2, wrappedDEK, []byte("object:42"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(dek2, blob, []byte("object:42"))
	if err != nil || !bytes.Equal(pt, []byte("hello")) {
		t.Fatalf("got %q, %v", pt, err)
	}
}

func TestWrongPasswordFails(t *testing.T) {
	salt, _ := RandomBytes(SaltSize)
	authKey, kek := DeriveKeys("right", salt, testKDF)
	mk, _ := NewKey()
	wrapped, _ := Wrap(kek, mk, nil)

	badAuth, badKEK := DeriveKeys("wrong", salt, testKDF)
	if VerifyAuth(badAuth, AuthVerifier(authKey)) {
		t.Fatal("wrong password verified")
	}
	if _, err := Unwrap(badKEK, wrapped, nil); err != ErrDecrypt {
		t.Fatalf("expected ErrDecrypt, got %v", err)
	}
}

func TestAADMismatchFails(t *testing.T) {
	key, _ := NewKey()
	blob, _ := Seal(key, []byte("x"), []byte("object:1"))
	if _, err := Open(key, blob, []byte("object:2")); err != ErrDecrypt {
		t.Fatalf("expected ErrDecrypt, got %v", err)
	}
}

func TestAuthKeyAndKEKDiffer(t *testing.T) {
	salt, _ := RandomBytes(SaltSize)
	a, k := DeriveKeys("pw", salt, testKDF)
	if bytes.Equal(a, k) {
		t.Fatal("auth key and KEK must differ")
	}
}

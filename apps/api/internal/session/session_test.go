package session

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateGet(t *testing.T) {
	s := NewStore(time.Hour, time.Minute)
	user := uuid.New()
	mk := bytes.Repeat([]byte{7}, 32)

	tok, err := s.Create(user, mk)
	if err != nil {
		t.Fatal(err)
	}
	gotUser, gotMK, ok := s.Get(tok)
	if !ok || gotUser != user || !bytes.Equal(gotMK, mk) {
		t.Fatalf("get failed: %v %v", ok, gotUser)
	}

	if _, _, ok := s.Get("not-a-token"); ok {
		t.Fatal("garbage token accepted")
	}
	s.Delete(tok)
	if _, _, ok := s.Get(tok); ok {
		t.Fatal("deleted session still valid")
	}
}

func TestStoredStateDoesNotRevealMK(t *testing.T) {
	s := NewStore(time.Hour, time.Minute)
	mk := bytes.Repeat([]byte{9}, 32)
	if _, err := s.Create(uuid.New(), mk); err != nil {
		t.Fatal(err)
	}
	for _, e := range s.entries {
		if bytes.Contains(e.sealedMK, mk) {
			t.Fatal("MK stored in plaintext")
		}
	}
}

func TestExpiry(t *testing.T) {
	now := time.Now()
	s := NewStore(time.Hour, 10*time.Minute)
	s.now = func() time.Time { return now }
	tok, _ := s.Create(uuid.New(), make([]byte, 32))

	now = now.Add(9 * time.Minute)
	if _, _, ok := s.Get(tok); !ok {
		t.Fatal("active session expired early")
	}
	now = now.Add(11 * time.Minute)
	if _, _, ok := s.Get(tok); ok {
		t.Fatal("idle session should expire")
	}
}

func TestDeleteUserKeepsCurrent(t *testing.T) {
	s := NewStore(time.Hour, time.Hour)
	user := uuid.New()
	a, _ := s.Create(user, make([]byte, 32))
	b, _ := s.Create(user, make([]byte, 32))
	other, _ := s.Create(uuid.New(), make([]byte, 32))

	s.DeleteUser(user, a)
	if _, _, ok := s.Get(a); !ok {
		t.Fatal("kept session removed")
	}
	if _, _, ok := s.Get(b); ok {
		t.Fatal("other session of user not removed")
	}
	if _, _, ok := s.Get(other); !ok {
		t.Fatal("another user's session removed")
	}
}

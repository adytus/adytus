// Package session keeps logged-in sessions in memory. A session is what lets
// the server decrypt a user's objects: it holds the user's master key, sealed
// with a key derived from the session token. The token lives only in the
// browser's cookie, so the session table alone cannot recover any MK.
//
// Sessions are not persisted: a server restart logs everyone out, which is
// the intended trade-off.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
)

type entry struct {
	userID   uuid.UUID
	sealedMK []byte
	expires  time.Time
	lastSeen time.Time
}

type Store struct {
	// TTL is the absolute session lifetime; Idle logs out inactive sessions.
	TTL, Idle time.Duration

	mu      sync.Mutex
	entries map[[32]byte]*entry
	now     func() time.Time
}

func NewStore(ttl, idle time.Duration) *Store {
	return &Store{TTL: ttl, Idle: idle, entries: map[[32]byte]*entry{}, now: time.Now}
}

func decode(token string) ([]byte, [32]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, [32]byte{}, false
	}
	return raw, sha256.Sum256(raw), true
}

// Create starts a session holding mk and returns its token.
func (s *Store) Create(userID uuid.UUID, mk []byte) (string, error) {
	raw, err := crypto.RandomBytes(32)
	if err != nil {
		return "", err
	}
	key := crypto.SessionKey(raw)
	defer crypto.Zero(key)
	sealed, err := crypto.Seal(key, mk, userID[:])
	if err != nil {
		return "", err
	}

	now := s.now()
	s.mu.Lock()
	s.entries[sha256.Sum256(raw)] = &entry{userID: userID, sealedMK: sealed, expires: now.Add(s.TTL), lastSeen: now}
	s.mu.Unlock()
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Get returns the session's user and a fresh copy of the master key, which
// the caller should Zero when done.
func (s *Store) Get(token string) (uuid.UUID, []byte, bool) {
	raw, id, ok := decode(token)
	if !ok {
		return uuid.Nil, nil, false
	}

	now := s.now()
	s.mu.Lock()
	e, ok := s.entries[id]
	if ok && (now.After(e.expires) || now.Sub(e.lastSeen) > s.Idle) {
		delete(s.entries, id)
		ok = false
	}
	if ok {
		e.lastSeen = now
	}
	s.mu.Unlock()
	if !ok {
		return uuid.Nil, nil, false
	}

	key := crypto.SessionKey(raw)
	defer crypto.Zero(key)
	mk, err := crypto.Open(key, e.sealedMK, e.userID[:])
	if err != nil {
		return uuid.Nil, nil, false
	}
	return e.userID, mk, true
}

func (s *Store) Delete(token string) {
	if _, id, ok := decode(token); ok {
		s.mu.Lock()
		delete(s.entries, id)
		s.mu.Unlock()
	}
}

// DeleteUser ends all of a user's sessions except the one with keepToken
// (pass "" to end all of them).
func (s *Store) DeleteUser(userID uuid.UUID, keepToken string) {
	_, keep, _ := decode(keepToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries {
		if e.userID == userID && id != keep {
			delete(s.entries, id)
		}
	}
}

// Run removes expired sessions until ctx is done.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := s.now()
			s.mu.Lock()
			for id, e := range s.entries {
				if now.After(e.expires) || now.Sub(e.lastSeen) > s.Idle {
					delete(s.entries, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

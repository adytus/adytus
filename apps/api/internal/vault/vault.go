// Package vault encrypts and decrypts stored objects with the key hierarchy
// from the crypto package. Every object has its own DEK, wrapped with the
// user's MK and, if the object is opted into offline processing, also with the
// server key.
package vault

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
)

// Associated data binds each ciphertext to its object and purpose, so wrapped
// keys and payloads cannot be swapped between rows.
func aadContent(id uuid.UUID) []byte { return []byte("obj:" + id.String()) }
func aadDEK(id uuid.UUID) []byte     { return []byte("obj:" + id.String() + ":dek") }
func aadServer(id uuid.UUID) []byte  { return []byte("obj:" + id.String() + ":server") }
func aadPart(id uuid.UUID, part string) []byte {
	return []byte("obj:" + id.String() + ":part:" + part)
}
func AADUserMK(id uuid.UUID) []byte { return []byte("user:" + id.String() + ":mk") }
func AADRecovery(id uuid.UUID) []byte {
	return []byte("user:" + id.String() + ":recovery")
}

// New creates an object with a fresh DEK. The caller fills in Ciphertext (via
// Seal) or S3Key, then inserts it. serverKey is nil unless the object is
// opted into offline processing.
func New(mk, serverKey []byte, userID uuid.UUID, kind string, parent uuid.NullUUID) (*store.Object, []byte, error) {
	id := uuid.Must(uuid.NewV7())
	dek, err := crypto.NewKey()
	if err != nil {
		return nil, nil, err
	}
	wrapped, err := crypto.Wrap(mk, dek, aadDEK(id))
	if err != nil {
		return nil, nil, err
	}
	o := &store.Object{ID: id, UserID: userID, ParentID: parent, Kind: kind, WrappedDEK: wrapped}
	if serverKey != nil {
		if o.ServerWrappedDEK, err = crypto.Wrap(serverKey, dek, aadServer(id)); err != nil {
			return nil, nil, err
		}
	}
	return o, dek, nil
}

// DEK unwraps an object's key with the user's master key.
func DEK(mk []byte, o *store.Object) ([]byte, error) {
	return crypto.Unwrap(mk, o.WrappedDEK, aadDEK(o.ID))
}

// OfflineDEK unwraps an object's key with the server key. It fails for
// objects that are not opted into offline processing.
func OfflineDEK(serverKey []byte, o *store.Object) ([]byte, error) {
	if o.ServerWrappedDEK == nil {
		return nil, crypto.ErrDecrypt
	}
	return crypto.Unwrap(serverKey, o.ServerWrappedDEK, aadServer(o.ID))
}

// WrapForServer returns the DEK wrapped with the server key, to opt an
// object into offline processing.
func WrapForServer(mk, serverKey []byte, o *store.Object) ([]byte, error) {
	dek, err := DEK(mk, o)
	if err != nil {
		return nil, err
	}
	defer crypto.Zero(dek)
	return crypto.Wrap(serverKey, dek, aadServer(o.ID))
}

func SealBytes(dek []byte, id uuid.UUID, data []byte) ([]byte, error) {
	return crypto.Seal(dek, data, aadContent(id))
}

func OpenBytes(dek []byte, id uuid.UUID, sealed []byte) ([]byte, error) {
	return crypto.Open(dek, sealed, aadContent(id))
}

// SealPart and OpenPart encrypt a derived blob of an object (such as an
// image thumbnail) with the object's DEK, bound to the part's name so it
// can't be swapped with the object's content.
func SealPart(dek []byte, id uuid.UUID, part string, data []byte) ([]byte, error) {
	return crypto.Seal(dek, data, aadPart(id, part))
}

func OpenPart(dek []byte, id uuid.UUID, part string, sealed []byte) ([]byte, error) {
	return crypto.Open(dek, sealed, aadPart(id, part))
}

// Seal JSON-encodes v and encrypts it with the object's DEK.
func Seal(dek []byte, id uuid.UUID, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return SealBytes(dek, id, raw)
}

// Open decrypts an object's inline ciphertext into v using the master key.
func Open(mk []byte, o *store.Object, v any) error {
	dek, err := DEK(mk, o)
	if err != nil {
		return err
	}
	defer crypto.Zero(dek)
	return OpenWithDEK(dek, o, v)
}

func OpenWithDEK(dek []byte, o *store.Object, v any) error {
	raw, err := OpenBytes(dek, o.ID, o.Ciphertext)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

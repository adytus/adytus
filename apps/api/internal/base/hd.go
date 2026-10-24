// Package base talks to the Base chain (an Ethereum L2): deposit address
// derivation from an extended public key, and a small JSON-RPC client.
package base

import (
	"errors"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"golang.org/x/crypto/sha3"
)

// Deriver turns an extended public key into deposit addresses. It holds no
// private key, so a compromised server cannot move deposited funds.
type Deriver struct {
	key *hdkeychain.ExtendedKey
}

// NewDeriver parses an xpub. A private extended key is refused on purpose.
func NewDeriver(xpub string) (*Deriver, error) {
	k, err := hdkeychain.NewKeyFromString(strings.TrimSpace(xpub))
	if err != nil {
		return nil, fmt.Errorf("parse xpub: %w", err)
	}
	if k.IsPrivate() {
		return nil, errors.New("BASE_XPUB must be an extended public key, not a private one")
	}
	return &Deriver{key: k}, nil
}

// Address returns the EIP-55 address at the non-hardened child index.
func (d *Deriver) Address(index uint32) (string, error) {
	child, err := d.key.Derive(index)
	if err != nil {
		return "", err
	}
	pub, err := child.ECPubKey()
	if err != nil {
		return "", err
	}
	raw := pub.SerializeUncompressed() // 0x04 || X || Y
	h := sha3.NewLegacyKeccak256()
	h.Write(raw[1:])
	return Checksum(h.Sum(nil)[12:]), nil
}

// Checksum formats 20 address bytes as an EIP-55 mixed-case hex string.
func Checksum(addr []byte) string {
	lower := fmt.Sprintf("%x", addr)
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(lower))
	sum := h.Sum(nil)
	out := []byte(lower)
	for i, c := range out {
		if c < 'a' || c > 'f' {
			continue
		}
		nibble := sum[i/2]
		if i%2 == 0 {
			nibble >>= 4
		} else {
			nibble &= 0x0f
		}
		if nibble >= 8 {
			out[i] = c - 32
		}
	}
	return "0x" + string(out)
}

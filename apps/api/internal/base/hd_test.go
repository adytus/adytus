package base

import (
	"crypto/sha512"
	"testing"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"golang.org/x/crypto/pbkdf2"
)

const hardhatMnemonic = "test test test test test test test test test test test junk"

// accountXpub derives m/44'/60'/0'/0 from the well-known hardhat mnemonic
// and returns its extended public key.
func accountXpub(t *testing.T) string {
	t.Helper()
	seed := pbkdf2.Key([]byte(hardhatMnemonic), []byte("mnemonic"), 2048, 64, sha512.New)
	k, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []uint32{
		hdkeychain.HardenedKeyStart + 44, hdkeychain.HardenedKeyStart + 60,
		hdkeychain.HardenedKeyStart, 0,
	} {
		if k, err = k.Derive(i); err != nil {
			t.Fatal(err)
		}
	}
	pub, err := k.Neuter()
	if err != nil {
		t.Fatal(err)
	}
	return pub.String()
}

func TestDeriverMatchesKnownAddresses(t *testing.T) {
	d, err := NewDeriver(accountXpub(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		"0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
		"0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
	}
	for i, w := range want {
		got, err := d.Address(uint32(i))
		if err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("index %d: got %s, want %s", i, got, w)
		}
	}
}

func TestDeriverRefusesPrivateKey(t *testing.T) {
	seed := pbkdf2.Key([]byte(hardhatMnemonic), []byte("mnemonic"), 2048, 64, sha512.New)
	k, _ := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if _, err := NewDeriver(k.String()); err == nil {
		t.Fatal("accepted an xprv")
	}
}

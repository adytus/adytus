// Package attest checks that an answer came out of NEAR AI's TEE.
//
// This file is the first link of the chain: the answer to the key. NEAR AI
// signs every chat completion it serves from a TEE. The signed text is
// "<sha256 of the request body>:<sha256 of the response body>", and the
// signature is an Ethereum personal_sign (EIP-191, secp256k1) by a key that
// lives inside the enclave. Recovering the signer and comparing the hashes to
// our own copy of the bytes shows the answer is the one that key signed.
// Whether that key really lives in a genuine enclave is the attestation
// report's job, checked separately.
package attest

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Signature is what NEAR AI returns for a completion.
type Signature struct {
	Text           string `json:"text"`
	Signature      string `json:"signature"`
	SigningAddress string `json:"signing_address"`
	SigningAlgo    string `json:"signing_algo"`
}

// Status of a signature check.
const (
	Verified    = "verified"
	Mismatch    = "mismatch"
	Unavailable = "unavailable"
)

// Result of checking one completion's signature.
type Result struct {
	Status string
	// Detail says what didn't match, or why there was nothing to check.
	Detail string
	// Signer is the address recovered from the signature, when it parsed.
	Signer string
}

// Verify checks sig against the hashes of the bytes we sent and received.
// Hashes are lower-case hex.
func Verify(requestSHA256, responseSHA256 string, sig Signature) Result {
	if sig.SigningAlgo != "" && !strings.EqualFold(sig.SigningAlgo, "ecdsa") {
		return Result{Status: Unavailable, Detail: fmt.Sprintf("signing algorithm %q isn't checked", sig.SigningAlgo)}
	}
	signer, err := RecoverAddress(sig.Text, sig.Signature)
	if err != nil {
		return Result{Status: Mismatch, Detail: "the signature doesn't parse: " + err.Error()}
	}
	r := Result{Status: Mismatch, Signer: signer}
	if !strings.EqualFold(signer, sig.SigningAddress) {
		r.Detail = fmt.Sprintf("signed by %s, not the stated %s", signer, sig.SigningAddress)
		return r
	}
	req, resp, ok := strings.Cut(strings.TrimSpace(sig.Text), ":")
	if !ok {
		r.Detail = "the signed text isn't request_hash:response_hash"
		return r
	}
	switch {
	case !strings.EqualFold(req, requestSHA256):
		r.Detail = "the signed request hash isn't the hash of the request we sent"
	case !strings.EqualFold(resp, responseSHA256):
		r.Detail = "the signed response hash isn't the hash of the response we received"
	default:
		r.Status = Verified
	}
	return r
}

// RecoverAddress returns the checksummed Ethereum address that signed text
// with personal_sign. sigHex is 65 bytes, r || s || v, with v 0/1 or 27/28.
func RecoverAddress(text, sigHex string) (string, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil {
		return "", err
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("%d bytes, want 65", len(sig))
	}
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return "", errors.New("bad recovery id")
	}
	// btcec wants v || r || s, with v = 27 + id for an uncompressed key.
	compact := make([]byte, 65)
	compact[0] = 27 + v
	copy(compact[1:], sig[:64])
	pub, _, err := ecdsa.RecoverCompact(compact, personalHash(text))
	if err != nil {
		return "", err
	}
	return checksum(keccak(pub.SerializeUncompressed()[1:])[12:]), nil
}

// personalHash is EIP-191's hash of a signed message.
func personalHash(text string) []byte {
	return keccak([]byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len(text)) + text))
}

func keccak(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

// checksum is EIP-55 mixed-case hex.
func checksum(addr []byte) string {
	lower := hex.EncodeToString(addr)
	hash := hex.EncodeToString(keccak([]byte(lower)))
	out := []byte("0x" + lower)
	for i := range lower {
		if lower[i] >= 'a' && hash[i] >= '8' {
			out[i+2] = lower[i] - 32
		}
	}
	return string(out)
}

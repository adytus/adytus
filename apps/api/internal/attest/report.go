package attest

// This file is the second link of the chain: the key to the enclave. NEAR AI
// publishes an attestation report per model. Each entry is an Intel TDX quote
// whose report_data commits to the enclave's signing address and to a nonce we
// chose, so the address in a receipt is tied to a quote produced fresh for us.
//
// What is checked here: the signer appears in the report, and that entry's
// quote binds the signer and our nonce. What is not: the quote's Intel
// certificate chain and the GPU attestation. Those are what NEAR AI's own
// verifier (github.com/nearai/nearai-cloud-verifier) adds on top.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Report is the part of NEAR AI's attestation report checked here.
type Report struct {
	Gateway *Attestation  `json:"gateway_attestation"`
	Models  []Attestation `json:"model_attestations"`
}

// Attestation is one enclave's entry in a report.
type Attestation struct {
	ModelName      string `json:"model_name"`
	SigningAddress string `json:"signing_address"`
	RequestNonce   string `json:"request_nonce"`
	IntelQuote     string `json:"intel_quote"`
}

// Attested is the result of looking a signer up in a report.
type Attested struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Nonce is the one the report was fetched with.
	Nonce string `json:"nonce"`
}

// NewNonce returns 32 random bytes as hex, for a report request.
func NewNonce() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Report fetches the attestation report for model with nonce (64 hex chars).
// It needs no API key.
func (n *NearAI) Report(ctx context.Context, model, nonce string) (*Report, error) {
	u := fmt.Sprintf("%s/attestation/report?model=%s&signing_algo=ecdsa&nonce=%s",
		n.BaseURL, url.QueryEscape(model), url.QueryEscape(nonce))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := n.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attestation report: http %d", res.StatusCode)
	}
	var r Report
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("attestation report: %w", err)
	}
	return &r, nil
}

// TDX quote v4: a 48-byte header, then the TD report, whose last 64 bytes
// (offset 520) are report_data.
const (
	reportDataAt  = 48 + 520
	reportDataEnd = reportDataAt + 64
)

// CheckSigner reports whether signer is attested in r for nonce: an entry
// with that signing address whose quote's report_data is the address
// (padded to 32 bytes) followed by the nonce.
func CheckSigner(r *Report, signer, nonce string) Attested {
	out := Attested{Nonce: nonce}
	if signer == "" {
		out.Detail = "the receipt has no signer to look up"
		return out
	}
	addr, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(signer), "0x"))
	if err != nil || len(addr) != 20 {
		out.Detail = "the signer isn't an address"
		return out
	}
	want, err := hex.DecodeString(nonce)
	if err != nil || len(want) != 32 {
		out.Detail = "the nonce isn't 32 bytes of hex"
		return out
	}
	entries := r.Models
	if r.Gateway != nil {
		entries = append([]Attestation{*r.Gateway}, entries...)
	}
	found := false
	for _, a := range entries {
		if !strings.EqualFold(a.SigningAddress, signer) {
			continue
		}
		found = true
		q, err := hex.DecodeString(a.IntelQuote)
		if err != nil || len(q) < reportDataEnd {
			out.Detail = "the signer's quote doesn't parse"
			continue
		}
		rd := q[reportDataAt:reportDataEnd]
		switch {
		case !bytes.Equal(rd[:20], addr) || !bytes.Equal(rd[20:32], make([]byte, 12)):
			out.Detail = "the signer's quote commits to a different address"
		case !bytes.Equal(rd[32:], want):
			out.Detail = "the signer's quote wasn't made for our nonce"
		default:
			out.OK = true
			out.Detail = "the signer is in NEAR AI's attestation report, bound to a TDX quote made for our nonce"
			return out
		}
	}
	if !found {
		out.Detail = "the signer isn't in NEAR AI's current attestation report (the enclave may have rotated its key since)"
	}
	return out
}

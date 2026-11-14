package attest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// testdata/report.json is a real NEAR AI report for Qwen/Qwen3.8-27B, cut down
// to the fields checked here, fetched with this nonce.
const (
	reportNonce  = "e44b87677496449f32923f41b5780f5b6fa20ed63359cb1b65584174fbc7455b"
	reportSigner = "0x835ab53d20294ef795906dff21ae4c045336aa26"
)

func loadReport(t *testing.T) *Report {
	t.Helper()
	b, err := os.ReadFile("testdata/report.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestCheckSigner(t *testing.T) {
	r := loadReport(t)
	if a := CheckSigner(r, reportSigner, reportNonce); !a.OK {
		t.Fatalf("model signer: %+v", a)
	}
	// Case doesn't matter (receipts carry the EIP-55 form).
	if a := CheckSigner(r, "0x"+strings.ToUpper(reportSigner[2:]), reportNonce); !a.OK {
		t.Fatalf("upper-case signer: %+v", a)
	}
	// The gateway's key is attested too.
	if a := CheckSigner(r, r.Gateway.SigningAddress, reportNonce); !a.OK {
		t.Fatalf("gateway signer: %+v", a)
	}

	other := "0x" + strings.Repeat("ab", 32)
	cases := map[string]struct{ signer, nonce string }{
		"unknown signer": {"0x" + strings.Repeat("11", 20), reportNonce},
		"stale nonce":    {reportSigner, other[2:]},
		"no signer":      {"", reportNonce},
		"bad nonce":      {reportSigner, "zz"},
	}
	for name, c := range cases {
		if a := CheckSigner(r, c.signer, c.nonce); a.OK || a.Detail == "" {
			t.Errorf("%s: %+v", name, a)
		}
	}

	// A quote that commits to another address doesn't count, even when the
	// entry names our signer.
	forged := *r
	forged.Gateway = nil
	forged.Models = []Attestation{{SigningAddress: "0x" + strings.Repeat("11", 20), IntelQuote: r.Models[0].IntelQuote}}
	if a := CheckSigner(&forged, "0x"+strings.Repeat("11", 20), reportNonce); a.OK {
		t.Fatalf("accepted a quote for another key: %+v", a)
	}
}

func TestReportFetch(t *testing.T) {
	raw, _ := os.ReadFile("testdata/report.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/attestation/report" || q.Get("model") != "Qwen/Qwen3.8-27B" || q.Get("nonce") != reportNonce || q.Get("signing_algo") != "ecdsa" {
			http.Error(w, "bad request", 400)
			return
		}
		w.Write(raw)
	}))
	defer srv.Close()
	r, err := NewNearAI(srv.URL+"/v1", "").Report(context.Background(), "Qwen/Qwen3.8-27B", reportNonce)
	if err != nil {
		t.Fatal(err)
	}
	if a := CheckSigner(r, reportSigner, reportNonce); !a.OK {
		t.Fatalf("%+v", a)
	}
	if len(NewNonce()) != 64 || NewNonce() == NewNonce() {
		t.Fatal("nonces should be fresh 32-byte hex")
	}
}

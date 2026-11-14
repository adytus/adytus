// Command verify checks a reply's inference receipts without trusting the
// adytus server: it takes the hashes and signatures from the public proof
// endpoint, recovers the signer itself, and fetches NEAR AI's attestation
// report directly with its own nonce.
//
//	go run ./cmd/verify https://api.example.com <message-id>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/reaganiwadha/adytus/apps/api/internal/attest"
)

type receipt struct {
	Seq            int    `json:"seq"`
	Model          string `json:"model"`
	CompletionID   string `json:"completion_id"`
	RequestSHA256  string `json:"request_sha256"`
	ResponseSHA256 string `json:"response_sha256"`
	SignedText     string `json:"signed_text"`
	Signature      string `json:"signature"`
	SigningAddress string `json:"signing_address"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: verify <api-base> <message-id>")
		os.Exit(2)
	}
	base, id := strings.TrimRight(os.Args[1], "/"), os.Args[2]
	nearBase := os.Getenv("NEARAI_BASE_URL")
	if nearBase == "" {
		nearBase = "https://cloud-api.near.ai/v1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rs, err := fetch(ctx, base+"/api/verify/"+id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch receipts:", err)
		os.Exit(2)
	}
	near := attest.NewNearAI(nearBase, "")
	near.HTTP.Timeout = time.Minute
	reports := map[string]attest.Attested{}
	pass := true
	for _, r := range rs {
		fmt.Printf("call %d  %s  %s\n", r.Seq, r.Model, r.CompletionID)

		res := attest.Verify(r.RequestSHA256, r.ResponseSHA256, attest.Signature{
			Text: r.SignedText, Signature: r.Signature, SigningAddress: r.SigningAddress, SigningAlgo: "ecdsa",
		})
		ok := res.Status == attest.Verified
		pass = pass && ok
		line("signature valid, hashes match", ok, res.Detail)

		a, seen := reports[r.Model+"|"+r.SigningAddress]
		if !seen {
			nonce := attest.NewNonce()
			if rep, err := near.Report(ctx, r.Model, nonce); err != nil {
				a = attest.Attested{Nonce: nonce, Detail: err.Error()}
			} else {
				a = attest.CheckSigner(rep, r.SigningAddress, nonce)
			}
			reports[r.Model+"|"+r.SigningAddress] = a
		}
		pass = pass && a.OK
		line("signer is an attested TEE key", a.OK, a.Detail+" (nonce "+a.Nonce[:16]+"…)")
		fmt.Println("  signer", r.SigningAddress)
	}
	if pass {
		fmt.Println("PASS")
		return
	}
	fmt.Println("FAIL")
	os.Exit(1)
}

func line(name string, ok bool, detail string) {
	mark := "✓"
	if !ok {
		mark = "✗"
	}
	if detail != "" {
		fmt.Printf("  %s %s: %s\n", mark, name, detail)
	} else {
		fmt.Printf("  %s %s\n", mark, name)
	}
}

func fetch(ctx context.Context, u string) ([]receipt, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", res.StatusCode)
	}
	var out struct {
		Receipts []receipt `json:"receipts"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Receipts) == 0 {
		return nil, fmt.Errorf("no receipts")
	}
	return out.Receipts, nil
}

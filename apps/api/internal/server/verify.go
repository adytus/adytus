package server

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/reaganiwadha/adytus/apps/api/internal/attest"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

// The public proof page: anyone with a reply's ID can check that NEAR AI's
// enclave signed it. It returns hashes, signatures and the signer, never the
// chat: the hashes are over the full request (system prompt and history
// included), so they reveal nothing that could be guessed back.

// AttestationSource fetches NEAR AI's attestation report for a model.
type AttestationSource interface {
	Report(ctx context.Context, model, nonce string) (*attest.Report, error)
}

const (
	verifyPerMinute = 60
	reportTTL       = 10 * time.Minute
)

type cachedReport struct {
	report  *attest.Report
	nonce   string
	fetched time.Time
	err     string
}

type reportCache struct {
	mu sync.Mutex
	by map[string]*cachedReport
}

func (s *Server) attestations() AttestationSource {
	if s.Attestations != nil {
		return s.Attestations()
	}
	base := "https://cloud-api.near.ai/v1"
	if s.LLM == nil {
		return attest.NewNearAI(base, "")
	}
	if snap := s.LLM.Current(); snap != nil {
		if p, ok := snap.Provider(llm.NearAI); ok && p.BaseURL != "" {
			base = p.BaseURL
		}
	}
	return attest.NewNearAI(base, "")
}

// report returns the model's attestation report, fetched with a fresh nonce
// at most every reportTTL. A failed fetch is cached for a minute.
func (s *Server) report(ctx context.Context, model string) *cachedReport {
	s.reports.mu.Lock()
	defer s.reports.mu.Unlock()
	if s.reports.by == nil {
		s.reports.by = map[string]*cachedReport{}
	}
	if c, ok := s.reports.by[model]; ok {
		ttl := reportTTL
		if c.err != "" {
			ttl = time.Minute
		}
		if time.Since(c.fetched) < ttl {
			return c
		}
	}
	nonce := attest.NewNonce()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := &cachedReport{nonce: nonce, fetched: time.Now()}
	if r, err := s.attestations().Report(ctx, model, nonce); err != nil {
		c.err = "NEAR AI's attestation report couldn't be fetched: " + err.Error()
	} else {
		c.report = r
	}
	s.reports.by[model] = c
	return c
}

type verifyCheck struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type verifyReceipt struct {
	Seq            int       `json:"seq"`
	Model          string    `json:"model"`
	CompletionID   string    `json:"completion_id"`
	RequestSHA256  string    `json:"request_sha256"`
	ResponseSHA256 string    `json:"response_sha256"`
	SignedText     string    `json:"signed_text"`
	Signature      string    `json:"signature"`
	SigningAddress string    `json:"signing_address"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	Checks         struct {
		Signature   verifyCheck `json:"signature"`
		Attested    verifyCheck `json:"attested"`
		Hashes      verifyCheck `json:"hashes"`
		ReportNonce string      `json:"report_nonce,omitempty"`
		ReportAt    *time.Time  `json:"report_fetched_at,omitempty"`
	} `json:"checks"`
}

// verifyMessage is GET /api/verify/:id, with no session.
func (s *Server) verifyMessage(c fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return err
	}
	rows, err := s.Store.DB.Query(c.Context(), `
		SELECT seq, model, completion_id, request_sha256, response_sha256,
			signed_text, signature, signing_address, status, created_at
		FROM inference_receipts WHERE message_id = $1 ORDER BY seq`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []verifyReceipt{}
	for rows.Next() {
		var r verifyReceipt
		if err := rows.Scan(&r.Seq, &r.Model, &r.CompletionID, &r.RequestSHA256, &r.ResponseSHA256,
			&r.SignedText, &r.Signature, &r.SigningAddress, &r.Status, &r.CreatedAt); err != nil {
			return err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(out) == 0 {
		return errs.NotFound.New("no receipts for this message")
	}
	for i := range out {
		s.check(c.Context(), &out[i])
	}
	return c.JSON(fiber.Map{"message_id": id, "receipts": out})
}

// check re-runs the three checks a visitor sees, from the stored receipt.
func (s *Server) check(ctx context.Context, r *verifyReceipt) {
	ch := &r.Checks
	if r.Signature == "" {
		ch.Signature.Detail = "NEAR AI returned no signature for this call"
	} else if signer, err := attest.RecoverAddress(r.SignedText, r.Signature); err != nil {
		ch.Signature.Detail = "the signature doesn't parse: " + err.Error()
	} else if !strings.EqualFold(signer, r.SigningAddress) {
		ch.Signature.Detail = "signed by " + signer + ", not the stated " + r.SigningAddress
	} else {
		ch.Signature = verifyCheck{OK: true, Detail: "recovered signer " + signer + " matches"}
	}

	if r.SignedText == "" {
		ch.Hashes.Detail = "nothing signed to compare"
	} else if strings.EqualFold(strings.TrimSpace(r.SignedText), r.RequestSHA256+":"+r.ResponseSHA256) {
		ch.Hashes = verifyCheck{OK: true, Detail: "the signed text is the SHA-256 of the exact request and response bytes"}
	} else {
		ch.Hashes.Detail = "the signed hashes aren't the hashes of the bytes the server sent and received"
	}

	if r.SigningAddress == "" {
		ch.Attested.Detail = "no signer to look up"
		return
	}
	rep := s.report(ctx, r.Model)
	ch.ReportNonce = rep.nonce
	at := rep.fetched
	ch.ReportAt = &at
	if rep.report == nil {
		ch.Attested.Detail = rep.err
		return
	}
	a := attest.CheckSigner(rep.report, r.SigningAddress, rep.nonce)
	ch.Attested = verifyCheck{OK: a.OK, Detail: a.Detail}
}

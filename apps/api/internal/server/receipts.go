package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/reaganiwadha/adytus/apps/api/internal/attest"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

// SignatureSource fetches a provider's signature for a completion.
type SignatureSource interface {
	SignatureWithRetry(ctx context.Context, completionID, model string) (attest.Signature, error)
}

// receiptTimeout bounds fetching every signature behind one reply.
const receiptTimeout = time.Minute

func (s *Server) signatures(p llm.Provider) SignatureSource {
	if s.Signatures != nil {
		return s.Signatures(p)
	}
	return attest.NewNearAI(p.BaseURL, p.APIKey)
}

// recordReceipts fetches and checks the signature of each call behind a
// reply, then stores the result. It runs after the reply is saved, so a slow
// signature never holds up the answer.
func (s *Server) recordReceipts(messageID, userID uuid.UUID, p llm.Provider, calls []llm.Call) {
	if len(calls) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), receiptTimeout)
	defer cancel()
	src := s.signatures(p)
	for i, c := range calls {
		var sig attest.Signature
		res := attest.Result{Status: attest.Unavailable}
		switch {
		case !c.Complete:
			res.Detail = "the response didn't finish, so there's nothing signed to check"
		default:
			var err error
			if sig, err = src.SignatureWithRetry(ctx, c.CompletionID, c.Model); err != nil {
				res.Detail = "NEAR AI didn't return a signature: " + err.Error()
			} else {
				res = attest.Verify(c.RequestSHA256, c.ResponseSHA256, sig)
			}
		}
		if res.Status != attest.Verified {
			slog.Warn("inference receipt not verified", "message", messageID, "seq", i, "status", res.Status, "detail", res.Detail)
		}
		if _, err := s.Store.DB.Exec(ctx, `
			INSERT INTO inference_receipts (message_id, user_id, seq, provider, model, completion_id,
				request_sha256, response_sha256, signed_text, signature, signing_address, signing_algo, status, detail)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (message_id, seq) DO NOTHING`,
			messageID, userID, i, p.ID, c.Model, c.CompletionID, c.RequestSHA256, c.ResponseSHA256,
			sig.Text, sig.Signature, sig.SigningAddress, sig.SigningAlgo, res.Status, res.Detail); err != nil {
			slog.Error("store inference receipt", "err", err, "message", messageID)
			return
		}
	}
}

type receiptView struct {
	MessageID      uuid.UUID `json:"message_id"`
	Seq            int       `json:"seq"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	CompletionID   string    `json:"completion_id"`
	RequestSHA256  string    `json:"request_sha256"`
	ResponseSHA256 string    `json:"response_sha256"`
	SignedText     string    `json:"signed_text"`
	Signature      string    `json:"signature"`
	SigningAddress string    `json:"signing_address"`
	Status         string    `json:"status"`
	Detail         string    `json:"detail"`
	CreatedAt      time.Time `json:"created_at"`
}

// chatReceipts lists the signature checks behind a chat's replies.
func (s *Server) chatReceipts(c fiber.Ctx) error {
	chatID, err := paramID(c)
	if err != nil {
		return err
	}
	rows, err := s.Store.DB.Query(c.Context(), `
		SELECT r.message_id, r.seq, r.provider, r.model, r.completion_id, r.request_sha256, r.response_sha256,
			r.signed_text, r.signature, r.signing_address, r.status, r.detail, r.created_at
		FROM inference_receipts r
		JOIN objects m ON m.id = r.message_id
		WHERE m.parent_id = $1 AND m.user_id = $2 AND r.user_id = $2
		ORDER BY m.created_at, r.seq`, chatID, mustAuth(c).UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []receiptView{}
	for rows.Next() {
		var r receiptView
		if err := rows.Scan(&r.MessageID, &r.Seq, &r.Provider, &r.Model, &r.CompletionID, &r.RequestSHA256,
			&r.ResponseSHA256, &r.SignedText, &r.Signature, &r.SigningAddress, &r.Status, &r.Detail, &r.CreatedAt); err != nil {
			return err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"receipts": out})
}

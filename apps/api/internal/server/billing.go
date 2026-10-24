package server

import (
	"github.com/gofiber/fiber/v3"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
)

func (s *Server) balance(c fiber.Ctx) error {
	b, err := s.Ledger.Balance(c.Context(), mustAuth(c).UserID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"balance": b})
}

func (s *Server) ledgerHistory(c fiber.Ctx) error {
	entries, err := s.Ledger.History(c.Context(), mustAuth(c).UserID, 100)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []billing.Entry{}
	}
	return c.JSON(fiber.Map{"entries": entries})
}

func (s *Server) subscribe(c fiber.Ctx) error {
	var req struct {
		PlanID string `json:"plan_id" validate:"required"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	plan, ok := billing.PlanByID(req.PlanID)
	if !ok {
		return errs.Validation.New("unknown plan %q", req.PlanID)
	}
	a := mustAuth(c)
	ref, err := s.Payments.Charge(c.Context(), a.UserID, plan)
	if err != nil {
		return errs.Upstream.Wrap(err, "payment failed")
	}
	if err := s.Ledger.Subscribe(c.Context(), a.UserID, plan, ref); err != nil {
		return err
	}
	b, err := s.Ledger.Balance(c.Context(), a.UserID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"balance": b, "payment_ref": ref})
}

func (s *Server) cryptoInfo(c fiber.Ctx) error {
	if s.Deposits == nil {
		return c.JSON(billing.DepositInfo{Deposits: []billing.DepositEntry{}})
	}
	info, err := s.Deposits.Info(c.Context(), mustAuth(c).UserID)
	if err != nil {
		return err
	}
	return c.JSON(info)
}

func (s *Server) cryptoQuote(c fiber.Ctx) error {
	if s.Deposits == nil {
		return errs.NotFound.New("crypto payments are not enabled")
	}
	var req struct {
		USDMicros int64 `json:"usd_micros" validate:"required,gt=0"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	q, err := s.Deposits.Quote(c.Context(), mustAuth(c).UserID, req.USDMicros)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"quote": q})
}

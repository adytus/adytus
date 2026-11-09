package server

import (
	"github.com/gofiber/fiber/v3"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
)

// voucherPerMinute limits code attempts per user, so codes can't be guessed.
const voucherPerMinute = 10

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

func (s *Server) claimVoucher(c fiber.Ctx) error {
	if s.Deposits == nil {
		return errs.NotFound.New("crypto payments are not enabled")
	}
	var req struct {
		Code string `json:"code" validate:"required,max=64"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	v, err := s.Deposits.ClaimVoucher(c.Context(), mustAuth(c).UserID, req.Code)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"voucher": v})
}

func (s *Server) removeVoucher(c fiber.Ctx) error {
	if s.Deposits == nil {
		return errs.NotFound.New("crypto payments are not enabled")
	}
	if err := s.Deposits.RemoveVoucher(c.Context(), mustAuth(c).UserID); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"voucher": nil})
}

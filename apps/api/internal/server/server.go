package server

import (
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/config"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
)

type Server struct {
	App      *fiber.App
	cfg      *config.Config
	db       *pgxpool.Pool
	llm      llm.Provider
	models   llm.Whitelist
	payments billing.Gateway
}

type structValidator struct{ v *validator.Validate }

func (s structValidator) Validate(out any) error { return s.v.Struct(out) }

func New(cfg *config.Config, db *pgxpool.Pool, provider llm.Provider, payments billing.Gateway) *Server {
	s := &Server{
		App: fiber.New(fiber.Config{
			AppName:         "adytus",
			ErrorHandler:    errs.Handler,
			StructValidator: structValidator{validator.New(validator.WithRequiredStructEnabled())},
		}),
		cfg:      cfg,
		db:       db,
		llm:      provider,
		models:   llm.Whitelist(cfg.AllowedModels),
		payments: payments,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.App.Get("/healthz", s.health)

	api := s.App.Group("/api")
	api.Get("/models", s.listModels)
	api.Get("/plans", s.listPlans)
}

func (s *Server) health(c fiber.Ctx) error {
	if err := s.db.Ping(c.Context()); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "db down"})
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

func (s *Server) listModels(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"models": s.models})
}

func (s *Server) listPlans(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"plans": billing.Plans})
}

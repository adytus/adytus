package server

import (
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/blob"
	"github.com/reaganiwadha/adytus/apps/api/internal/config"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/session"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
)

type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Ledger   *billing.Ledger
	Catalog  *llm.Catalog
	LLM      llm.Provider
	Payments billing.Gateway
	Sessions *session.Store
	Blobs    blob.Store
}

type Server struct {
	Deps
	App *fiber.App
	// kdfSlots bounds concurrent Argon2id runs (64 MiB each).
	kdfSlots chan struct{}
}

type structValidator struct{ v *validator.Validate }

func (s structValidator) Validate(out any) error { return s.v.Struct(out) }

func New(d Deps) *Server {
	s := &Server{
		Deps: d,
		App: fiber.New(fiber.Config{
			AppName:         "adytus",
			ErrorHandler:    errs.Handler,
			StructValidator: structValidator{validator.New(validator.WithRequiredStructEnabled())},
			BodyLimit:       d.Config.MaxUploadBytes + 1<<20,
		}),
		kdfSlots: make(chan struct{}, 4),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.App.Use(recover.New())
	s.App.Get("/healthz", s.health)

	api := s.App.Group("/api")
	api.Get("/models", s.listModels)
	api.Get("/plans", s.listPlans)

	auth := api.Group("/auth", limiter.New(limiter.Config{
		Max:        s.Config.AuthPerMinute,
		Expiration: time.Minute,
		LimitReached: func(c fiber.Ctx) error {
			return errs.RateLimited.New("too many attempts; wait a minute")
		},
	}))
	auth.Post("/signup", s.signup)
	auth.Post("/login", s.login)
	auth.Post("/recover", s.recover)
	auth.Post("/logout", s.logout)

	me := api.Group("", s.requireSession)
	me.Get("/me", s.me)
	me.Post("/auth/password", s.changePassword)

	me.Get("/billing/balance", s.balance)
	me.Get("/billing/ledger", s.ledgerHistory)
	me.Post("/billing/subscribe", s.subscribe)

	me.Get("/chats", s.listChats)
	me.Post("/chats", s.createChat)
	me.Get("/chats/:id", s.getChat)
	me.Patch("/chats/:id", s.updateChat)
	me.Delete("/chats/:id", s.deleteChat)
	me.Put("/chats/:id/offline", s.setChatOffline)
	me.Post("/chats/:id/messages", limiter.New(limiter.Config{
		Max:          s.Config.ChatPerMinute,
		Expiration:   time.Minute,
		KeyGenerator: func(c fiber.Ctx) string { return mustAuth(c).UserID.String() },
		LimitReached: func(c fiber.Ctx) error {
			return errs.RateLimited.New("too many messages; slow down")
		},
	}), s.sendMessage)

	me.Get("/files", s.listFiles)
	me.Post("/files", s.uploadFile)
	me.Get("/files/:id/content", s.downloadFile)
	me.Delete("/files/:id", s.deleteFile)
	me.Put("/files/:id/offline", s.setFileOffline)
}

func (s *Server) health(c fiber.Ctx) error {
	if err := s.Store.DB.Ping(c.Context()); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "db down"})
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

func (s *Server) listModels(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"models": s.Catalog.Models, "default": s.Catalog.Default().ID})
}

func (s *Server) listPlans(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"plans": billing.Plans})
}

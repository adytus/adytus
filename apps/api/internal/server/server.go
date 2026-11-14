package server

import (
	"context"
	"encoding/json"
	"sync"
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
	"github.com/reaganiwadha/adytus/apps/api/internal/tools"
)

type Deps struct {
	Config *config.Config
	Store  *store.Store
	Ledger *billing.Ledger
	// LLM is the live model catalog and providers; see SeedLLM and ReloadLLM.
	LLM *llm.Registry
	// Deposits takes USDC and ETH on Base; nil when crypto is not configured.
	Deposits *billing.Deposits
	// Near takes NEAR and USDC on NEAR, set in the backoffice. Vouchers
	// discount top-ups on either chain. New fills both in from Store when unset.
	Near     *billing.Near
	Vouchers *billing.Vouchers
	// Signatures fetches signed receipts for a provider's completions;
	// nil uses NEAR AI's API. Tests swap in a fake.
	Signatures func(llm.Provider) SignatureSource
	// Attestations fetches NEAR AI's attestation reports for the public
	// proof page; nil uses NEAR AI's API. Tests swap in a fake.
	Attestations func() AttestationSource
	Sessions   *session.Store
	Blobs      blob.Store
	Tools      *tools.Registry
}

type Server struct {
	Deps
	App *fiber.App
	// kdfSlots bounds concurrent Argon2id runs (64 MiB each).
	kdfSlots chan struct{}
	// llmMu orders backoffice changes, so swaps land in commit order.
	llmMu sync.Mutex
	// providerMu guards the cached Ollama reachability check.
	// nearUsed remembers NEAR sign-in challenges already used.
	nearUsed        nearNonces
	providerMu      sync.Mutex
	ollamaCheckedAt time.Time
	ollamaUp        bool
	// reports caches attestation reports for the proof page, per model.
	reports reportCache
}

// preferredModelID is offered first and is the fallback default when the
// backoffice has not chosen one explicitly.
const preferredModelID = "deepseek-v4.1-flash"

const providerCheckTTL = 30 * time.Second

type structValidator struct{ v *validator.Validate }

func (s structValidator) Validate(out any) error { return s.v.Struct(out) }

func New(d Deps) *Server {
	if d.Store != nil {
		if d.Near == nil {
			d.Near = &billing.Near{DB: d.Store.DB, Ledger: d.Ledger}
		}
		if d.Vouchers == nil {
			d.Vouchers = &billing.Vouchers{DB: d.Store.DB}
		}
	}
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
	api.Get("/verify/:id", limiter.New(limiter.Config{
		Max:        verifyPerMinute,
		Expiration: time.Minute,
		LimitReached: func(c fiber.Ctx) error {
			return errs.RateLimited.New("too many checks; wait a minute")
		},
	}), s.verifyMessage)

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
	auth.Get("/near", s.nearLoginStatus)
	auth.Get("/near/challenge", s.nearChallenge)
	auth.Post("/near", s.nearLogin)
	auth.Post("/logout", s.logout)

	me := api.Group("", s.requireSession)
	me.Get("/me", s.me)
	me.Post("/auth/password", s.changePassword)

	me.Get("/billing/balance", s.balance)
	me.Get("/billing/ledger", s.ledgerHistory)
	me.Get("/billing/crypto", s.cryptoInfo)
	me.Post("/billing/crypto/quote", s.cryptoQuote)
	me.Get("/billing/near", s.nearInfo)
	me.Post("/billing/near/quote", s.nearQuote)
	me.Post("/billing/voucher", limiter.New(limiter.Config{
		Max:          voucherPerMinute,
		Expiration:   time.Minute,
		KeyGenerator: func(c fiber.Ctx) string { return mustAuth(c).UserID.String() },
		LimitReached: func(c fiber.Ctx) error {
			return errs.RateLimited.New("too many tries; wait a minute")
		},
	}), s.claimVoucher)
	me.Delete("/billing/voucher", s.removeVoucher)

	me.Get("/chats", s.listChats)
	me.Post("/chats", s.createChat)
	me.Get("/chats/:id", s.getChat)
	me.Get("/chats/:id/receipts", s.chatReceipts)
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

	me.Get("/settings", s.getSettings)
	me.Put("/settings", s.putSettings)

	me.Get("/files", s.listFiles)
	me.Post("/files", s.uploadFile)
	me.Get("/files/:id/content", s.downloadFile)
	me.Get("/files/:id/thumbnail", s.thumbnail)
	me.Get("/files/:id/view", s.viewImage)
	me.Delete("/files/:id", s.deleteFile)
	me.Put("/files/:id/offline", s.setFileOffline)

	s.backofficeRoutes(me)
}

func (s *Server) health(c fiber.Ctx) error {
	if err := s.Store.DB.Ping(c.Context()); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "db down"})
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

func (s *Server) listModels(c fiber.Ctx) error {
	cat := s.LLM.Current().Catalog
	visible := visibleModels(cat.Models, s.ollamaActive(c.Context()))
	// Single model mode offers that one model only, even when its endpoint
	// is quiet: there's no other choice to fall back to. A disabled model
	// isn't in the catalog, so the mode drops rather than the catalog.
	single := ""
	if id := cat.Single(); id != "" {
		if m, ok := cat.Get(id); ok {
			single, visible = id, []llm.Model{m}
		}
	}
	def := cat.Default().ID
	found := false
	for _, m := range visible {
		if m.ID == def {
			found = true
			break
		}
	}
	if !found {
		def = ""
		if len(visible) > 0 {
			def = visible[0].ID
		}
	}
	var models any = visible
	if !s.isBackoffice(c) {
		models = withoutModelInternals(visible)
	}
	return c.JSON(fiber.Map{"models": models, "default": def, "single": single})
}

// visibleModels drops Ollama models when its endpoint is down and puts the
// preferred model first.
func visibleModels(models []llm.Model, ollamaUp bool) []llm.Model {
	out := make([]llm.Model, 0, len(models))
	for _, m := range models {
		if m.Provider == llm.Ollama && !ollamaUp {
			continue
		}
		if m.ID == preferredModelID {
			out = append([]llm.Model{m}, out...)
			continue
		}
		out = append(out, m)
	}
	return out
}

// ollamaActive reports whether the configured Ollama endpoint answers. The
// result is cached briefly so opening the model picker does not probe it every
// time.
func (s *Server) ollamaActive(ctx context.Context) bool {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if !s.ollamaCheckedAt.IsZero() && time.Since(s.ollamaCheckedAt) < providerCheckTTL {
		return s.ollamaUp
	}
	p, ok := s.LLM.Current().Provider(llm.Ollama)
	if !ok || p.BaseURL == "" {
		s.ollamaCheckedAt = time.Now()
		s.ollamaUp = false
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := llm.Probe(probeCtx, p)
	s.ollamaCheckedAt = time.Now()
	s.ollamaUp = err == nil
	return s.ollamaUp
}

// withoutModelInternals strips what ordinary users don't need from a catalog:
// the per-token prices, the identity instruction, and the upstream model and
// provider a rebrand hides. They only need names, descriptions and
// capabilities; the backoffice and admins using the chat keep the rest.
func withoutModelInternals(models []llm.Model) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		raw, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var view map[string]any
		if err := json.Unmarshal(raw, &view); err != nil {
			continue
		}
		delete(view, "prompt_usd_per_mtok")
		delete(view, "completion_usd_per_mtok")
		delete(view, "identity")
		delete(view, "upstream")
		delete(view, "provider")
		out = append(out, view)
	}
	return out
}

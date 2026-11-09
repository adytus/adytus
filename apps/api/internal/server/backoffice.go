package server

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/errs"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/vault"
)

// The backoffice sets the models and provider keys for the whole server.
// Only users with users.is_admin can reach any of it, its UI included: the
// web app's bundle has no backoffice code and loads it from /api/backoffice/ui
// for admins only.

const privBackoffice = "backoffice"

func (s *Server) backofficeRoutes(me fiber.Router) {
	bo := me.Group("/backoffice", s.requireAdmin)
	bo.Get("", s.backofficeState)
	bo.Get("/users", s.backofficeUsers)
	bo.Get("/crypto", s.backofficeCrypto)
	bo.Get("/vouchers", s.backofficeVouchers)
	bo.Post("/vouchers", s.createVoucher)
	bo.Post("/vouchers/:id/end", s.endVoucher)
	bo.Put("/providers/:id", s.putProvider)
	bo.Post("/providers/:id/test", s.testProvider)
	bo.Get("/providers/:id/offers", s.providerOffers)
	bo.Put("/models/:id", s.putModel)
	bo.Delete("/models/:id", s.deleteModel)
	bo.Put("/default", s.putDefaultModel)
	bo.Put("/single", s.putSingleModel)

	dir := s.Config.BackofficeDir
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		slog.Warn("backoffice UI not built; run bun run build in apps/backoffice", "dir", dir)
	}
	bo.Get("/ui/*", static.New(dir, static.Config{
		CacheDuration: -1,
		ModifyResponse: func(c fiber.Ctx) error {
			c.Set(fiber.HeaderCacheControl, "private, no-store")
			return nil
		},
	}))
}

// requireAdmin runs after requireSession. Everyone else is told the
// backoffice doesn't exist.
func (s *Server) requireAdmin(c fiber.Ctx) error {
	admin, err := s.Store.IsAdmin(c.Context(), mustAuth(c).UserID)
	if err != nil {
		return err
	}
	if !admin {
		return errs.NotFound.New("not found")
	}
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	return c.Next()
}

func (s *Server) privileges(c fiber.Ctx, a *authCtx) ([]string, error) {
	admin, err := s.Store.IsAdmin(c.Context(), a.UserID)
	if err != nil || !admin {
		return []string{}, err
	}
	return []string{privBackoffice}, nil
}

// --- loading and seeding ---

// SeedLLM fills the provider and model tables on first start. Both providers
// get a row, the configured one with the environment's URL and key; models
// come from load when there are none yet. After that the backoffice owns
// them and the environment is not read again.
func (s *Server) SeedLLM(ctx context.Context, seed llm.Provider, load func() (*llm.Catalog, error)) error {
	return s.Store.Tx(ctx, func(tx pgx.Tx) error {
		for _, id := range llm.Providers {
			row := store.ProviderRow{ID: id, BaseURL: llm.DefaultBaseURL[id]}
			if id == seed.ID {
				if seed.BaseURL != "" {
					row.BaseURL = seed.BaseURL
				}
				if seed.APIKey != "" {
					sealed, err := vault.SealProviderKey(s.Config.ServerKey, id, seed.APIKey)
					if err != nil {
						return err
					}
					row.SealedKey = sealed
				}
			}
			if err := store.SeedProvider(ctx, tx, row); err != nil {
				return err
			}
		}
		n, err := store.CountModels(ctx, tx)
		if err != nil || n > 0 {
			return err
		}
		cat, err := load()
		if err != nil {
			return err
		}
		for _, m := range cat.Models {
			if err := store.UpsertModel(ctx, tx, uuid.Nil, m, true); err != nil {
				return err
			}
		}
		slog.Info("seeded model catalog", "models", len(cat.Models))
		return nil
	})
}

// ReloadLLM swaps in the catalog and providers saved in the database.
func (s *Server) ReloadLLM(ctx context.Context) error {
	s.llmMu.Lock()
	defer s.llmMu.Unlock()
	snap, err := s.buildLLM(ctx, s.Store.DB)
	if err != nil {
		return err
	}
	s.LLM.Swap(snap)
	return nil
}

func (s *Server) buildLLM(ctx context.Context, q store.DBTX) (*llm.Snapshot, error) {
	rows, err := store.Models(ctx, q)
	if err != nil {
		return nil, err
	}
	providers, err := s.providers(ctx, q)
	if err != nil {
		return nil, err
	}
	var models []llm.Model
	def, single := "", ""
	for _, r := range rows {
		if r.Enabled {
			models = append(models, r.Spec)
			if r.Default {
				def = r.Spec.ID
			}
			if r.Single {
				single = r.Spec.ID
			}
		}
	}
	if len(models) == 0 {
		return nil, errs.Validation.New("at least one model must stay enabled")
	}
	// With no explicit backoffice default, new chats use the preferred model.
	if def == "" {
		for _, m := range models {
			if m.ID == preferredModelID {
				def = m.ID
				break
			}
		}
	}
	// Single model mode is the default too: it's the only choice left.
	if single != "" {
		def = single
	}
	cat, err := llm.NewCatalog(models)
	if err != nil {
		return nil, errs.Validation.New("%s", err.Error())
	}
	snap, err := s.LLM.Build(ctx, cat.WithDefault(def).WithSingle(single), providers)
	if err != nil {
		return nil, errs.Validation.New("%s", err.Error())
	}
	return snap, nil
}

func (s *Server) providers(ctx context.Context, q store.DBTX) ([]llm.Provider, error) {
	rows, err := store.Providers(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]llm.Provider, 0, len(rows))
	for _, r := range rows {
		p := llm.Provider{ID: r.ID, BaseURL: r.BaseURL}
		if r.SealedKey != nil {
			if p.APIKey, err = vault.OpenProviderKey(s.Config.ServerKey, r.ID, r.SealedKey); err != nil {
				return nil, errs.Internal.Wrap(err, "open %s key; was SERVER_KEY changed?", r.ID)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// changeLLM applies a change and swaps in the result, or refuses it if it
// would leave the catalog unusable.
func (s *Server) changeLLM(c fiber.Ctx, change func(tx pgx.Tx) error) error {
	ctx := c.Context()
	s.llmMu.Lock()
	defer s.llmMu.Unlock()
	var snap *llm.Snapshot
	err := s.Store.Tx(ctx, func(tx pgx.Tx) error {
		if err := change(tx); err != nil {
			return err
		}
		var err error
		snap, err = s.buildLLM(ctx, tx)
		return err
	})
	if err != nil {
		return err
	}
	s.LLM.Swap(snap)
	return s.backofficeState(c)
}

// --- views ---

type providerView struct {
	ID      string `json:"id"`
	BaseURL string `json:"base_url"`
	KeySet  bool   `json:"key_set"`
	// KeyHint is the key's last four characters; the key itself never
	// leaves the server.
	KeyHint   string    `json:"key_hint"`
	UpdatedAt time.Time `json:"updated_at"`
}

type adminModelView struct {
	llm.Model
	Enabled   bool      `json:"enabled"`
	Default   bool      `json:"default"`
	UpdatedAt time.Time `json:"updated_at"`
}

type adminUserView struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	IsAdmin   bool      `json:"is_admin"`
	CreatedAt time.Time `json:"created_at"`

	AvailableTokens  int64 `json:"available_tokens"`
	HeldTokens       int64 `json:"held_tokens"`
	PurchasedTokens  int64 `json:"purchased_tokens"`
	UsedTokens       int64 `json:"used_tokens"`
	SpentTodayTokens int64 `json:"spent_today_tokens"`
	DailyCapTokens   int64 `json:"daily_cap_tokens"`

	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	Requests         int64 `json:"requests"`
	UsedMicros       int64 `json:"used_micros"`
}

func (s *Server) backofficeUsers(c fiber.Ctx) error {
	rows, err := store.Users(c.Context(), s.Store.DB)
	if err != nil {
		return err
	}
	capTokens := billing.Tokens(s.Ledger.DailyCapMicros)
	users := make([]adminUserView, 0, len(rows))
	for _, u := range rows {
		available := u.CreditMicros - u.HeldMicros
		users = append(users, adminUserView{
			ID:               u.ID,
			Email:            u.Email,
			IsAdmin:          u.IsAdmin,
			CreatedAt:        u.CreatedAt,
			AvailableTokens:  billing.Tokens(available),
			HeldTokens:       billing.Tokens(u.HeldMicros),
			PurchasedTokens:  billing.Tokens(u.PurchasedMicros),
			UsedTokens:       billing.Tokens(u.UsedMicros),
			SpentTodayTokens: billing.Tokens(u.SpentTodayMicros),
			DailyCapTokens:   capTokens,
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			Requests:         u.Requests,
			UsedMicros:       u.UsedMicros,
		})
	}
	return c.JSON(fiber.Map{"users": users})
}

func keyHint(key string) string {
	if len(key) < 12 {
		return ""
	}
	return key[len(key)-4:]
}

func (s *Server) backofficeState(c fiber.Ctx) error {
	ctx := c.Context()
	saved, err := store.Providers(ctx, s.Store.DB)
	if err != nil {
		return err
	}
	rows, err := store.Models(ctx, s.Store.DB)
	if err != nil {
		return err
	}
	pv := make([]providerView, 0, len(saved))
	for _, r := range saved {
		key := ""
		if r.SealedKey != nil {
			if key, err = vault.OpenProviderKey(s.Config.ServerKey, r.ID, r.SealedKey); err != nil {
				return errs.Internal.Wrap(err, "open %s key", r.ID)
			}
		}
		pv = append(pv, providerView{r.ID, r.BaseURL, key != "", keyHint(key), r.UpdatedAt})
	}
	def := s.LLM.Current().Catalog.Default().ID
	mv := make([]adminModelView, 0, len(rows))
	for _, r := range rows {
		mv = append(mv, adminModelView{r.Spec, r.Enabled, r.Enabled && r.Spec.ID == def, r.UpdatedAt})
	}
	return c.JSON(fiber.Map{"providers": pv, "models": mv, "single": s.LLM.Current().Catalog.Single()})
}

// --- handlers ---

func providerParam(c fiber.Ctx) (string, error) {
	id := c.Params("id")
	if !llm.IsProvider(id) {
		return "", errs.NotFound.New("no provider %q", id)
	}
	return id, nil
}

// putProvider sets a provider's URL and key. A missing api_key keeps the
// saved one and an empty one clears it.
func (s *Server) putProvider(c fiber.Ctx) error {
	id, err := providerParam(c)
	if err != nil {
		return err
	}
	var req struct {
		BaseURL string  `json:"base_url" validate:"required,http_url,max=500"`
		APIKey  *string `json:"api_key" validate:"omitnil,max=500"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	var sealed []byte
	if req.APIKey != nil && *req.APIKey != "" {
		if sealed, err = vault.SealProviderKey(s.Config.ServerKey, id, *req.APIKey); err != nil {
			return err
		}
	}
	by := mustAuth(c).UserID
	return s.changeLLM(c, func(tx pgx.Tx) error {
		return store.UpdateProvider(c.Context(), tx, by, id, req.BaseURL, req.APIKey != nil, sealed)
	})
}

// testProvider calls the saved provider and lists the models it offers.
func (s *Server) testProvider(c fiber.Ctx) error {
	id, err := providerParam(c)
	if err != nil {
		return err
	}
	providers, err := s.providers(c.Context(), s.Store.DB)
	if err != nil {
		return err
	}
	for _, p := range providers {
		if p.ID == id {
			ids, err := llm.Probe(c.Context(), p)
			if err != nil {
				return errs.Upstream.New("%s", err.Error())
			}
			return c.JSON(fiber.Map{"models": ids})
		}
	}
	return errs.NotFound.New("no provider %q", id)
}

// providerOffers lists the models the provider serves, to add from.
func (s *Server) providerOffers(c fiber.Ctx) error {
	id, err := providerParam(c)
	if err != nil {
		return err
	}
	providers, err := s.providers(c.Context(), s.Store.DB)
	if err != nil {
		return err
	}
	for _, p := range providers {
		if p.ID == id {
			offers, err := llm.Offers(c.Context(), p)
			if err != nil {
				return errs.Upstream.New("%s", err.Error())
			}
			return c.JSON(fiber.Map{"offers": offers})
		}
	}
	return errs.NotFound.New("no provider %q", id)
}

var modelID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func modelParam(c fiber.Ctx) (string, error) {
	id := c.Params("id")
	if !modelID.MatchString(id) {
		return "", errs.Validation.New("model ids are 1 to 64 lowercase letters, digits, dots, dashes or underscores")
	}
	return id, nil
}

// putModel adds or replaces a model. Chats keep their model's id, so
// changing its upstream or price applies to existing chats too.
func (s *Server) putModel(c fiber.Ctx) error {
	id, err := modelParam(c)
	if err != nil {
		return err
	}
	var req struct {
		llm.Model
		Enabled bool `json:"enabled"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	if err := checkRebrand(req.Model); err != nil {
		return err
	}
	req.Model.ID = id
	by := mustAuth(c).UserID
	return s.changeLLM(c, func(tx pgx.Tx) error {
		return store.UpsertModel(c.Context(), tx, by, req.Model, req.Enabled)
	})
}

// checkRebrand bounds the backoffice-written identity text, which goes into
// every system prompt for the model.
func checkRebrand(m llm.Model) error {
	switch {
	case utf8.RuneCountInString(m.Name) > 80:
		return errs.Validation.New("names are up to 80 characters")
	case utf8.RuneCountInString(m.Description) > 400:
		return errs.Validation.New("descriptions are up to 400 characters")
	case utf8.RuneCountInString(m.Identity) > 2000:
		return errs.Validation.New("identity instructions are up to 2000 characters")
	}
	return nil
}

// putSingleModel turns single model mode on for a model, or off with an
// empty id. In it users are offered that model only and every reply runs on
// it, whatever model an older chat names.
func (s *Server) putSingleModel(c fiber.Ctx) error {
	var req struct {
		Model string `json:"model"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	return s.changeLLM(c, func(tx pgx.Tx) error {
		if req.Model == "" {
			return store.SetSingleModel(c.Context(), tx, "")
		}
		rows, err := store.Models(c.Context(), tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Spec.ID == req.Model && !r.Enabled {
				return errs.Validation.New("enable %s before making it the only model", req.Model)
			}
		}
		return store.SetSingleModel(c.Context(), tx, req.Model)
	})
}

// deleteModel removes a model. Chats on it can't continue until they're
// moved to another model; disabling it has the same effect but is undoable.
func (s *Server) deleteModel(c fiber.Ctx) error {
	id, err := modelParam(c)
	if err != nil {
		return err
	}
	return s.changeLLM(c, func(tx pgx.Tx) error { return store.DeleteModel(c.Context(), tx, id) })
}

func (s *Server) putDefaultModel(c fiber.Ctx) error {
	var req struct {
		Model string `json:"model" validate:"required"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	return s.changeLLM(c, func(tx pgx.Tx) error {
		if err := store.SetDefaultModel(c.Context(), tx, req.Model); err != nil {
			return err
		}
		rows, err := store.Models(c.Context(), tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Spec.ID == req.Model && !r.Enabled {
				return errs.Validation.New("enable %s before making it the default", req.Model)
			}
		}
		return nil
	})
}

// backofficeCrypto shows what crypto was received and where it sits. Balances
// are read from the chain on each call, so it is slow with many addresses.
func (s *Server) backofficeCrypto(c fiber.Ctx) error {
	if s.Deposits == nil {
		return c.JSON(billing.Overview{Addresses: []billing.AddressOverview{}})
	}
	o, err := s.Deposits.Overview(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(o)
}

// backofficeVouchers lists the discount codes and what each has cost.
func (s *Server) backofficeVouchers(c fiber.Ctx) error {
	if s.Deposits == nil {
		return c.JSON(fiber.Map{"enabled": false, "vouchers": []billing.VoucherReport{}})
	}
	v, err := s.Deposits.VoucherReports(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"enabled": true, "vouchers": v})
}

func (s *Server) createVoucher(c fiber.Ctx) error {
	if s.Deposits == nil {
		return errs.NotFound.New("crypto payments are not enabled")
	}
	var req struct {
		Code      string    `json:"code" validate:"required,max=64"`
		Percent   int64     `json:"percent" validate:"required"`
		ExpiresAt time.Time `json:"expires_at" validate:"required"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return err
	}
	if err := s.Deposits.CreateVoucher(c.Context(), req.Code, req.Percent, req.ExpiresAt); err != nil {
		return err
	}
	return s.backofficeVouchers(c)
}

func (s *Server) endVoucher(c fiber.Ctx) error {
	if s.Deposits == nil {
		return errs.NotFound.New("crypto payments are not enabled")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return errs.NotFound.New("no voucher with that id")
	}
	if err := s.Deposits.EndVoucher(c.Context(), id); err != nil {
		return err
	}
	return s.backofficeVouchers(c)
}

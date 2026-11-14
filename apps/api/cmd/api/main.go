package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reaganiwadha/adytus/apps/api/internal/base"
	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/blob"
	"github.com/reaganiwadha/adytus/apps/api/internal/config"
	"github.com/reaganiwadha/adytus/apps/api/internal/db"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/server"
	"github.com/reaganiwadha/adytus/apps/api/internal/session"
	"github.com/reaganiwadha/adytus/apps/api/internal/store"
	"github.com/reaganiwadha/adytus/apps/api/internal/tools"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	blobs, err := blob.NewS3(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err != nil {
		return err
	}

	registry, closeTools, err := buildTools(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeTools()

	sessions := session.NewStore(cfg.SessionTTL, cfg.SessionIdle)
	go sessions.Run(ctx, time.Minute)

	ledger := &billing.Ledger{DB: pool, DailyCapMicros: cfg.DailyCapMicros, HoldTTL: cfg.HoldTTL}
	deposits, err := buildDeposits(ctx, cfg, pool, ledger)
	if err != nil {
		return err
	}

	srv := server.New(server.Deps{
		Config:   cfg,
		Store:    &store.Store{DB: pool},
		Ledger:   ledger,
		LLM:      llm.NewRegistry(llm.DialOpenAI),
		Deposits: deposits,
		Sessions: sessions,
		Blobs:    blobs,
		Tools:    registry,
	})

	seed := llm.Provider{ID: cfg.LLMProvider, BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey}
	if err := srv.SeedLLM(ctx, seed, func() (*llm.Catalog, error) {
		return llm.LoadCatalog(cfg.ModelsFile, cfg.LLMProvider)
	}); err != nil {
		return err
	}
	if err := srv.ReloadLLM(ctx); err != nil {
		return err
	}
	catalog := srv.LLM.Current().Catalog

	if deposits != nil {
		go deposits.Run(ctx)
	}
	// NEAR payments are switched on in the backoffice; the watcher idles until then.
	go srv.Near.Run(ctx)

	go func() {
		<-ctx.Done()
		_ = srv.App.ShutdownWithTimeout(10 * time.Second)
	}()

	var toolIDs []string
	for _, t := range registry.Infos() {
		toolIDs = append(toolIDs, t.ID)
	}
	slog.Info("listening", "addr", cfg.Addr, "llm", cfg.LLMProvider, "models", len(catalog.Models), "tools", toolIDs)
	return srv.App.Listen(cfg.Addr)
}

// buildTools sets up the tools. Search is left out without SEARXNG_URL; the
// Python sandbox and document reader are always there, so the API won't start
// without Deno and Pyodide. The returned func releases them.
func buildTools(ctx context.Context, cfg *config.Config) (*tools.Registry, func(), error) {
	var search *tools.Search
	if cfg.SearxngURL != "" {
		search = tools.NewSearch(cfg.SearxngURL)
	}
	py, err := tools.NewPyodide(ctx, tools.PyodideConfig{
		Dir:      cfg.PyodideDir,
		Timeout:  cfg.PythonTimeout,
		MemoryMB: cfg.PythonMemoryMB,
		MaxOut:   16 << 10,
		Parallel: cfg.PythonParallel,
		Warm:     cfg.PythonWarm,
	})
	if err != nil {
		return nil, nil, err
	}
	docs := tools.NewDocuments()
	release := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = py.Close(ctx)
		_ = docs.Close()
	}
	reg, err := tools.NewRegistry(search, py, docs)
	if err != nil {
		release()
		return nil, nil, err
	}
	return reg, release, nil
}

// buildDeposits sets up crypto payments, or returns nil when BASE_XPUB is
// unset. It checks the RPC serves the configured chain, so a mainnet xpub is
// never watched through a testnet endpoint.
func buildDeposits(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, ledger *billing.Ledger) (*billing.Deposits, error) {
	if cfg.BaseXPub == "" {
		return nil, nil
	}
	deriver, err := base.NewDeriver(cfg.BaseXPub)
	if err != nil {
		return nil, err
	}
	chain := base.NewClient(cfg.BaseRPCURL)
	// A rate-limited RPC (http 429) is usually gone in seconds; retry before
	// giving up, so a busy endpoint doesn't crash-loop the whole API.
	var id int64
	for attempt := range 5 {
		if id, err = chain.ChainID(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("BASE_RPC_URL: %w", err)
	}
	if id != cfg.BaseChainID {
		return nil, fmt.Errorf("BASE_RPC_URL serves chain %d, BASE_CHAIN_ID is %d", id, cfg.BaseChainID)
	}
	return &billing.Deposits{
		DB: pool, Ledger: ledger, Chain: chain, Deriver: deriver,
		Cfg: billing.DepositConfig{
			ChainID:       cfg.BaseChainID,
			USDC:          cfg.BaseUSDC,
			ETHUSDFeed:    cfg.BaseETHUSDFeed,
			Confirmations: cfg.CryptoConfirms,
			QuoteTTL:      cfg.CryptoQuoteTTL,
			MinMicros:     cfg.CryptoMinMicros,
			DustMicros:    cfg.CryptoDustMicros,
			ToleranceBps:  cfg.CryptoToleranceBP,
			Poll:          cfg.CryptoPoll,
			FullScanEvery: cfg.CryptoFullScan,
			MaxFeedAge:    time.Hour,
		},
	}, nil
}

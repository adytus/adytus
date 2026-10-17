package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	srv := server.New(server.Deps{
		Config:   cfg,
		Store:    &store.Store{DB: pool},
		Ledger:   &billing.Ledger{DB: pool, DailyCapMicros: cfg.DailyCapMicros, HoldTTL: cfg.HoldTTL},
		LLM:      llm.NewRegistry(llm.DialOpenAI),
		Payments: billing.MockGateway{},
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

// buildTools sets up the tools whose backends are configured. Without the
// configured docker runtime the API won't start unless PYTHON_ENABLED=false.
// The returned func releases the sandbox.
func buildTools(ctx context.Context, cfg *config.Config) (*tools.Registry, func(), error) {
	var search *tools.Search
	if cfg.SearxngURL != "" {
		search = tools.NewSearch(cfg.SearxngURL)
	}
	var py tools.PythonRunner
	if cfg.PythonEnabled {
		p, err := tools.NewGvisor(ctx, tools.GvisorConfig{
			Image:    cfg.PythonImage,
			Runtime:  cfg.PythonRuntime,
			Timeout:  cfg.PythonTimeout,
			MemoryMB: cfg.PythonMemoryMB,
			CPUs:     cfg.PythonCPUs,
			MaxOut:   16 << 10,
			Parallel: cfg.PythonParallel,
			Warm:     cfg.PythonWarm,
		})
		if err != nil {
			return nil, nil, err
		}
		py = p
	}
	closePy := func() {
		if py != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = py.Close(ctx)
		}
	}
	reg, err := tools.NewRegistry(search, py)
	if err != nil {
		closePy()
		return nil, nil, err
	}
	return reg, closePy, nil
}

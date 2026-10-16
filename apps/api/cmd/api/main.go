package main

import (
	"context"
	"errors"
	"io/fs"
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
	catalog, err := llm.LoadCatalog(cfg.ModelsFile)
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

	chatModel, err := llm.NewChatModel(ctx, cfg.LLMProvider, cfg.LLMBaseURL, cfg.LLMAPIKey)
	if err != nil {
		return err
	}

	registry, err := buildTools(ctx, cfg)
	if err != nil {
		return err
	}

	sessions := session.NewStore(cfg.SessionTTL, cfg.SessionIdle)
	go sessions.Run(ctx, time.Minute)

	srv := server.New(server.Deps{
		Config:   cfg,
		Store:    &store.Store{DB: pool},
		Ledger:   &billing.Ledger{DB: pool, DailyCapMicros: cfg.DailyCapMicros, HoldTTL: cfg.HoldTTL},
		Catalog:  catalog,
		LLM:      chatModel,
		Payments: billing.MockGateway{},
		Sessions: sessions,
		Blobs:    blobs,
		Tools:    registry,
	})

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

// buildTools sets up the tools whose backends are configured. A missing
// Python interpreter only disables that tool, so a fresh checkout still runs.
func buildTools(ctx context.Context, cfg *config.Config) (*tools.Registry, error) {
	var search *tools.Search
	if cfg.SearxngURL != "" {
		search = tools.NewSearch(cfg.SearxngURL)
	}
	var py *tools.Python
	if cfg.PythonWasm != "" {
		p, err := tools.NewPython(ctx, tools.PythonConfig{
			WasmPath: cfg.PythonWasm,
			Timeout:  cfg.PythonTimeout,
			MemoryMB: uint32(cfg.PythonMemoryMB),
			MaxOut:   16 << 10,
			Parallel: cfg.PythonParallel,
		})
		switch {
		case errors.Is(err, fs.ErrNotExist):
			slog.Warn("python sandbox disabled: interpreter not found; run scripts/fetch-python-wasm.sh", "path", cfg.PythonWasm)
		case err != nil:
			return nil, err
		default:
			py = p
		}
	}
	return tools.NewRegistry(search, py)
}

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
	})

	go func() {
		<-ctx.Done()
		_ = srv.App.ShutdownWithTimeout(10 * time.Second)
	}()

	slog.Info("listening", "addr", cfg.Addr, "llm", cfg.LLMProvider, "models", len(catalog.Models))
	return srv.App.Listen(cfg.Addr)
}

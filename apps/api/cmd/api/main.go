package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/reaganiwadha/adytus/apps/api/internal/billing"
	"github.com/reaganiwadha/adytus/apps/api/internal/config"
	"github.com/reaganiwadha/adytus/apps/api/internal/db"
	"github.com/reaganiwadha/adytus/apps/api/internal/llm"
	"github.com/reaganiwadha/adytus/apps/api/internal/server"
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

	var provider llm.Provider
	switch cfg.LLMProvider {
	case "openrouter":
		provider = llm.NewOpenRouter(cfg.LLMBaseURL, cfg.LLMAPIKey)
	default:
		provider = llm.NewOllama(cfg.LLMBaseURL)
	}

	srv := server.New(cfg, pool, provider, billing.MockGateway{})

	go func() {
		<-ctx.Done()
		_ = srv.App.Shutdown()
	}()

	slog.Info("listening", "addr", cfg.Addr, "llm", cfg.LLMProvider)
	return srv.App.Listen(cfg.Addr)
}

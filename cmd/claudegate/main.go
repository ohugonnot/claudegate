package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/claudegate/claudegate/internal/api"
	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
	"github.com/claudegate/claudegate/internal/queue"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	store, err := job.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		slog.Error("store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	q := queue.New(cfg, store)

	if err := q.Recovery(context.Background()); err != nil {
		slog.Error("recovery", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	q.StartCleanup(ctx, cfg.JobTTLHours, cfg.CleanupIntervalMinutes)

	if !cfg.DisableKeepalive {
		startKeepalive(cfg.ClaudePath)
	}

	mux := http.NewServeMux()
	h := api.NewHandler(store, q, cfg)
	h.RegisterRoutes(mux)

	handler := api.Chain(mux,
		api.CORS(cfg.CORSOrigins),
		api.RequestID,
		api.Logging,
		api.Auth(cfg.APIKeys),
	)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown error", "error", err)
		}
	}()

	slog.Info("claudegate listening", "addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

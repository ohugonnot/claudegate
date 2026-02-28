package main

import (
	"context"
	"log"
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
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := job.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()

	q := queue.New(cfg, store)

	if err := q.Recovery(context.Background()); err != nil {
		log.Fatalf("recovery: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	q.StartCleanup(ctx, cfg.JobTTLHours, cfg.CleanupIntervalMinutes)

	mux := http.NewServeMux()
	h := api.NewHandler(store, q, cfg)
	h.RegisterRoutes(mux)

	handler := api.CORSMiddleware(cfg.CORSOrigins,
		api.LoggingMiddleware(
			api.RequestIDMiddleware(
				api.AuthMiddleware(cfg.APIKeys, mux),
			),
		),
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
		log.Println("shutting down...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("claudegate listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

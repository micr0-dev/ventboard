package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ventboard/internal/classifier"
	"ventboard/internal/config"
	"ventboard/internal/db"
	boardhttp "ventboard/internal/http"
	"ventboard/internal/posts"
	"ventboard/internal/rate_limit"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}
	if cfg.AppEnv == "dev" && cfg.IPHashSalt == "dev-salt-change-me" {
		logger.Printf("warning: using default development IP hash salt")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabasePath)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer database.Close()

	repo := posts.NewRepository(database)
	limiter := rate_limit.NewLimiter(cfg.PostCooldown, cfg.PostRateLimitWindow, cfg.PostRateLimitCount, cfg.IPHashSalt, cfg.RateLimitHashRotation)
	categorizer := classifier.NewClient(cfg.OllamaURL, cfg.OllamaModel, cfg.ClassifierTimeout)
	postService := posts.NewService(repo, limiter, cfg.PostMaxChars, categorizer.Version())
	worker := classifier.NewWorker(repo, categorizer, cfg.ClassifierPollInterval, cfg.ClassifierMaxRetries, logger)
	formProtector := boardhttp.NewFormProtector(cfg.FormTokenSecret, cfg.FormTokenMinAge, cfg.FormTokenMaxAge)

	app, err := boardhttp.NewServer(database, postService, repo, formProtector, cfg.PublicSourceURL, cfg.FeedLimit, cfg.PostMaxChars)
	if err != nil {
		logger.Fatalf("build server: %v", err)
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go worker.Run(ctx)
	go func() {
		if err := boardhttp.WaitForShutdown(ctx, httpServer, 10*time.Second); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("shutdown error: %v", err)
		}
	}()

	logger.Printf("vent board listening on http://127.0.0.1:%s", cfg.Port)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("serve: %v", err)
	}
}

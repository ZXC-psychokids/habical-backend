package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"habical/backend/libs/logger"
	"habical/backend/services/worker/internal/app"
	"habical/backend/services/worker/internal/config"
	"habical/backend/services/worker/internal/repository"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := logger.New("worker")
	cfg, err := config.Load()
	if err != nil {
		log.Error("config_load_failed", "error", err.Error())
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Error("postgres_connect_failed", "error", err.Error())
		panic(err)
	}
	defer pool.Close()

	repo := repository.New(pool)
	application := app.New(repo, cfg, log)

	log.Info("worker_started")
	if err := application.Run(ctx); err != nil {
		log.Error("worker_failed", "error", err.Error())
		panic(err)
	}
	log.Info("worker_stopped")
}

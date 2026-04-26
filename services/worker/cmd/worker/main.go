package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"habical/backend/services/worker/internal/app"
	"habical/backend/services/worker/internal/config"
	"habical/backend/services/worker/internal/repository"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("failed to load config: ", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatal("failed to connect to postgres: ", err)
	}
	defer pool.Close()

	repo := repository.New(pool)
	application := app.New(repo, cfg)

	log.Println("worker started")
	if err := application.Run(ctx); err != nil {
		log.Fatal("worker failed: ", err)
	}
	log.Println("worker stopped")
}

package main

import (
	"context"
	"net/http"
	"time"

	"habical/backend/libs/logger"
	"habical/backend/libs/pgxutil"
	"habical/backend/services/core/internal/config"
	"habical/backend/services/core/internal/server"
)

func main() {
	cfg := config.Load()
	log := logger.New("core")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxutil.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Error("postgres_connect_failed", "error", err.Error())
		panic(err)
	}
	defer pool.Close()

	srv := server.New(cfg, pool, log)
	addr := ":" + cfg.Port
	log.Info("service_started", "addr", addr)
	if err := http.ListenAndServe(addr, srv.Router()); err != nil {
		log.Error("service_listen_failed", "error", err.Error())
		panic(err)
	}
}

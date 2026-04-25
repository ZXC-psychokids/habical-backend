package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"habical/backend/libs/pgxutil"
	"habical/backend/services/core/internal/config"
	"habical/backend/services/core/internal/server"
)

func main() {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxutil.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("core: postgres connect failed: %v", err)
	}
	defer pool.Close()

	srv := server.New(cfg, pool)
	addr := ":" + cfg.Port
	log.Printf("core service listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Router()); err != nil {
		log.Fatalf("core: listen failed: %v", err)
	}
}

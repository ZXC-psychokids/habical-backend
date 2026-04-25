package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"habical/backend/libs/pgxutil"
	"habical/backend/services/social/internal/config"
	"habical/backend/services/social/internal/server"
)

func main() {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxutil.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("social: postgres connect failed: %v", err)
	}
	defer pool.Close()

	srv := server.New(cfg, pool)
	addr := ":" + cfg.Port
	log.Printf("social service listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Router()); err != nil {
		log.Fatalf("social: listen failed: %v", err)
	}
}

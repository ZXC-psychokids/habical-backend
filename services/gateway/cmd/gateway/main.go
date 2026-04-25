package main

import (
	"log"
	"net/http"

	"habical/backend/services/gateway/internal/config"
	"habical/backend/services/gateway/internal/server"
)

func main() {
	cfg := config.Load()
	srv := server.New(cfg)
	addr := ":" + cfg.Port
	log.Printf("gateway service listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Router()); err != nil {
		log.Fatalf("gateway: listen failed: %v", err)
	}
}

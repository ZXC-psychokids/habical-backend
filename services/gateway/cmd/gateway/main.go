package main

import (
	"net/http"

	"habical/backend/libs/logger"
	"habical/backend/services/gateway/internal/config"
	"habical/backend/services/gateway/internal/server"
)

func main() {
	cfg := config.Load()
	log := logger.New("gateway")
	srv := server.New(cfg, log)
	addr := ":" + cfg.Port
	log.Info("service_started", "addr", addr)
	if err := http.ListenAndServe(addr, srv.Router()); err != nil {
		log.Error("service_listen_failed", "error", err.Error())
		panic(err)
	}
}

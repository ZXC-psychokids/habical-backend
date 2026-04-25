.PHONY: build test run-gateway run-auth run-core run-social docker-up docker-down

build:
	go build ./...

test:
	go test ./...

run-gateway:
	go run ./services/gateway/cmd/gateway

run-auth:
	go run ./services/auth/cmd/auth

run-core:
	go run ./services/core/cmd/core

run-social:
	go run ./services/social/cmd/social

docker-up:
	docker compose up --build

docker-down:
	docker compose down

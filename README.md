# Habical Backend

Backend monorepo for the Habical app.

## Services

- `gateway` on `:4010`
- `auth` on `:4011`
- `core` on `:4012` (participant-2 scope: events + event categories)
- `social` on `:4013`

## Participant-2 scope implemented

- Auth, profile, settings
- Events and event categories
- Friends, friend invites, feed
- Gateway proxy and friend-page orchestration:
  - implemented: `/users/{userId}`, `/users/{userId}/events`
  - intentionally `501` (participant-1 dependency): `/users/{userId}/tasks`, `/users/{userId}/shared-habits`

## Quick start with Docker

Prerequisite: Docker with Compose plugin.

1. Go to `backend` directory.
2. Run:

```bash
docker compose up --build
```

What happens:

- `postgres` starts
- `migrate` applies `deploy/migrations/0001_init.sql`
- `auth`, `core`, `social`, `gateway` start after migration is complete

Gateway URL for frontend: `http://127.0.0.1:4010`

## Manual local run (without Docker)

1. Start PostgreSQL and create database `habical`.
2. Apply migration `deploy/migrations/0001_init.sql`.
3. Set env vars from `.env.example`.
4. Run services in separate terminals:

```bash
go run ./services/auth/cmd/auth
go run ./services/core/cmd/core
go run ./services/social/cmd/social
go run ./services/gateway/cmd/gateway
```

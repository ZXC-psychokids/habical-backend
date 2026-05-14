# Habical Backend

Habical — приложение для личной продуктивности: задачи, привычки, календарные события и активность друзей.
Этот репозиторий — Go-бэкенд: API gateway, доменные сервисы и фоновые воркеры.

## Архитектура
- `services/gateway` (`:4010`) — единая точка входа для фронта, проверка JWT, проксирование в доменные сервисы, friend-page orchestration.
- `services/auth` (`:4011`) — регистрация/логин, refresh/logout, профиль, настройки, аватары.
- `services/core` (`:4012`) — таски, привычки, shared habits, события, категории, friend calendar/tasks/habits endpoints.
- `services/social` (`:4013`) — друзья, инвайты, социальный feed, internal friendship check.
- `services/worker` — фоновые джобы (`generate_future_tasks`, `rebuild_future_tasks`, `compute_habit_streaks`).
- `libs/` — общие пакеты (`authjwt`, `httpx`, `logger`, `pgxutil`, `idgen`, `password`).
- `deploy/migrations/0001_init.sql` — полная схема БД (users, tasks, habits, events, friendships, feed, tokens, background_jobs).

Поток запроса: `Flutter -> Gateway -> Auth/Core/Social -> Postgres`.

## Инструкция по запуску
Требования: Docker + Docker Compose.
Перед запуском нужен файл `.env` в корне `habical-backend`.

```bash
cd habical-backend
make docker-up
```

Поднимется:
- `postgres`
- `migrate` (применяет `0001_init.sql`)
- `auth`, `core`, `social`, `worker`, `gateway`

API для фронта: `http://127.0.0.1:4010`.

## Полезные команды
```bash
make build
make test
make docker-up
make docker-down
```

## Конфиг (основные переменные)
- Общие: `POSTGRES_DSN`, `JWT_SECRET`
- Gateway: `GATEWAY_PORT`, `AUTH_URL`, `CORE_URL`, `SOCIAL_URL`
- Auth: `AUTH_PORT`, `ACCESS_TTL_MINUTES`, `REFRESH_TTL_HOURS`, `AVATAR_STORAGE_DIR`, `AVATAR_BASE_URL`
- Core: `CORE_PORT`
- Social: `SOCIAL_PORT`
- Worker: `WORKER_POLL_INTERVAL_SECONDS`, `WORKER_BATCH_SIZE`, `WORKER_RETRY_DELAY_SECONDS`, `WORKER_MAX_ATTEMPTS`


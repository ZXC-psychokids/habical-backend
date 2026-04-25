package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/httpx"
	"habical/backend/libs/idgen"
	"habical/backend/services/core/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var allowedEventSchedules = []string{"none", "daily", "interval", "weekdays", "monthly"}

type Server struct {
	cfg  config.Config
	pool *pgxpool.Pool
}

func New(cfg config.Config, pool *pgxpool.Pool) *Server {
	return &Server{cfg: cfg, pool: pool}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.authMiddleware)

	r.Get("/me/events", s.handleGetMyEvents)
	r.Post("/me/events", s.handleCreateEvent)
	r.Get("/me/events/{eventId}", s.handleGetEvent)
	r.Patch("/me/events/{eventId}", s.handlePatchEvent)
	r.Delete("/me/events/{eventId}", s.handleDeleteEvent)
	r.Post("/me/events/{eventId}/move", s.handleMoveEvent)

	r.Get("/me/event-categories", s.handleListCategories)
	r.Post("/me/event-categories", s.handleCreateCategory)
	r.Patch("/me/event-categories/{categoryId}", s.handlePatchCategory)
	r.Delete("/me/event-categories/{categoryId}", s.handleDeleteCategory)

	r.Get("/users/{userId}/events", s.handleGetFriendEvents)

	return r
}

type ctxKey string

const userIDKey ctxKey = "user_id"

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := httpx.BearerToken(r)
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
			return
		}
		userID, err := authjwt.ParseAccessToken(token, s.cfg.JWTSecret)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userIDFromContext(ctx context.Context) string {
	raw, _ := ctx.Value(userIDKey).(string)
	return raw
}

type eventCategory struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Color string `json:"color"`
}

type eventTaskLink struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type eventResponse struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	StartsAt     time.Time      `json:"startsAt"`
	EndsAt       time.Time      `json:"endsAt"`
	ScheduleType string         `json:"scheduleType"`
	IntervalDays int            `json:"intervalDays"`
	Weekdays     []int          `json:"weekdays"`
	Category     eventCategory  `json:"category"`
	Task         *eventTaskLink `json:"task"`
}

type friendDayEventResponse struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	StartsAt time.Time     `json:"startsAt"`
	EndsAt   time.Time     `json:"endsAt"`
	Category eventCategory `json:"category"`
}

func parseDateTimeQuery(r *http.Request, key string) (time.Time, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", key)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func parseDateQuery(r *http.Request, key string) (time.Time, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", key)
	}
	return time.Parse("2006-01-02", value)
}

func (s *Server) handleGetMyEvents(w http.ResponseWriter, r *http.Request) {
	from, err := parseDateTimeQuery(r, "from")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Не передан from")
		return
	}
	to, err := parseDateTimeQuery(r, "to")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Не передан to")
		return
	}
	events, err := s.listEvents(r.Context(), userIDFromContext(r.Context()), from, to)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, events)
}

type createEventRequest struct {
	Title        string  `json:"title"`
	StartsAt     string  `json:"startsAt"`
	EndsAt       string  `json:"endsAt"`
	ScheduleType string  `json:"scheduleType"`
	IntervalDays int     `json:"intervalDays"`
	Weekdays     []int   `json:"weekdays"`
	CategoryID   string  `json:"categoryId"`
	TaskID       *string `json:"taskId"`
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой title")
		return
	}
	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный формат дат")
		return
	}
	endsAt, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный формат дат")
		return
	}
	if !endsAt.After(startsAt) {
		httpx.WriteError(w, http.StatusBadRequest, "endsAt <= startsAt")
		return
	}
	if !slices.Contains(allowedEventSchedules, req.ScheduleType) {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный scheduleType")
		return
	}
	if req.IntervalDays < 1 {
		httpx.WriteError(w, http.StatusBadRequest, "intervalDays должен быть >= 1")
		return
	}
	if err := validateWeekdaysRule(req.ScheduleType, req.Weekdays); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	userID := userIDFromContext(r.Context())

	var category eventCategory
	if err := s.pool.QueryRow(r.Context(), `
		SELECT id, title, color
		FROM event_categories
		WHERE id = $1 AND user_id = $2
	`, req.CategoryID, userID).Scan(&category.ID, &category.Title, &category.Color); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Категория не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	var task *eventTaskLink
	if req.TaskID != nil && strings.TrimSpace(*req.TaskID) != "" {
		taskID := strings.TrimSpace(*req.TaskID)
		task = &eventTaskLink{}
		if err := s.pool.QueryRow(r.Context(), `
			SELECT id, title FROM tasks WHERE id = $1 AND user_id = $2
		`, taskID, userID).Scan(&task.ID, &task.Title); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		var linked bool
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE task_id = $1)`, taskID).Scan(&linked); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if linked {
			httpx.WriteError(w, http.StatusConflict, "Задача уже связана с другим событием")
			return
		}
	}

	eventID := idgen.New()
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	var taskID any
	if task != nil {
		taskID = task.ID
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO events (
			id, user_id, category_id, task_id, title, starts_at, ends_at, schedule_type, interval_days, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, eventID, userID, category.ID, taskID, req.Title, startsAt.UTC(), endsAt.UTC(), req.ScheduleType, req.IntervalDays, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	for _, wd := range req.Weekdays {
		if _, err := tx.Exec(r.Context(), `INSERT INTO event_weekdays (event_id, weekday) VALUES ($1, $2)`, eventID, wd); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, eventResponse{
		ID:           eventID,
		Title:        req.Title,
		StartsAt:     startsAt.UTC(),
		EndsAt:       endsAt.UTC(),
		ScheduleType: req.ScheduleType,
		IntervalDays: req.IntervalDays,
		Weekdays:     req.Weekdays,
		Category:     category,
		Task:         task,
	})
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventId")
	userID := userIDFromContext(r.Context())
	ownerID, exists, err := s.getEventOwner(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !exists {
		httpx.WriteError(w, http.StatusNotFound, "Событие не найдено")
		return
	}
	if ownerID != userID {
		httpx.WriteError(w, http.StatusForbidden, "Событие не принадлежит текущему пользователю")
		return
	}
	event, err := s.getEventByID(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, event)
}

type updateEventRequest struct {
	Title        *string `json:"title"`
	StartsAt     *string `json:"startsAt"`
	EndsAt       *string `json:"endsAt"`
	ScheduleType *string `json:"scheduleType"`
	IntervalDays *int    `json:"intervalDays"`
	Weekdays     []int   `json:"weekdays"`
	CategoryID   *string `json:"categoryId"`
}

func (s *Server) handlePatchEvent(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventId")
	userID := userIDFromContext(r.Context())
	ownerID, exists, err := s.getEventOwner(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !exists {
		httpx.WriteError(w, http.StatusNotFound, "Событие не найдено")
		return
	}
	if ownerID != userID {
		httpx.WriteError(w, http.StatusForbidden, "Событие не принадлежит текущему пользователю")
		return
	}

	var req updateEventRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.Title == nil && req.StartsAt == nil && req.EndsAt == nil && req.ScheduleType == nil && req.IntervalDays == nil && req.CategoryID == nil && req.Weekdays == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют поля для обновления")
		return
	}

	current, err := s.getEventByID(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	next := current
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой title")
			return
		}
		next.Title = title
	}
	if req.StartsAt != nil {
		parsed, err := time.Parse(time.RFC3339, *req.StartsAt)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный формат дат")
			return
		}
		next.StartsAt = parsed.UTC()
	}
	if req.EndsAt != nil {
		parsed, err := time.Parse(time.RFC3339, *req.EndsAt)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный формат дат")
			return
		}
		next.EndsAt = parsed.UTC()
	}
	if !next.EndsAt.After(next.StartsAt) {
		httpx.WriteError(w, http.StatusBadRequest, "endsAt <= startsAt")
		return
	}
	if req.ScheduleType != nil {
		if !slices.Contains(allowedEventSchedules, *req.ScheduleType) {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный scheduleType")
			return
		}
		next.ScheduleType = *req.ScheduleType
	}
	if req.IntervalDays != nil {
		if *req.IntervalDays < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "intervalDays должен быть >= 1")
			return
		}
		next.IntervalDays = *req.IntervalDays
	}
	if req.Weekdays != nil {
		next.Weekdays = req.Weekdays
	}
	if err := validateWeekdaysRule(next.ScheduleType, next.Weekdays); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CategoryID != nil {
		categoryID := strings.TrimSpace(*req.CategoryID)
		if categoryID == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой categoryId")
			return
		}
		var category eventCategory
		if err := s.pool.QueryRow(r.Context(), `
			SELECT id, title, color FROM event_categories WHERE id = $1 AND user_id = $2
		`, categoryID, userID).Scan(&category.ID, &category.Title, &category.Color); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, http.StatusNotFound, "Категория не найдена")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		next.Category = category
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(), `
		UPDATE events
		SET title = $2, starts_at = $3, ends_at = $4, schedule_type = $5, interval_days = $6, category_id = $7
		WHERE id = $1
	`, eventID, next.Title, next.StartsAt, next.EndsAt, next.ScheduleType, next.IntervalDays, next.Category.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM event_weekdays WHERE event_id = $1`, eventID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	for _, wd := range next.Weekdays {
		if _, err := tx.Exec(r.Context(), `INSERT INTO event_weekdays (event_id, weekday) VALUES ($1, $2)`, eventID, wd); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	updated, err := s.getEventByID(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventId")
	userID := userIDFromContext(r.Context())
	ownerID, exists, err := s.getEventOwner(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !exists {
		httpx.WriteError(w, http.StatusNotFound, "Событие не найдено")
		return
	}
	if ownerID != userID {
		httpx.WriteError(w, http.StatusForbidden, "Событие не принадлежит текущему пользователю")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM events WHERE id = $1`, eventID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type moveEventRequest struct {
	StartsAt string `json:"startsAt"`
	EndsAt   string `json:"endsAt"`
}

func (s *Server) handleMoveEvent(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventId")
	userID := userIDFromContext(r.Context())
	ownerID, exists, err := s.getEventOwner(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !exists {
		httpx.WriteError(w, http.StatusNotFound, "Событие не найдено")
		return
	}
	if ownerID != userID {
		httpx.WriteError(w, http.StatusForbidden, "Событие не принадлежит текущему пользователю")
		return
	}

	var req moveEventRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный формат дат")
		return
	}
	endsAt, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный формат дат")
		return
	}
	if !endsAt.After(startsAt) {
		httpx.WriteError(w, http.StatusBadRequest, "endsAt <= startsAt")
		return
	}

	if _, err := s.pool.Exec(r.Context(), `
		UPDATE events SET starts_at = $2, ends_at = $3 WHERE id = $1
	`, eventID, startsAt.UTC(), endsAt.UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	updated, err := s.getEventByID(r.Context(), eventID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, title, color
		FROM event_categories
		WHERE user_id = $1
		ORDER BY title ASC
	`, userIDFromContext(r.Context()))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer rows.Close()

	result := make([]eventCategory, 0)
	for rows.Next() {
		var item eventCategory
		if err := rows.Scan(&item.ID, &item.Title, &item.Color); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		result = append(result, item)
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

type createCategoryRequest struct {
	Title string `json:"title"`
	Color string `json:"color"`
}

func (s *Server) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	var req createCategoryRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Color = strings.TrimSpace(req.Color)
	if req.Title == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой title")
		return
	}
	if req.Color == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой color")
		return
	}
	item := eventCategory{
		ID:    idgen.New(),
		Title: req.Title,
		Color: req.Color,
	}
	if _, err := s.pool.Exec(r.Context(), `
		INSERT INTO event_categories (id, user_id, title, color)
		VALUES ($1, $2, $3, $4)
	`, item.ID, userIDFromContext(r.Context()), item.Title, item.Color); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, item)
}

type patchCategoryRequest struct {
	Title *string `json:"title"`
	Color *string `json:"color"`
}

func (s *Server) handlePatchCategory(w http.ResponseWriter, r *http.Request) {
	categoryID := chi.URLParam(r, "categoryId")
	userID := userIDFromContext(r.Context())

	var exists bool
	if err := s.pool.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM event_categories WHERE id = $1 AND user_id = $2)
	`, categoryID, userID).Scan(&exists); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !exists {
		httpx.WriteError(w, http.StatusNotFound, "Категория не найдена")
		return
	}

	var req patchCategoryRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.Title == nil && req.Color == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют поля для обновления")
		return
	}

	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой title")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `
			UPDATE event_categories SET title = $2 WHERE id = $1
		`, categoryID, title); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.Color != nil {
		color := strings.TrimSpace(*req.Color)
		if color == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой color")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `
			UPDATE event_categories SET color = $2 WHERE id = $1
		`, categoryID, color); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}

	var updated eventCategory
	if err := s.pool.QueryRow(r.Context(), `
		SELECT id, title, color FROM event_categories WHERE id = $1
	`, categoryID).Scan(&updated.ID, &updated.Title, &updated.Color); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	categoryID := chi.URLParam(r, "categoryId")
	userID := userIDFromContext(r.Context())

	var exists bool
	if err := s.pool.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM event_categories WHERE id = $1 AND user_id = $2)
	`, categoryID, userID).Scan(&exists); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !exists {
		httpx.WriteError(w, http.StatusNotFound, "Категория не найдена")
		return
	}

	var used bool
	if err := s.pool.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM events WHERE category_id = $1)
	`, categoryID).Scan(&used); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if used {
		httpx.WriteError(w, http.StatusConflict, "Категория используется событиями")
		return
	}

	if _, err := s.pool.Exec(r.Context(), `DELETE FROM event_categories WHERE id = $1`, categoryID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetFriendEvents(w http.ResponseWriter, r *http.Request) {
	friendID := chi.URLParam(r, "userId")
	q := r.URL.Query()
	dateQ := strings.TrimSpace(q.Get("date"))
	fromQ := strings.TrimSpace(q.Get("from"))
	toQ := strings.TrimSpace(q.Get("to"))

	switch {
	case dateQ != "" && fromQ == "" && toQ == "":
		date, err := time.Parse("2006-01-02", dateQ)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный date")
			return
		}
		start := date.UTC()
		end := start.Add(24 * time.Hour)
		events, err := s.listEvents(r.Context(), friendID, start, end)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		response := make([]friendDayEventResponse, 0, len(events))
		for _, ev := range events {
			response = append(response, friendDayEventResponse{
				ID:       ev.ID,
				Title:    ev.Title,
				StartsAt: ev.StartsAt,
				EndsAt:   ev.EndsAt,
				Category: ev.Category,
			})
		}
		httpx.WriteJSON(w, http.StatusOK, response)
		return

	case dateQ == "" && fromQ != "" && toQ != "":
		from, err := time.Parse(time.RFC3339, fromQ)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректные даты")
			return
		}
		to, err := time.Parse(time.RFC3339, toQ)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректные даты")
			return
		}
		events, err := s.listEvents(r.Context(), friendID, from.UTC(), to.UTC())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, events)
		return

	default:
		httpx.WriteError(w, http.StatusBadRequest, "Invalid query combination: use either date or from+to")
		return
	}
}

func validateWeekdaysRule(scheduleType string, weekdays []int) error {
	if scheduleType == "weekdays" {
		if len(weekdays) == 0 {
			return errors.New("weekdays обязателен для scheduleType=weekdays")
		}
	} else if len(weekdays) > 0 {
		return errors.New("weekdays должен быть пустым для выбранного scheduleType")
	}
	for _, wd := range weekdays {
		if wd < 1 || wd > 7 {
			return errors.New("weekday должен быть от 1 до 7")
		}
	}
	return nil
}

func (s *Server) getEventOwner(ctx context.Context, eventID string) (string, bool, error) {
	var ownerID string
	err := s.pool.QueryRow(ctx, `SELECT user_id FROM events WHERE id = $1`, eventID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return ownerID, true, nil
}

func (s *Server) getEventByID(ctx context.Context, eventID string) (eventResponse, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			e.id, e.title, e.starts_at, e.ends_at, e.schedule_type, e.interval_days,
			ec.id, ec.title, ec.color,
			t.id, t.title
		FROM events e
		JOIN event_categories ec ON ec.id = e.category_id
		LEFT JOIN tasks t ON t.id = e.task_id
		WHERE e.id = $1
	`, eventID)
	if err != nil {
		return eventResponse{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return eventResponse{}, pgx.ErrNoRows
	}
	ev, err := scanEventRow(rows)
	if err != nil {
		return eventResponse{}, err
	}
	ev.Weekdays, err = s.getEventWeekdays(ctx, ev.ID)
	if err != nil {
		return eventResponse{}, err
	}
	return ev, nil
}

func (s *Server) listEvents(ctx context.Context, userID string, from time.Time, to time.Time) ([]eventResponse, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			e.id, e.title, e.starts_at, e.ends_at, e.schedule_type, e.interval_days,
			ec.id, ec.title, ec.color,
			t.id, t.title
		FROM events e
		JOIN event_categories ec ON ec.id = e.category_id
		LEFT JOIN tasks t ON t.id = e.task_id
		WHERE e.user_id = $1
		  AND e.starts_at >= $2
		  AND e.starts_at <= $3
		ORDER BY e.starts_at ASC
	`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]eventResponse, 0)
	for rows.Next() {
		ev, err := scanEventRow(rows)
		if err != nil {
			return nil, err
		}
		ev.Weekdays, err = s.getEventWeekdays(ctx, ev.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func scanEventRow(rows pgx.Rows) (eventResponse, error) {
	var ev eventResponse
	var taskID *string
	var taskTitle *string
	if err := rows.Scan(
		&ev.ID, &ev.Title, &ev.StartsAt, &ev.EndsAt, &ev.ScheduleType, &ev.IntervalDays,
		&ev.Category.ID, &ev.Category.Title, &ev.Category.Color,
		&taskID, &taskTitle,
	); err != nil {
		return eventResponse{}, err
	}
	if taskID != nil && taskTitle != nil {
		ev.Task = &eventTaskLink{ID: *taskID, Title: *taskTitle}
	}
	return ev, nil
}

func (s *Server) getEventWeekdays(ctx context.Context, eventID string) ([]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT weekday FROM event_weekdays WHERE event_id = $1 ORDER BY weekday ASC
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int, 0)
	for rows.Next() {
		var wd int
		if err := rows.Scan(&wd); err != nil {
			return nil, err
		}
		out = append(out, wd)
	}
	return out, nil
}

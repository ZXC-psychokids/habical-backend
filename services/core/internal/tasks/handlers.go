package tasks

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"habical/backend/libs/httpx"
	"habical/backend/libs/idgen"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const userIDContextKey = "user_id"

type Handler struct {
	pool *pgxpool.Pool
}

type HabitShort struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Color string `json:"color"`
}

type EventTimeLink struct {
	ID       string    `json:"id"`
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}

type TaskResponse struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	TaskDate    string         `json:"taskDate"`
	Position    int            `json:"position"`
	IsCompleted bool           `json:"isCompleted"`
	ManualColor *string        `json:"manualColor"`
	Habit       *HabitShort    `json:"habit"`
	Event       *EventTimeLink `json:"event"`
}

type createTaskRequest struct {
	Title       string  `json:"title"`
	TaskDate    string  `json:"taskDate"`
	ManualColor *string `json:"manualColor"`
	Position    int     `json:"position"`
}

type updateTaskRequest struct {
	Title       *string `json:"title"`
	TaskDate    *string `json:"taskDate"`
	ManualColor *string `json:"manualColor"`
	Position    *int    `json:"position"`
}

type reorderTasksRequest struct {
	Items []struct {
		TaskID   string `json:"taskId"`
		Position int    `json:"position"`
		TaskDate string `json:"taskDate"`
	} `json:"items"`
}

type linkTaskEventRequest struct {
	EventID string `json:"eventId"`
}

type taskToggleResponse struct {
	ID          string `json:"id"`
	IsCompleted bool   `json:"isCompleted"`
}

type taskRow struct {
	ID          string
	Title       string
	TaskDate    time.Time
	Position    int
	IsCompleted bool
	ManualColor *string
	HabitID     *string
	HabitTitle  *string
	HabitColor  *string
	EventID     *string
	EventStart  *time.Time
	EventEnd    *time.Time
}

func New(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/me/tasks", h.handleListTasks)
	r.Post("/me/tasks", h.handleCreateTask)
	r.Post("/me/tasks/reorder", h.handleReorderTasks)
	r.Get("/me/tasks/{taskId}", h.handleGetTask)
	r.Patch("/me/tasks/{taskId}", h.handlePatchTask)
	r.Delete("/me/tasks/{taskId}", h.handleDeleteTask)
	r.Post("/me/tasks/{taskId}/toggle", h.handleToggleTask)
	r.Post("/me/tasks/{taskId}/event-link", h.handleLinkTaskEvent)
	r.Delete("/me/tasks/{taskId}/event-link", h.handleDeleteTaskEventLink)
}

func (h *Handler) handleListTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	date, err := parseDateQuery(r, "date")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Не передана дата")
		return
	}
	tasks, err := h.listTasks(r.Context(), userID, date)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tasks)
}

func (h *Handler) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	var req createTaskRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустое название")
		return
	}
	if req.Position < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "position должен быть >= 0")
		return
	}
	date, err := time.Parse("2006-01-02", req.TaskDate)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректная дата")
		return
	}
	if req.ManualColor != nil {
		color := strings.TrimSpace(*req.ManualColor)
		if color == "" {
			req.ManualColor = nil
		} else {
			req.ManualColor = &color
		}
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(), `
        UPDATE tasks
        SET position = position + 1
        WHERE user_id = $1 AND task_date = $2 AND position >= $3
    `, userID, date, req.Position); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	taskID := idgen.New()
	if _, err := tx.Exec(r.Context(), `
        INSERT INTO tasks (id, user_id, title, task_date, manual_color, position, is_completed)
        VALUES ($1, $2, $3, $4, $5, $6, false)
    `, taskID, userID, req.Title, date, req.ManualColor, req.Position); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	if err := normalizePositions(r.Context(), tx, userID, date); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, TaskResponse{
		ID:          taskID,
		Title:       req.Title,
		TaskDate:    date.Format("2006-01-02"),
		Position:    req.Position,
		IsCompleted: false,
		ManualColor: req.ManualColor,
	})
}

func (h *Handler) handleReorderTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	var req reorderTasksRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if len(req.Items) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "items обязаны быть непустыми")
		return
	}

	taskIDs := make([]string, 0, len(req.Items))
	seen := make(map[string]struct{}, len(req.Items))
	for _, item := range req.Items {
		if strings.TrimSpace(item.TaskID) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой taskId")
			return
		}
		if item.Position < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "position должен быть >= 0")
			return
		}
		if _, exists := seen[item.TaskID]; exists {
			httpx.WriteError(w, http.StatusBadRequest, "Дублирование taskId")
			return
		}
		seen[item.TaskID] = struct{}{}
		taskIDs = append(taskIDs, item.TaskID)
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	rows, err := tx.Query(r.Context(), `
        SELECT id, task_date FROM tasks WHERE id = ANY($1::uuid[]) AND user_id = $2
    `, taskIDs, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer rows.Close()

	found := make(map[string]time.Time, len(req.Items))
	for rows.Next() {
		var id string
		var date time.Time
		if err := rows.Scan(&id, &date); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		found[id] = date
	}
	if len(found) != len(req.Items) {
		httpx.WriteError(w, http.StatusNotFound, "Одна или несколько задач не найдены")
		return
	}

	dates := make(map[string]struct{}, len(req.Items)*2)
	for _, item := range req.Items {
		if _, err := time.Parse("2006-01-02", item.TaskDate); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректная дата в item")
			return
		}
		if err := h.updateTaskDatePosition(r.Context(), tx, item.TaskID, userID, item.TaskDate, item.Position); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		oldDate := found[item.TaskID]
		dates[oldDate.Format("2006-01-02")] = struct{}{}
		dates[item.TaskDate] = struct{}{}
	}

	for dateString := range dates {
		date, _ := time.Parse("2006-01-02", dateString)
		if err := normalizePositions(r.Context(), tx, userID, date); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]string{"message": "Порядок задач обновлён"})
}

func (h *Handler) handleGetTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	if strings.TrimSpace(taskID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой taskId")
		return
	}
	task, err := h.getTaskByID(r.Context(), taskID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, task)
}

func (h *Handler) handlePatchTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	var req updateTaskRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.Title == nil && req.TaskDate == nil && req.ManualColor == nil && req.Position == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют поля для обновления")
		return
	}
	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if trimmed == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустое название")
			return
		}
		req.Title = &trimmed
	}
	var newDate time.Time
	if req.TaskDate != nil {
		date, err := time.Parse("2006-01-02", strings.TrimSpace(*req.TaskDate))
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректная дата")
			return
		}
		newDate = date
	}
	if req.Position != nil && *req.Position < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "position должен быть >= 0")
		return
	}
	if req.ManualColor != nil {
		color := strings.TrimSpace(*req.ManualColor)
		if color == "" {
			req.ManualColor = nil
		} else {
			req.ManualColor = &color
		}
	}

	taskID := chi.URLParam(r, "taskId")
	taskRaw, err := h.getTaskRaw(r.Context(), taskID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if req.Title != nil && taskRaw.HabitID != nil {
		httpx.WriteError(w, http.StatusForbidden, "Нельзя менять название задачи привычки")
		return
	}
	if req.ManualColor != nil && (taskRaw.HabitID != nil || taskRaw.EventID != nil) {
		httpx.WriteError(w, http.StatusForbidden, "Нельзя менять цвет задачи, привязанной к привычке или событию")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	oldDate := taskRaw.TaskDate
	newTaskDate := oldDate
	if !newDate.IsZero() {
		newTaskDate = newDate
	}

	fields := make([]string, 0, 4)
	args := make([]any, 0, 6)
	args = append(args, taskRaw.ID)
	argPos := 2
	if req.Title != nil {
		fields = append(fields, "title = $"+strconv.Itoa(argPos))
		args = append(args, *req.Title)
		argPos++
	}
	if req.ManualColor != nil {
		fields = append(fields, "manual_color = $"+strconv.Itoa(argPos))
		args = append(args, req.ManualColor)
		argPos++
	}
	if req.Position != nil {
		fields = append(fields, "position = $"+strconv.Itoa(argPos))
		args = append(args, *req.Position)
		argPos++
	}
	if !newDate.IsZero() {
		fields = append(fields, "task_date = $"+strconv.Itoa(argPos))
		args = append(args, newTaskDate)
		argPos++
	}
	if len(fields) > 0 {
		query := "UPDATE tasks SET " + strings.Join(fields, ", ") + " WHERE id = $1 AND user_id = $" + strconv.Itoa(argPos)
		args = append(args, userID)
		if _, err := tx.Exec(r.Context(), query, args...); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}

	if !oldDate.Equal(newTaskDate) || req.Position != nil {
		if err := normalizePositions(r.Context(), tx, userID, oldDate); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if err := normalizePositions(r.Context(), tx, userID, newTaskDate); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	task, err := h.getTaskByID(r.Context(), taskRaw.ID, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, task)
}

func (h *Handler) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	taskRaw, err := h.getTaskRaw(r.Context(), taskID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if taskRaw.HabitID != nil {
		httpx.WriteError(w, http.StatusForbidden, "Можно удалять только ручные задачи")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(), `UPDATE events SET task_id = NULL WHERE task_id = $1 AND user_id = $2`, taskID, userID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM tasks WHERE id = $1 AND user_id = $2`, taskID, userID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := normalizePositions(r.Context(), tx, userID, taskRaw.TaskDate); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleToggleTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	var response taskToggleResponse
	if err := h.pool.QueryRow(r.Context(), `UPDATE tasks SET is_completed = NOT is_completed WHERE id = $1 AND user_id = $2 RETURNING id, is_completed`, taskID, userID).Scan(&response.ID, &response.IsCompleted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) handleLinkTaskEvent(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	var req linkTaskEventRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.EventID = strings.TrimSpace(req.EventID)
	if req.EventID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой eventId")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	if _, err := h.getTaskRaw(r.Context(), taskID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	var existingTaskID *string
	if err := tx.QueryRow(r.Context(), `SELECT task_id FROM events WHERE id = $1 AND user_id = $2`, req.EventID, userID).Scan(&existingTaskID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Событие не найдено")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if existingTaskID != nil && *existingTaskID != taskID {
		httpx.WriteError(w, http.StatusConflict, "Событие уже связано с другой задачей")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE events SET task_id = $1 WHERE id = $2`, taskID, req.EventID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	task, err := h.getTaskByID(r.Context(), taskID, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, task)
}

func (h *Handler) handleDeleteTaskEventLink(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	if _, err := h.getTaskRaw(r.Context(), taskID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := h.pool.Exec(r.Context(), `UPDATE events SET task_id = NULL WHERE task_id = $1 AND user_id = $2`, taskID, userID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTasks(ctx context.Context, userID string, date time.Time) ([]TaskResponse, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT t.id, t.title, t.task_date, t.position, t.is_completed, t.manual_color,
               t.habit_id, h.title, h.color,
               e.id, e.starts_at, e.ends_at
        FROM tasks t
        LEFT JOIN habits h ON h.id = t.habit_id
        LEFT JOIN events e ON e.task_id = t.id
        WHERE t.user_id = $1 AND t.task_date = $2
        ORDER BY
          CASE WHEN e.starts_at IS NOT NULL THEN 0 WHEN t.habit_id IS NOT NULL THEN 1 ELSE 2 END,
          e.starts_at ASC NULLS LAST,
          t.position ASC
    `, userID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]TaskResponse, 0)
	for rows.Next() {
		row, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row.toResponse())
	}
	return result, rows.Err()
}

func (h *Handler) getTaskByID(ctx context.Context, taskID, userID string) (TaskResponse, error) {
	row, err := h.getTaskRaw(ctx, taskID, userID)
	if err != nil {
		return TaskResponse{}, err
	}
	return row.toResponse(), nil
}

func (h *Handler) getTaskRaw(ctx context.Context, taskID, userID string) (taskRow, error) {
	row := taskRow{}
	err := h.pool.QueryRow(ctx, `
        SELECT t.id, t.title, t.task_date, t.position, t.is_completed, t.manual_color,
               t.habit_id, h.title, h.color,
               e.id, e.starts_at, e.ends_at
        FROM tasks t
        LEFT JOIN habits h ON h.id = t.habit_id
        LEFT JOIN events e ON e.task_id = t.id
        WHERE t.id = $1 AND t.user_id = $2
    `, taskID, userID).Scan(
		&row.ID, &row.Title, &row.TaskDate, &row.Position, &row.IsCompleted, &row.ManualColor,
		&row.HabitID, &row.HabitTitle, &row.HabitColor,
		&row.EventID, &row.EventStart, &row.EventEnd,
	)
	if err != nil {
		return taskRow{}, err
	}
	return row, nil
}

func scanTaskRow(rows pgx.Rows) (taskRow, error) {
	row := taskRow{}
	if err := rows.Scan(
		&row.ID, &row.Title, &row.TaskDate, &row.Position, &row.IsCompleted, &row.ManualColor,
		&row.HabitID, &row.HabitTitle, &row.HabitColor,
		&row.EventID, &row.EventStart, &row.EventEnd,
	); err != nil {
		return taskRow{}, err
	}
	return row, nil
}

func (r taskRow) toResponse() TaskResponse {
	var habit *HabitShort
	if r.HabitID != nil {
		habit = &HabitShort{ID: *r.HabitID, Title: *r.HabitTitle, Color: *r.HabitColor}
	}
	var event *EventTimeLink
	if r.EventID != nil {
		event = &EventTimeLink{ID: *r.EventID, StartsAt: *r.EventStart, EndsAt: *r.EventEnd}
	}
	return TaskResponse{
		ID:          r.ID,
		Title:       r.Title,
		TaskDate:    r.TaskDate.Format("2006-01-02"),
		Position:    r.Position,
		IsCompleted: r.IsCompleted,
		ManualColor: r.ManualColor,
		Habit:       habit,
		Event:       event,
	}
}

func (h *Handler) updateTaskDatePosition(ctx context.Context, tx pgx.Tx, taskID, userID string, dateStr string, position int) error {
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE tasks SET task_date = $2, position = $3 WHERE id = $1 AND user_id = $4`, taskID, date, position, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func normalizePositions(ctx context.Context, tx pgx.Tx, userID string, date time.Time) error {
	rows, err := tx.Query(ctx, `SELECT id FROM tasks WHERE user_id = $1 AND task_date = $2 ORDER BY position ASC, id ASC`, userID, date)
	if err != nil {
		return err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE tasks SET position = $2 WHERE id = $1`, id, i); err != nil {
			return err
		}
	}
	return nil
}

func userIDFromContext(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(userIDContextKey).(string)
	return raw, ok
}

func parseDateQuery(r *http.Request, key string) (time.Time, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return time.Time{}, errors.New("date is required")
	}
	return time.Parse("2006-01-02", value)
}

package habits

import (
	"context"
	"errors"
	"net/http"
	"sort"
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

var validHabitScheduleTypes = map[string]struct{}{
	"daily":    {},
	"interval": {},
	"weekdays": {},
}

type publicUser struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
}

type habitResponse struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	Color        string      `json:"color"`
	ScheduleType string      `json:"scheduleType"`
	IntervalDays int         `json:"intervalDays"`
	Weekdays     []int       `json:"weekdays"`
	IsShared     bool        `json:"isShared"`
	SharedWith   *publicUser `json:"sharedWith"`
	StreakDays   int         `json:"streakDays"`
}

type createHabitRequest struct {
	Title        string `json:"title"`
	Color        string `json:"color"`
	ScheduleType string `json:"scheduleType"`
	IntervalDays int    `json:"intervalDays"`
	Weekdays     []int  `json:"weekdays"`
}

type updateHabitRequest struct {
	Title        *string `json:"title"`
	Color        *string `json:"color"`
	ScheduleType *string `json:"scheduleType"`
	IntervalDays *int    `json:"intervalDays"`
	Weekdays     []int   `json:"weekdays"`
}

type habitRow struct {
	ID           string
	Title        string
	Color        string
	ScheduleType string
	IntervalDays int
	Weekdays     []int
	IsShared     bool
	SharedWithID *string
	SharedHandle *string
}

type calendarSummaryResponse struct {
	Days []calendarSummaryDay `json:"days"`
}

type calendarSummaryDay struct {
	Date            string              `json:"date"`
	CompletedHabits []calendarHabitInfo `json:"completedHabits"`
}

type calendarHabitInfo struct {
	HabitID string `json:"habitId"`
	Color   string `json:"color"`
}

type habitScheduleInfo struct {
	ScheduleType string
	IntervalDays int
	Weekdays     []int
}

func New(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/me/habits", h.handleListHabits)
	r.Post("/me/habits", h.handleCreateHabit)
	r.Get("/me/habits/calendar-summary", h.handleGetHabitCalendarSummary)
	r.Get("/me/habits/{habitId}", h.handleGetHabit)
	r.Patch("/me/habits/{habitId}", h.handlePatchHabit)
	r.Delete("/me/habits/{habitId}", h.handleDeleteHabit)
}

func (h *Handler) handleListHabits(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	habits, err := h.listHabits(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, habits)
}

func (h *Handler) handleCreateHabit(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	var req createHabitRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if err := validateHabitPayload(req.Title, req.Color, req.ScheduleType, req.IntervalDays, req.Weekdays); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	habitID := idgen.New()
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(), `
        INSERT INTO habits (id, user_id, title, color, schedule_type, interval_days, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
    `, habitID, userID, req.Title, req.Color, req.ScheduleType, req.IntervalDays, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if len(req.Weekdays) > 0 {
		if err := insertHabitWeekdays(r.Context(), tx, habitID, req.Weekdays); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	response := habitResponse{
		ID:           habitID,
		Title:        req.Title,
		Color:        req.Color,
		ScheduleType: req.ScheduleType,
		IntervalDays: req.IntervalDays,
		Weekdays:     append([]int(nil), req.Weekdays...),
		IsShared:     false,
		SharedWith:   nil,
		StreakDays:   0,
	}
	httpx.WriteJSON(w, http.StatusCreated, response)
}

func (h *Handler) handleGetHabitCalendarSummary(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	from, err := parseDateQuery(r, "from")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Не передан from")
		return
	}
	to, err := parseDateQuery(r, "to")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Не передан to")
		return
	}
	if to.Before(from) {
		httpx.WriteError(w, http.StatusBadRequest, "Неверный диапазон дат")
		return
	}
	summary, err := h.buildCalendarSummary(r.Context(), userID, from, to)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, summary)
}

func (h *Handler) handleGetHabit(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	habitID := chi.URLParam(r, "habitId")
	if strings.TrimSpace(habitID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой habitId")
		return
	}
	habit, err := h.getHabitByID(r.Context(), habitID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Привычка не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, habit)
}

func (h *Handler) handlePatchHabit(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	habitID := chi.URLParam(r, "habitId")
	if strings.TrimSpace(habitID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой habitId")
		return
	}
	var req updateHabitRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.Title == nil && req.Color == nil && req.ScheduleType == nil && req.IntervalDays == nil && req.Weekdays == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют поля для обновления")
		return
	}
	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if trimmed == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой title")
			return
		}
		req.Title = &trimmed
	}
	if req.Color != nil {
		trimmed := strings.TrimSpace(*req.Color)
		if trimmed == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой color")
			return
		}
		req.Color = &trimmed
	}
	if req.ScheduleType != nil {
		if _, ok := validHabitScheduleTypes[*req.ScheduleType]; !ok {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный scheduleType")
			return
		}
	}
	if req.IntervalDays != nil && *req.IntervalDays < 1 {
		httpx.WriteError(w, http.StatusBadRequest, "intervalDays должен быть >= 1")
		return
	}
	if req.ScheduleType != nil {
		if *req.ScheduleType == "weekdays" && len(req.Weekdays) == 0 {
			httpx.WriteError(w, http.StatusBadRequest, "weekdays обязателен для scheduleType=weekdays")
			return
		}
		if *req.ScheduleType != "weekdays" && len(req.Weekdays) > 0 {
			httpx.WriteError(w, http.StatusBadRequest, "weekdays должен быть пустым для этого scheduleType")
			return
		}
	}
	if len(req.Weekdays) > 0 {
		if err := validateWeekdays(req.Weekdays); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	habitRow, err := h.getHabitRow(r.Context(), tx, habitID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Привычка не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if req.ScheduleType == nil && req.Weekdays != nil && habitRow.ScheduleType != "weekdays" {
		httpx.WriteError(w, http.StatusBadRequest, "weekdays можно менять только для scheduleType=weekdays")
		return
	}

	fields := make([]string, 0, 5)
	args := make([]any, 0, 6)
	args = append(args, habitID)
	argPos := 2
	if req.Title != nil {
		fields = append(fields, "title = $"+strconv.Itoa(argPos))
		args = append(args, *req.Title)
		argPos++
	}
	if req.Color != nil {
		fields = append(fields, "color = $"+strconv.Itoa(argPos))
		args = append(args, *req.Color)
		argPos++
	}
	if req.ScheduleType != nil {
		fields = append(fields, "schedule_type = $"+strconv.Itoa(argPos))
		args = append(args, *req.ScheduleType)
		argPos++
	}
	if req.IntervalDays != nil {
		fields = append(fields, "interval_days = $"+strconv.Itoa(argPos))
		args = append(args, *req.IntervalDays)
		argPos++
	}
	if len(fields) > 0 {
		query := "UPDATE habits SET " + strings.Join(fields, ", ") + " WHERE id = $1 AND user_id = $" + strconv.Itoa(argPos)
		args = append(args, userID)
		if _, err := tx.Exec(r.Context(), query, args...); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.Weekdays != nil {
		if _, err := tx.Exec(r.Context(), `DELETE FROM habit_weekdays WHERE habit_id = $1`, habitID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if len(req.Weekdays) > 0 {
			if err := insertHabitWeekdays(r.Context(), tx, habitID, req.Weekdays); err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
				return
			}
		}
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	if _, err := tx.Exec(r.Context(), `DELETE FROM tasks WHERE habit_id = $1 AND task_date >= $2 AND is_completed = FALSE`, habitID, today); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	habit, err := h.getHabitByID(r.Context(), habitID, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, habit)
}

func (h *Handler) handleDeleteHabit(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	habitID := chi.URLParam(r, "habitId")
	if strings.TrimSpace(habitID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой habitId")
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := h.getHabitRow(r.Context(), tx, habitID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Привычка не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if _, err := tx.Exec(r.Context(), `DELETE FROM tasks WHERE habit_id = $1 AND task_date >= $2 AND is_completed = FALSE`, habitID, today); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM habits WHERE id = $1 AND user_id = $2`, habitID, userID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listHabits(ctx context.Context, userID string) ([]habitResponse, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT h.id, h.title, h.color, h.schedule_type, h.interval_days,
               COALESCE(array_agg(DISTINCT hw.weekday ORDER BY hw.weekday), ARRAY[]::integer[]) AS weekdays,
               CASE WHEN p.id IS NOT NULL THEN TRUE ELSE FALSE END AS is_shared,
               ou.id, ou.handle
        FROM habits h
        LEFT JOIN habit_weekdays hw ON hw.habit_id = h.id
        LEFT JOIN shared_habit_pairs p ON p.habit1_id = h.id OR p.habit2_id = h.id
        LEFT JOIN habits other_h ON other_h.id = CASE WHEN p.habit1_id = h.id THEN p.habit2_id ELSE p.habit1_id END
        LEFT JOIN users ou ON ou.id = other_h.user_id
        WHERE h.user_id = $1
        GROUP BY h.id, p.id, ou.id, ou.handle
        ORDER BY h.created_at ASC
    `, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]habitResponse, 0)
	for rows.Next() {
		var row habitRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Color, &row.ScheduleType, &row.IntervalDays, &row.Weekdays, &row.IsShared, &row.SharedWithID, &row.SharedHandle); err != nil {
			return nil, err
		}
		streak, err := h.calculateHabitStreak(ctx, row.ID, habitScheduleInfo{ScheduleType: row.ScheduleType, IntervalDays: row.IntervalDays, Weekdays: row.Weekdays})
		if err != nil {
			return nil, err
		}
		result = append(result, habitResponse{
			ID:           row.ID,
			Title:        row.Title,
			Color:        row.Color,
			ScheduleType: row.ScheduleType,
			IntervalDays: row.IntervalDays,
			Weekdays:     row.Weekdays,
			IsShared:     row.IsShared,
			SharedWith:   buildSharedWith(row.SharedWithID, row.SharedHandle),
			StreakDays:   streak,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *Handler) getHabitByID(ctx context.Context, habitID, userID string) (habitResponse, error) {
	row, err := h.getHabitRow(ctx, h.pool, habitID, userID)
	if err != nil {
		return habitResponse{}, err
	}
	streak, err := h.calculateHabitStreak(ctx, row.ID, habitScheduleInfo{ScheduleType: row.ScheduleType, IntervalDays: row.IntervalDays, Weekdays: row.Weekdays})
	if err != nil {
		return habitResponse{}, err
	}
	return habitResponse{
		ID:           row.ID,
		Title:        row.Title,
		Color:        row.Color,
		ScheduleType: row.ScheduleType,
		IntervalDays: row.IntervalDays,
		Weekdays:     row.Weekdays,
		IsShared:     row.IsShared,
		SharedWith:   buildSharedWith(row.SharedWithID, row.SharedHandle),
		StreakDays:   streak,
	}, nil
}

func (h *Handler) getHabitRow(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, habitID, userID string) (habitRow, error) {
	row := habitRow{}
	err := querier.QueryRow(ctx, `
        SELECT h.id, h.title, h.color, h.schedule_type, h.interval_days,
               COALESCE(array_agg(DISTINCT hw.weekday ORDER BY hw.weekday), ARRAY[]::integer[]) AS weekdays,
               CASE WHEN p.id IS NOT NULL THEN TRUE ELSE FALSE END AS is_shared,
               ou.id, ou.handle
        FROM habits h
        LEFT JOIN habit_weekdays hw ON hw.habit_id = h.id
        LEFT JOIN shared_habit_pairs p ON p.habit1_id = h.id OR p.habit2_id = h.id
        LEFT JOIN habits other_h ON other_h.id = CASE WHEN p.habit1_id = h.id THEN p.habit2_id ELSE p.habit1_id END
        LEFT JOIN users ou ON ou.id = other_h.user_id
        WHERE h.id = $1 AND h.user_id = $2
        GROUP BY h.id, p.id, ou.id, ou.handle
    `, habitID, userID).Scan(&row.ID, &row.Title, &row.Color, &row.ScheduleType, &row.IntervalDays, &row.Weekdays, &row.IsShared, &row.SharedWithID, &row.SharedHandle)
	if err != nil {
		return habitRow{}, err
	}
	return row, nil
}

func (h *Handler) buildCalendarSummary(ctx context.Context, userID string, from, to time.Time) (calendarSummaryResponse, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT t.task_date, t.habit_id, h.color
        FROM tasks t
        JOIN habits h ON h.id = t.habit_id
        WHERE t.user_id = $1 AND t.is_completed = TRUE AND t.task_date BETWEEN $2 AND $3
        ORDER BY t.task_date ASC
    `, userID, from, to)
	if err != nil {
		return calendarSummaryResponse{}, err
	}
	defer rows.Close()

	daysMap := map[string]map[string]calendarHabitInfo{}
	for rows.Next() {
		var date time.Time
		var habitID string
		var color string
		if err := rows.Scan(&date, &habitID, &color); err != nil {
			return calendarSummaryResponse{}, err
		}
		key := date.Format("2006-01-02")
		if _, ok := daysMap[key]; !ok {
			daysMap[key] = map[string]calendarHabitInfo{}
		}
		daysMap[key][habitID] = calendarHabitInfo{HabitID: habitID, Color: color}
	}
	if err := rows.Err(); err != nil {
		return calendarSummaryResponse{}, err
	}

	dates := make([]string, 0, len(daysMap))
	for k := range daysMap {
		dates = append(dates, k)
	}
	sort.Strings(dates)

	days := make([]calendarSummaryDay, 0, len(dates))
	for _, dateKey := range dates {
		dayInfo := calendarSummaryDay{Date: dateKey}
		for _, habit := range daysMap[dateKey] {
			dayInfo.CompletedHabits = append(dayInfo.CompletedHabits, habit)
		}
		sort.Slice(dayInfo.CompletedHabits, func(i, j int) bool {
			return dayInfo.CompletedHabits[i].HabitID < dayInfo.CompletedHabits[j].HabitID
		})
		days = append(days, dayInfo)
	}
	return calendarSummaryResponse{Days: days}, nil
}

func validateHabitPayload(title, color, scheduleType string, intervalDays int, weekdays []int) error {
	title = strings.TrimSpace(title)
	color = strings.TrimSpace(color)
	if title == "" {
		return errors.New("Пустой title")
	}
	if color == "" {
		return errors.New("Пустой color")
	}
	if _, ok := validHabitScheduleTypes[scheduleType]; !ok {
		return errors.New("Некорректный scheduleType")
	}
	if intervalDays < 1 {
		return errors.New("intervalDays должен быть >= 1")
	}
	if scheduleType == "weekdays" {
		if len(weekdays) == 0 {
			return errors.New("weekdays обязателен для scheduleType=weekdays")
		}
	} else if len(weekdays) > 0 {
		return errors.New("weekdays должен быть пустым для этого scheduleType")
	}
	return validateWeekdays(weekdays)
}

func validateWeekdays(weekdays []int) error {
	seen := make(map[int]struct{}, len(weekdays))
	for _, weekday := range weekdays {
		if weekday < 1 || weekday > 7 {
			return errors.New("weekdays должны быть в диапазоне 1..7")
		}
		if _, ok := seen[weekday]; ok {
			return errors.New("дублирование значения в weekdays")
		}
		seen[weekday] = struct{}{}
	}
	return nil
}

func insertHabitWeekdays(ctx context.Context, tx pgx.Tx, habitID string, weekdays []int) error {
	for _, weekday := range weekdays {
		if _, err := tx.Exec(ctx, `INSERT INTO habit_weekdays (habit_id, weekday) VALUES ($1, $2)`, habitID, weekday); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) calculateHabitStreak(ctx context.Context, habitID string, schedule habitScheduleInfo) (int, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT task_date
        FROM tasks
        WHERE habit_id = $1 AND is_completed = TRUE AND task_date <= $2
        ORDER BY task_date ASC
    `, habitID, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	completed := map[string]struct{}{}
	var lastDate time.Time
	for rows.Next() {
		var taskDate time.Time
		if err := rows.Scan(&taskDate); err != nil {
			return 0, err
		}
		completed[taskDate.Format("2006-01-02")] = struct{}{}
		if taskDate.After(lastDate) {
			lastDate = taskDate
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if lastDate.IsZero() {
		return 0, nil
	}

	count := 0
	current := lastDate
	for {
		if _, ok := completed[current.Format("2006-01-02")]; !ok {
			break
		}
		count++
		prev := previousHabitDate(current, schedule)
		if prev.IsZero() {
			break
		}
		current = prev
	}
	return count, nil
}

func previousHabitDate(current time.Time, schedule habitScheduleInfo) time.Time {
	switch schedule.ScheduleType {
	case "daily":
		return current.AddDate(0, 0, -1)
	case "interval":
		return current.AddDate(0, 0, -schedule.IntervalDays)
	case "weekdays":
		if len(schedule.Weekdays) == 0 {
			return time.Time{}
		}
		weekdaySet := make(map[int]struct{}, len(schedule.Weekdays))
		for _, w := range schedule.Weekdays {
			weekdaySet[w] = struct{}{}
		}
		next := current.AddDate(0, 0, -1)
		for i := 0; i < 7; i++ {
			weekday := int(next.Weekday())
			if weekday == 0 {
				weekday = 7
			}
			if _, ok := weekdaySet[weekday]; ok {
				return next
			}
			next = next.AddDate(0, 0, -1)
		}
		return time.Time{}
	default:
		return time.Time{}
	}
}

func buildSharedWith(id *string, handle *string) *publicUser {
	if id == nil || handle == nil {
		return nil
	}
	return &publicUser{ID: *id, Handle: *handle}
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

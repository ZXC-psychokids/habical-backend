package sharedhabits

import (
	"context"
	"errors"
	"net/http"
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

type habitShort struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Color string `json:"color"`
}

type createSharedHabitRequest struct {
	FriendUserID string `json:"friendUserId"`
	Title        string `json:"title"`
	Color        string `json:"color"`
	ScheduleType string `json:"scheduleType"`
	IntervalDays int    `json:"intervalDays"`
	Weekdays     []int  `json:"weekdays"`
}

type createSharedHabitResponse struct {
	SharedHabitPairID string     `json:"sharedHabitPairId"`
	FirstHabit        habitShort `json:"firstHabit"`
	SecondHabit       habitShort `json:"secondHabit"`
}

type sharedHabitDetails struct {
	SharedHabitPairID    string     `json:"sharedHabitPairId"`
	Title                string     `json:"title"`
	Color                string     `json:"color"`
	StreakDays           int        `json:"streakDays"`
	YouCompletedToday    bool       `json:"youCompletedToday"`
	FriendCompletedToday bool       `json:"friendCompletedToday"`
	You                  publicUser `json:"you"`
	Friend               publicUser `json:"friend"`
}

type remindSharedHabitRequest struct {
	TaskID string `json:"taskId"`
}

type habitScheduleInfo struct {
	ScheduleType string
	IntervalDays int
	Weekdays     []int
}

type sharedPairRow struct {
	PairID             string
	Habit1ID           string
	Habit1UserID       string
	Habit1Title        string
	Habit1Color        string
	Habit1ScheduleType string
	Habit1IntervalDays int
	Habit1Weekdays     []int
	User1ID            string
	User1Handle        string
	Habit2ID           string
	Habit2UserID       string
	Habit2Title        string
	Habit2Color        string
	Habit2ScheduleType string
	Habit2IntervalDays int
	Habit2Weekdays     []int
	User2ID            string
	User2Handle        string
}

func New(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/me/shared-habits", h.handleCreateSharedHabit)
	r.Get("/me/shared-habits/{sharedHabitPairId}", h.handleGetSharedHabit)
	r.Post("/me/shared-habits/{sharedHabitPairId}/remind", h.handleRemindSharedHabit)
}

func (h *Handler) handleCreateSharedHabit(w http.ResponseWriter, r *http.Request) {
	currentUserID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	var req createSharedHabitRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.FriendUserID == "" || req.FriendUserID == currentUserID {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный friendUserId")
		return
	}
	if err := validateHabitPayload(req.Title, req.Color, req.ScheduleType, req.IntervalDays, req.Weekdays); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var friendHandle string
	if err := h.pool.QueryRow(r.Context(), `SELECT handle FROM users WHERE id = $1`, req.FriendUserID).Scan(&friendHandle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Друг не найден")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	if !h.hasFriendship(r.Context(), currentUserID, req.FriendUserID) {
		httpx.WriteError(w, http.StatusForbidden, "Пользователь не является вашим другом")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	firstHabitID := idgen.New()
	secondHabitID := idgen.New()
	pairID := idgen.New()

	if _, err := tx.Exec(r.Context(), `
        INSERT INTO habits (id, user_id, title, color, schedule_type, interval_days, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
    `, firstHabitID, currentUserID, req.Title, req.Color, req.ScheduleType, req.IntervalDays, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `
        INSERT INTO habits (id, user_id, title, color, schedule_type, interval_days, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
    `, secondHabitID, req.FriendUserID, req.Title, req.Color, req.ScheduleType, req.IntervalDays, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if len(req.Weekdays) > 0 {
		if err := insertHabitWeekdays(r.Context(), tx, firstHabitID, req.Weekdays); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if err := insertHabitWeekdays(r.Context(), tx, secondHabitID, req.Weekdays); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `
        INSERT INTO shared_habit_pairs (id, habit1_id, habit2_id, created_at)
        VALUES ($1, $2, $3, $4)
    `, pairID, firstHabitID, secondHabitID, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, createSharedHabitResponse{
		SharedHabitPairID: pairID,
		FirstHabit:        habitShort{ID: firstHabitID, Title: req.Title, Color: req.Color},
		SecondHabit:       habitShort{ID: secondHabitID, Title: req.Title, Color: req.Color},
	})
}

func (h *Handler) handleGetSharedHabit(w http.ResponseWriter, r *http.Request) {
	currentUserID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	pairID := chi.URLParam(r, "sharedHabitPairId")
	if strings.TrimSpace(pairID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой sharedHabitPairId")
		return
	}
	row, err := h.getSharedPair(r.Context(), pairID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Shared habit не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	currentIsFirst := row.Habit1UserID == currentUserID
	if !currentIsFirst && row.Habit2UserID != currentUserID {
		httpx.WriteError(w, http.StatusForbidden, "Нет доступа к shared habit")
		return
	}
	youHabitID := row.Habit1ID
	friendHabitID := row.Habit2ID
	youUser := publicUser{ID: row.User1ID, Handle: row.User1Handle}
	friendUser := publicUser{ID: row.User2ID, Handle: row.User2Handle}
	title := row.Habit1Title
	color := row.Habit1Color
	if !currentIsFirst {
		youHabitID = row.Habit2ID
		friendHabitID = row.Habit1ID
		youUser = publicUser{ID: row.User2ID, Handle: row.User2Handle}
		friendUser = publicUser{ID: row.User1ID, Handle: row.User1Handle}
		title = row.Habit2Title
		color = row.Habit2Color
	}

	youCompleted, friendCompleted, err := h.getSharedHabitCompletionToday(r.Context(), youHabitID, friendHabitID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	streak, err := h.calculateSharedHabitStreak(r.Context(), sharedHabitScheduleInfo(row), youHabitID, friendHabitID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, sharedHabitDetails{
		SharedHabitPairID:    pairID,
		Title:                title,
		Color:                color,
		StreakDays:           streak,
		YouCompletedToday:    youCompleted,
		FriendCompletedToday: friendCompleted,
		You:                  youUser,
		Friend:               friendUser,
	})
}

func (h *Handler) handleRemindSharedHabit(w http.ResponseWriter, r *http.Request) {
	currentUserID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	pairID := chi.URLParam(r, "sharedHabitPairId")
	if strings.TrimSpace(pairID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой sharedHabitPairId")
		return
	}
	var req remindSharedHabitRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	if req.TaskID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой taskId")
		return
	}

	row, err := h.getSharedPair(r.Context(), pairID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Shared habit не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if currentUserID != row.Habit1UserID && currentUserID != row.Habit2UserID {
		httpx.WriteError(w, http.StatusForbidden, "Нет доступа к shared habit")
		return
	}

	taskUserID, taskHabitID, err := h.getTaskOwnerAndHabit(r.Context(), req.TaskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Задача не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if taskHabitID != row.Habit1ID && taskHabitID != row.Habit2ID {
		httpx.WriteError(w, http.StatusBadRequest, "taskId не принадлежит этой shared habit")
		return
	}

	recipientID := row.Habit1UserID
	if currentUserID == recipientID {
		recipientID = row.Habit2UserID
	}
	if taskUserID != currentUserID && taskUserID != recipientID {
		httpx.WriteError(w, http.StatusBadRequest, "taskId не принадлежит участникам этой shared habit")
		return
	}

	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := startOfDay.AddDate(0, 0, 1)
	exists := false
	if err := h.pool.QueryRow(r.Context(), `
        SELECT EXISTS(
            SELECT 1 FROM shared_habit_reminders
            WHERE task_id = $1 AND created_at >= $2 AND created_at < $3
        )
    `, req.TaskID, startOfDay, tomorrow).Scan(&exists); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if exists {
		httpx.WriteError(w, http.StatusConflict, "Напоминание уже отправлено сегодня")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(), `
        INSERT INTO shared_habit_reminders (id, shared_habit_pair_id, sender_user_id, recipient_user_id, task_id, created_at)
        VALUES ($1, $2, $3, $4, $5, $6)
    `, idgen.New(), pairID, currentUserID, recipientID, req.TaskID, now); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `
        INSERT INTO feed_items (id, recipient_user_id, actor_user_id, type, related_user_id, related_habit_id, streak_value, created_at)
        VALUES ($1, $2, $3, 'shared_habit_reminder', NULL, NULL, NULL, $4)
    `, idgen.New(), recipientID, currentUserID, now); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"message": "Напоминание отправлено"})
}

func (h *Handler) hasFriendship(ctx context.Context, userA, userB string) bool {
	var exists bool
	_ = h.pool.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM friendships
            WHERE (user1_id = $1 AND user2_id = $2) OR (user1_id = $2 AND user2_id = $1)
        )
    `, userA, userB).Scan(&exists)
	return exists
}

func (h *Handler) getSharedPair(ctx context.Context, pairID string) (sharedPairRow, error) {
	row := sharedPairRow{}
	err := h.pool.QueryRow(ctx, `
        SELECT p.id,
               h1.id, h1.user_id, h1.title, h1.color, h1.schedule_type, h1.interval_days,
                      COALESCE(array_agg(DISTINCT hw1.weekday ORDER BY hw1.weekday), ARRAY[]::integer[]) AS weekdays1,
               u1.id, u1.handle,
               h2.id, h2.user_id, h2.title, h2.color, h2.schedule_type, h2.interval_days,
                      COALESCE(array_agg(DISTINCT hw2.weekday ORDER BY hw2.weekday), ARRAY[]::integer[]) AS weekdays2,
               u2.id, u2.handle
        FROM shared_habit_pairs p
        JOIN habits h1 ON h1.id = p.habit1_id
        LEFT JOIN habit_weekdays hw1 ON hw1.habit_id = h1.id
        JOIN users u1 ON u1.id = h1.user_id
        JOIN habits h2 ON h2.id = p.habit2_id
        LEFT JOIN habit_weekdays hw2 ON hw2.habit_id = h2.id
        JOIN users u2 ON u2.id = h2.user_id
        WHERE p.id = $1
        GROUP BY p.id, h1.id, h1.user_id, h1.title, h1.color, h1.schedule_type, h1.interval_days, u1.id, u1.handle,
                 h2.id, h2.user_id, h2.title, h2.color, h2.schedule_type, h2.interval_days, u2.id, u2.handle
    `, pairID).Scan(
		&row.PairID,
		&row.Habit1ID, &row.Habit1UserID, &row.Habit1Title, &row.Habit1Color, &row.Habit1ScheduleType, &row.Habit1IntervalDays, &row.Habit1Weekdays,
		&row.User1ID, &row.User1Handle,
		&row.Habit2ID, &row.Habit2UserID, &row.Habit2Title, &row.Habit2Color, &row.Habit2ScheduleType, &row.Habit2IntervalDays, &row.Habit2Weekdays,
		&row.User2ID, &row.User2Handle,
	)
	if err != nil {
		return sharedPairRow{}, err
	}
	return row, nil
}

func (h *Handler) getTaskOwnerAndHabit(ctx context.Context, taskID string) (userID, habitID string, err error) {
	err = h.pool.QueryRow(ctx, `SELECT user_id, habit_id FROM tasks WHERE id = $1`, taskID).Scan(&userID, &habitID)
	return
}

func (h *Handler) getSharedHabitCompletionToday(ctx context.Context, youHabitID, friendHabitID string) (bool, bool, error) {
	var youCompleted, friendCompleted bool
	rows, err := h.pool.Query(ctx, `
        SELECT habit_id
        FROM tasks
        WHERE task_date = $1 AND is_completed = TRUE AND habit_id = ANY($2::uuid[])
    `, time.Now().UTC().Truncate(24*time.Hour), []string{youHabitID, friendHabitID})
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var habitID string
		if err := rows.Scan(&habitID); err != nil {
			return false, false, err
		}
		switch habitID {
		case youHabitID:
			youCompleted = true
		case friendHabitID:
			friendCompleted = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, err
	}
	return youCompleted, friendCompleted, nil
}

func sharedHabitScheduleInfo(row sharedPairRow) habitScheduleInfo {
	if row.Habit1ScheduleType == "weekdays" {
		return habitScheduleInfo{ScheduleType: row.Habit1ScheduleType, IntervalDays: row.Habit1IntervalDays, Weekdays: row.Habit1Weekdays}
	}
	return habitScheduleInfo{ScheduleType: row.Habit1ScheduleType, IntervalDays: row.Habit1IntervalDays, Weekdays: row.Habit1Weekdays}
}

func (h *Handler) calculateSharedHabitStreak(ctx context.Context, schedule habitScheduleInfo, youHabitID, friendHabitID string) (int, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT task_date, habit_id
        FROM tasks
        WHERE habit_id = ANY($1::uuid[]) AND is_completed = TRUE AND task_date <= $2
        ORDER BY task_date ASC
    `, []string{youHabitID, friendHabitID}, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	youCompleted := map[string]struct{}{}
	friendCompleted := map[string]struct{}{}
	var lastDate time.Time
	for rows.Next() {
		var taskDate time.Time
		var habitID string
		if err := rows.Scan(&taskDate, &habitID); err != nil {
			return 0, err
		}
		key := taskDate.Format("2006-01-02")
		if habitID == youHabitID {
			youCompleted[key] = struct{}{}
		} else if habitID == friendHabitID {
			friendCompleted[key] = struct{}{}
		}
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
		key := current.Format("2006-01-02")
		if _, ok := youCompleted[key]; !ok {
			break
		}
		if _, ok := friendCompleted[key]; !ok {
			break
		}
		count++
		prev := previousSharedDate(current, schedule)
		if prev.IsZero() {
			break
		}
		current = prev
	}
	return count, nil
}

func previousSharedDate(current time.Time, schedule habitScheduleInfo) time.Time {
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

func userIDFromContext(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(userIDContextKey).(string)
	return raw, ok
}

package friendpage

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"habical/backend/libs/httpx"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const userIDContextKey = "user_id"

type Handler struct {
	pool *pgxpool.Pool
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

type eventTimeLink struct {
	ID       string    `json:"id"`
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}

type taskResponse struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	IsCompleted bool           `json:"isCompleted"`
	Habit       *habitShort    `json:"habit"`
	Event       *eventTimeLink `json:"event"`
}

type sharedHabitListItem struct {
	SharedHabitPairID    string `json:"sharedHabitPairId"`
	HabitID              string `json:"habitId"`
	Title                string `json:"title"`
	Color                string `json:"color"`
	StreakDays           int    `json:"streakDays"`
	YouCompletedToday    bool   `json:"youCompletedToday"`
	FriendCompletedToday bool   `json:"friendCompletedToday"`
}

type sharedHabitPairRow struct {
	PairID        string
	YouHabitID    string
	FriendHabitID string
	Title         string
	Color         string
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
	r.Get("/users/{userId}/tasks", h.handleGetFriendTasks)
	r.Get("/users/{userId}/shared-habits", h.handleGetFriendSharedHabits)
}

func (h *Handler) handleGetFriendTasks(w http.ResponseWriter, r *http.Request) {
	currentUserID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	targetUserID := chi.URLParam(r, "userId")
	if strings.TrimSpace(targetUserID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой userId")
		return
	}
	date, err := parseDateQuery(r, "date")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный date")
		return
	}

	if currentUserID != targetUserID {
		allowed, err := h.canSeeFriendTasks(r.Context(), currentUserID, targetUserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, http.StatusNotFound, "Пользователь не найден")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if !allowed {
			httpx.WriteError(w, http.StatusForbidden, "Нет дружбы или задачи скрыты")
			return
		}
	}

	tasks, err := h.listFriendTasks(r.Context(), targetUserID, date)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tasks)
}

func (h *Handler) handleGetFriendSharedHabits(w http.ResponseWriter, r *http.Request) {
	currentUserID, ok := userIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
		return
	}
	targetUserID := chi.URLParam(r, "userId")
	if strings.TrimSpace(targetUserID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой userId")
		return
	}
	if currentUserID != targetUserID {
		friend, err := h.hasFriendship(r.Context(), currentUserID, targetUserID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if !friend {
			httpx.WriteError(w, http.StatusForbidden, "Нет дружбы")
			return
		}
	}

	habits, err := h.listFriendSharedHabits(r.Context(), currentUserID, targetUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, habits)
}

func (h *Handler) canSeeFriendTasks(ctx context.Context, currentUserID, targetUserID string) (bool, error) {
	var shareHabits bool
	err := h.pool.QueryRow(ctx, `
        SELECT COALESCE(us.share_habits, FALSE)
        FROM users u
        LEFT JOIN user_settings us ON us.user_id = u.id
        WHERE u.id = $1
    `, targetUserID).Scan(&shareHabits)
	if err != nil {
		return false, err
	}
	if !shareHabits {
		return false, nil
	}
	friend, err := h.hasFriendship(ctx, currentUserID, targetUserID)
	if err != nil {
		return false, err
	}
	return friend, nil
}

func (h *Handler) hasFriendship(ctx context.Context, userA, userB string) (bool, error) {
	var exists bool
	err := h.pool.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM friendships
            WHERE (user1_id = $1 AND user2_id = $2) OR (user1_id = $2 AND user2_id = $1)
        )
    `, userA, userB).Scan(&exists)
	return exists, err
}

func (h *Handler) listFriendTasks(ctx context.Context, userID string, date time.Time) ([]taskResponse, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT t.id, t.title, t.is_completed,
               h.id, h.title, h.color,
               e.id, e.starts_at, e.ends_at
        FROM tasks t
        LEFT JOIN habits h ON h.id = t.habit_id
        LEFT JOIN events e ON e.task_id = t.id
        WHERE t.user_id = $1 AND t.task_date = $2
        ORDER BY CASE WHEN e.starts_at IS NOT NULL THEN 0 ELSE 1 END, e.starts_at ASC NULLS LAST, t.position ASC
    `, userID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]taskResponse, 0)
	for rows.Next() {
		var item taskResponse
		var habitID *string
		var habitTitle *string
		var habitColor *string
		var eventID *string
		var startsAt *time.Time
		var endsAt *time.Time
		if err := rows.Scan(&item.ID, &item.Title, &item.IsCompleted, &habitID, &habitTitle, &habitColor, &eventID, &startsAt, &endsAt); err != nil {
			return nil, err
		}
		if habitID != nil {
			item.Habit = &habitShort{ID: *habitID, Title: *habitTitle, Color: *habitColor}
		}
		if eventID != nil {
			item.Event = &eventTimeLink{ID: *eventID, StartsAt: *startsAt, EndsAt: *endsAt}
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *Handler) listFriendSharedHabits(ctx context.Context, currentUserID, targetUserID string) ([]sharedHabitListItem, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT p.id,
               CASE WHEN h1.user_id = $1 THEN h1.id ELSE h2.id END AS you_habit_id,
               CASE WHEN h1.user_id = $1 THEN h2.id ELSE h1.id END AS friend_habit_id,
               CASE WHEN h1.user_id = $1 THEN h1.title ELSE h2.title END AS title,
               CASE WHEN h1.user_id = $1 THEN h1.color ELSE h2.color END AS color
        FROM shared_habit_pairs p
        JOIN habits h1 ON h1.id = p.habit1_id
        JOIN habits h2 ON h2.id = p.habit2_id
        WHERE (h1.user_id = $1 AND h2.user_id = $2) OR (h1.user_id = $2 AND h2.user_id = $1)
    `, currentUserID, targetUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]sharedHabitListItem, 0)
	for rows.Next() {
		var row sharedHabitPairRow
		if err := rows.Scan(&row.PairID, &row.YouHabitID, &row.FriendHabitID, &row.Title, &row.Color); err != nil {
			return nil, err
		}
		youToday, friendToday, err := h.getSharedHabitCompletionToday(ctx, row.YouHabitID, row.FriendHabitID)
		if err != nil {
			return nil, err
		}
		streak, err := h.calculateSharedHabitStreak(ctx, row.YouHabitID, row.FriendHabitID)
		if err != nil {
			return nil, err
		}
		result = append(result, sharedHabitListItem{
			SharedHabitPairID:    row.PairID,
			HabitID:              row.YouHabitID,
			Title:                row.Title,
			Color:                row.Color,
			StreakDays:           streak,
			YouCompletedToday:    youToday,
			FriendCompletedToday: friendToday,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].SharedHabitPairID < result[j].SharedHabitPairID
	})
	return result, nil
}

func (h *Handler) getSharedHabitCompletionToday(ctx context.Context, youHabitID, friendHabitID string) (bool, bool, error) {
	rows, err := h.pool.Query(ctx, `
        SELECT habit_id
        FROM tasks
        WHERE task_date = $1 AND is_completed = TRUE AND habit_id = ANY($2::uuid[])
    `, time.Now().UTC().Truncate(24*time.Hour), []string{youHabitID, friendHabitID})
	if err != nil {
		return false, false, err
	}
	defer rows.Close()

	youCompleted := false
	friendCompleted := false
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

func (h *Handler) calculateSharedHabitStreak(ctx context.Context, youHabitID, friendHabitID string) (int, error) {
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
		current = current.AddDate(0, 0, -1)
	}
	return count, nil
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

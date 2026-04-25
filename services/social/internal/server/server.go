package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/httpx"
	"habical/backend/libs/idgen"
	"habical/backend/services/social/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

	r.Get("/me/friends", s.handleListFriends)
	r.Delete("/me/friends/{friendUserId}", s.handleDeleteFriend)

	r.Get("/me/friend-invites", s.handleListInvites)
	r.Post("/me/friend-invites", s.handleCreateInvite)
	r.Post("/me/friend-invites/{inviteId}/accept", s.handleAcceptInvite)
	r.Post("/me/friend-invites/{inviteId}/reject", s.handleRejectInvite)

	r.Get("/me/feed", s.handleGetFeed)

	r.Get("/internal/friendships/{userId}/with/{otherUserId}", s.handleInternalFriendshipCheck)
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

type publicUser struct {
	ID        string `json:"id"`
	Handle    string `json:"handle"`
	AvatarURL string `json:"avatarUrl"`
}

type friendListItem struct {
	User              publicUser `json:"user"`
	SharedHabitsCount int        `json:"sharedHabitsCount"`
}

func (s *Server) handleListFriends(w http.ResponseWriter, r *http.Request) {
	currentUserID := userIDFromContext(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, user1_id, user2_id
		FROM friendships
		WHERE user1_id = $1 OR user2_id = $1
	`, currentUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer rows.Close()

	result := make([]friendListItem, 0)
	for rows.Next() {
		var friendshipID string
		var u1 string
		var u2 string
		if err := rows.Scan(&friendshipID, &u1, &u2); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		friendID := u1
		if u1 == currentUserID {
			friendID = u2
		}
		var friend publicUser
		if err := s.pool.QueryRow(r.Context(), `
			SELECT id, handle, avatar_url FROM users WHERE id = $1
		`, friendID).Scan(&friend.ID, &friend.Handle, &friend.AvatarURL); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		var sharedCount int
		if err := s.pool.QueryRow(r.Context(), `
			SELECT COUNT(*)
			FROM shared_habit_pairs shp
			JOIN habits h1 ON h1.id = shp.habit1_id
			JOIN habits h2 ON h2.id = shp.habit2_id
			WHERE (h1.user_id = $1 AND h2.user_id = $2)
			   OR (h1.user_id = $2 AND h2.user_id = $1)
		`, currentUserID, friendID).Scan(&sharedCount); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}

		result = append(result, friendListItem{
			User:              friend,
			SharedHabitsCount: sharedCount,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].SharedHabitsCount != result[j].SharedHabitsCount {
			return result[i].SharedHabitsCount > result[j].SharedHabitsCount
		}
		return strings.ToLower(result[i].User.Handle) < strings.ToLower(result[j].User.Handle)
	})

	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) handleDeleteFriend(w http.ResponseWriter, r *http.Request) {
	currentUserID := userIDFromContext(r.Context())
	friendUserID := chi.URLParam(r, "friendUserId")
	if strings.TrimSpace(friendUserID) == "" {
		httpx.WriteError(w, http.StatusNotFound, "Дружба не найдена")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `
		DELETE FROM friendships
		WHERE (user1_id = $1 AND user2_id = $2) OR (user1_id = $2 AND user2_id = $1)
	`, currentUserID, friendUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, http.StatusNotFound, "Дружба не найдена")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type friendInviteResponse struct {
	ID        string     `json:"id"`
	Sender    publicUser `json:"sender"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	currentUserID := userIDFromContext(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT fi.id, fi.status, fi.created_at, u.id, u.handle, u.avatar_url
		FROM friend_invites fi
		JOIN users u ON u.id = fi.sender_user_id
		WHERE fi.receiver_user_id = $1 AND fi.status = 'pending'
		ORDER BY fi.created_at DESC
	`, currentUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer rows.Close()
	result := make([]friendInviteResponse, 0)
	for rows.Next() {
		var item friendInviteResponse
		if err := rows.Scan(
			&item.ID, &item.Status, &item.CreatedAt,
			&item.Sender.ID, &item.Sender.Handle, &item.Sender.AvatarURL,
		); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		result = append(result, item)
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

type createInviteRequest struct {
	Handle string `json:"handle"`
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	currentUserID := userIDFromContext(r.Context())
	var req createInviteRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	handle := strings.TrimSpace(req.Handle)
	if handle == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой handle")
		return
	}

	var targetUserID string
	if err := s.pool.QueryRow(r.Context(), `
		SELECT id FROM users WHERE handle = $1
	`, handle).Scan(&targetUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Пользователь не найден")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if targetUserID == currentUserID {
		httpx.WriteError(w, http.StatusConflict, "Нельзя отправить заявку самому себе")
		return
	}

	isFriend, err := s.isFriendshipExists(r.Context(), currentUserID, targetUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if isFriend {
		httpx.WriteError(w, http.StatusConflict, "Пользователь уже друг")
		return
	}

	var pendingExists bool
	if err := s.pool.QueryRow(r.Context(), `
		SELECT EXISTS(
			SELECT 1
			FROM friend_invites
			WHERE status = 'pending'
			  AND (
				(sender_user_id = $1 AND receiver_user_id = $2)
				OR
				(sender_user_id = $2 AND receiver_user_id = $1)
			  )
		)
	`, currentUserID, targetUserID).Scan(&pendingExists); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if pendingExists {
		httpx.WriteError(w, http.StatusConflict, "Заявка уже существует")
		return
	}

	inviteID := idgen.New()
	if _, err := s.pool.Exec(r.Context(), `
		INSERT INTO friend_invites (id, sender_user_id, receiver_user_id, status, created_at)
		VALUES ($1,$2,$3,'pending',$4)
	`, inviteID, currentUserID, targetUserID, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":     inviteID,
		"status": "pending",
	})
}

func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	inviteID := chi.URLParam(r, "inviteId")
	currentUserID := userIDFromContext(r.Context())

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	var senderID string
	var receiverID string
	var status string
	err = tx.QueryRow(r.Context(), `
		SELECT sender_user_id, receiver_user_id, status
		FROM friend_invites
		WHERE id = $1
	`, inviteID).Scan(&senderID, &receiverID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Заявка не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if receiverID != currentUserID {
		httpx.WriteError(w, http.StatusForbidden, "Заявка не адресована текущему пользователю")
		return
	}
	if status != "pending" {
		httpx.WriteError(w, http.StatusConflict, "Заявка уже обработана")
		return
	}

	if _, err := tx.Exec(r.Context(), `
		UPDATE friend_invites SET status = 'accepted' WHERE id = $1
	`, inviteID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	u1, u2 := normalizePair(senderID, receiverID)
	friendshipID := idgen.New()
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO friendships (id, user1_id, user2_id, created_at)
		VALUES ($1,$2,$3,$4)
	`, friendshipID, u1, u2, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusConflict, "Пользователь уже друг")
		return
	}

	_, _ = tx.Exec(r.Context(), `
		INSERT INTO feed_items (id, recipient_user_id, actor_user_id, type, related_user_id, related_habit_id, streak_value, created_at)
		VALUES ($1,$2,$3,'friend_added',$4,NULL,NULL,$5)
	`, idgen.New(), senderID, receiverID, senderID, time.Now().UTC())

	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"friendshipId": friendshipID,
	})
}

func (s *Server) handleRejectInvite(w http.ResponseWriter, r *http.Request) {
	inviteID := chi.URLParam(r, "inviteId")
	currentUserID := userIDFromContext(r.Context())

	var receiverID string
	var status string
	err := s.pool.QueryRow(r.Context(), `
		SELECT receiver_user_id, status
		FROM friend_invites
		WHERE id = $1
	`, inviteID).Scan(&receiverID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Заявка не найдена")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if receiverID != currentUserID {
		httpx.WriteError(w, http.StatusForbidden, "Заявка не адресована текущему пользователю")
		return
	}
	if status != "pending" {
		httpx.WriteError(w, http.StatusConflict, "Заявка уже обработана")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `
		UPDATE friend_invites SET status = 'rejected' WHERE id = $1
	`, inviteID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type feedItemResponse struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	Actor        publicUser  `json:"actor"`
	RelatedUser  *publicUser `json:"relatedUser"`
	RelatedHabit *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Color string `json:"color"`
	} `json:"relatedHabit"`
	StreakValue *int      `json:"streakValue"`
	CreatedAt   time.Time `json:"createdAt"`
}

type feedResponse struct {
	Items      []feedItemResponse `json:"items"`
	NextCursor *string            `json:"nextCursor"`
}

func decodeCursor(cursor string) (int, error) {
	if strings.TrimSpace(cursor) == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0, errors.New("invalid cursor")
	}
	return n, nil
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func (s *Server) handleGetFeed(w http.ResponseWriter, r *http.Request) {
	currentUserID := userIDFromContext(r.Context())
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 100 {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный limit")
			return
		}
		limit = n
	}
	offset, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный cursor")
		return
	}

	rows, err := s.pool.Query(r.Context(), `
		SELECT
			fi.id, fi.type::text, fi.streak_value, fi.created_at,
			actor.id, actor.handle, actor.avatar_url,
			related_user.id, related_user.handle, related_user.avatar_url,
			related_habit.id, related_habit.title, related_habit.color
		FROM feed_items fi
		JOIN users actor ON actor.id = fi.actor_user_id
		LEFT JOIN users related_user ON related_user.id = fi.related_user_id
		LEFT JOIN habits related_habit ON related_habit.id = fi.related_habit_id
		WHERE fi.recipient_user_id = $1
		ORDER BY CASE WHEN fi.type = 'shared_habit_reminder' THEN 0 ELSE 1 END ASC,
		         fi.created_at DESC,
		         fi.id DESC
		LIMIT $2 OFFSET $3
	`, currentUserID, limit+1, offset)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer rows.Close()

	items := make([]feedItemResponse, 0, limit+1)
	for rows.Next() {
		var item feedItemResponse
		var relatedUserID *string
		var relatedUserHandle *string
		var relatedUserAvatar *string
		var relatedHabitID *string
		var relatedHabitTitle *string
		var relatedHabitColor *string
		if err := rows.Scan(
			&item.ID, &item.Type, &item.StreakValue, &item.CreatedAt,
			&item.Actor.ID, &item.Actor.Handle, &item.Actor.AvatarURL,
			&relatedUserID, &relatedUserHandle, &relatedUserAvatar,
			&relatedHabitID, &relatedHabitTitle, &relatedHabitColor,
		); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if relatedUserID != nil && relatedUserHandle != nil && relatedUserAvatar != nil {
			item.RelatedUser = &publicUser{
				ID:        *relatedUserID,
				Handle:    *relatedUserHandle,
				AvatarURL: *relatedUserAvatar,
			}
		}
		if relatedHabitID != nil && relatedHabitTitle != nil && relatedHabitColor != nil {
			item.RelatedHabit = &struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Color string `json:"color"`
			}{
				ID:    *relatedHabitID,
				Title: *relatedHabitTitle,
				Color: *relatedHabitColor,
			}
		}
		items = append(items, item)
	}

	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		value := encodeCursor(offset + limit)
		nextCursor = &value
	}
	httpx.WriteJSON(w, http.StatusOK, feedResponse{
		Items:      items,
		NextCursor: nextCursor,
	})
}

func (s *Server) handleInternalFriendshipCheck(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	otherUserID := chi.URLParam(r, "otherUserId")
	ok, err := s.isFriendshipExists(r.Context(), userID, otherUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"isFriend": ok})
}

func (s *Server) isFriendshipExists(ctx context.Context, userID string, otherUserID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM friendships
			WHERE (user1_id = $1 AND user2_id = $2)
			   OR (user1_id = $2 AND user2_id = $1)
		)
	`, userID, otherUserID).Scan(&exists)
	return exists, err
}

func normalizePair(a string, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

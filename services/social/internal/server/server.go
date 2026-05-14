package server

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/httpx"
	"habical/backend/libs/idgen"
	"habical/backend/libs/logger"
	"habical/backend/services/social/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	cfg  config.Config
	pool *pgxpool.Pool
	log  *slog.Logger

	listFriendsFn      func(ctx context.Context, userID string) ([]friendListItem, error)
	deleteFriendFn     func(ctx context.Context, userID string, friendUserID string) (bool, error)
	listInvitesFn      func(ctx context.Context, userID string) ([]friendInviteResponse, error)
	findUserByHandleFn func(ctx context.Context, handle string) (string, error)
	friendshipExistsFn func(ctx context.Context, userID string, otherUserID string) (bool, error)
	pendingInviteFn    func(ctx context.Context, userID string, otherUserID string) (bool, error)
	createInviteFn     func(ctx context.Context, senderID string, receiverID string) (string, error)
	acceptInviteFn     func(ctx context.Context, inviteID string, currentUserID string) (string, error)
	rejectInviteFn     func(ctx context.Context, inviteID string, currentUserID string) error
	getFeedFn          func(ctx context.Context, userID string, limit int, offset int) ([]feedItemResponse, error)
}

var (
	errNotFound  = errors.New("not found")
	errForbidden = errors.New("forbidden")
	errConflict  = errors.New("conflict")
)

func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, pool: pool, log: log}
	s.listFriendsFn = s.listFriendsDB
	s.deleteFriendFn = s.deleteFriendDB
	s.listInvitesFn = s.listInvitesDB
	s.findUserByHandleFn = s.findUserByHandleDB
	s.friendshipExistsFn = s.friendshipExistsDB
	s.pendingInviteFn = s.pendingInviteExistsDB
	s.createInviteFn = s.createInviteDB
	s.acceptInviteFn = s.acceptInviteDB
	s.rejectInviteFn = s.rejectInviteDB
	s.getFeedFn = s.getFeedDB
	return s
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(logger.RequestID)
	r.Use(logger.HTTPLogger(s.log))
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
			s.writeError(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		userID, err := authjwt.ParseAccessToken(token, s.cfg.JWTSecret)
		if err != nil {
			s.writeError(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, userID)))
	})
}

func userIDFromContext(ctx context.Context) string {
	raw, _ := ctx.Value(userIDKey).(string)
	return raw
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, message string, attrs ...any) {
	logAttrs := []any{"request_id", logger.RequestIDFromContext(r.Context()), "user_id", userIDFromContext(r.Context()), "path", r.URL.Path, "status", status, "error", message}
	logAttrs = append(logAttrs, attrs...)
	if status >= http.StatusInternalServerError {
		s.log.Error("handler_failed", logAttrs...)
	} else {
		s.log.Warn("handler_warning", logAttrs...)
	}
	httpx.WriteError(w, status, message)
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
	items, err := s.listFriendsFn(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (s *Server) handleDeleteFriend(w http.ResponseWriter, r *http.Request) {
	friendUserID := chi.URLParam(r, "friendUserId")
	if strings.TrimSpace(friendUserID) == "" {
		s.writeError(w, r, http.StatusNotFound, "friendship not found")
		return
	}
	ok, err := s.deleteFriendFn(r.Context(), userIDFromContext(r.Context()), friendUserID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "friendship not found")
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
	items, err := s.listInvitesFn(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

type createInviteRequest struct {
	Handle string `json:"handle"`
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	currentUserID := userIDFromContext(r.Context())
	var req createInviteRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid json")
		return
	}
	handle := strings.TrimSpace(req.Handle)
	if handle == "" {
		s.writeError(w, r, http.StatusBadRequest, "empty handle")
		return
	}
	targetUserID, err := s.findUserByHandleFn(r.Context(), handle)
	if err != nil {
		if errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusNotFound, "user not found")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if targetUserID == currentUserID {
		s.writeError(w, r, http.StatusBadRequest, "cannot invite yourself")
		return
	}
	isFriend, err := s.friendshipExistsFn(r.Context(), currentUserID, targetUserID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if isFriend {
		s.writeError(w, r, http.StatusConflict, "already friends")
		return
	}
	pending, err := s.pendingInviteFn(r.Context(), currentUserID, targetUserID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if pending {
		s.writeError(w, r, http.StatusConflict, "invite already exists")
		return
	}
	inviteID, err := s.createInviteFn(r.Context(), currentUserID, targetUserID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": inviteID, "status": "pending"})
}

func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	friendshipID, err := s.acceptInviteFn(r.Context(), chi.URLParam(r, "inviteId"), userIDFromContext(r.Context()))
	if err != nil {
		switch {
		case errors.Is(err, errNotFound):
			s.writeError(w, r, http.StatusNotFound, "invite not found")
		case errors.Is(err, errForbidden):
			s.writeError(w, r, http.StatusForbidden, "forbidden")
		case errors.Is(err, errConflict):
			s.writeError(w, r, http.StatusConflict, "invite already processed")
		default:
			s.writeError(w, r, http.StatusInternalServerError, "internal error")
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"friendshipId": friendshipID})
}

func (s *Server) handleRejectInvite(w http.ResponseWriter, r *http.Request) {
	err := s.rejectInviteFn(r.Context(), chi.URLParam(r, "inviteId"), userIDFromContext(r.Context()))
	if err != nil {
		switch {
		case errors.Is(err, errNotFound):
			s.writeError(w, r, http.StatusNotFound, "invite not found")
		case errors.Is(err, errForbidden):
			s.writeError(w, r, http.StatusForbidden, "forbidden")
		case errors.Is(err, errConflict):
			s.writeError(w, r, http.StatusConflict, "invite already processed")
		default:
			s.writeError(w, r, http.StatusInternalServerError, "internal error")
		}
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
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 100 {
			s.writeError(w, r, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	offset, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid cursor")
		return
	}
	items, err := s.getFeedFn(r.Context(), userIDFromContext(r.Context()), limit+1, offset)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		v := encodeCursor(offset + limit)
		nextCursor = &v
	}
	httpx.WriteJSON(w, http.StatusOK, feedResponse{Items: items, NextCursor: nextCursor})
}

func (s *Server) handleInternalFriendshipCheck(w http.ResponseWriter, r *http.Request) {
	ok, err := s.friendshipExistsFn(r.Context(), chi.URLParam(r, "userId"), chi.URLParam(r, "otherUserId"))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"isFriend": ok})
}

func (s *Server) isFriendshipExists(ctx context.Context, userID string, otherUserID string) (bool, error) {
	return s.friendshipExistsFn(ctx, userID, otherUserID)
}

func normalizePair(a string, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

func (s *Server) friendshipExistsDB(ctx context.Context, userID string, otherUserID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE (user1_id = $1 AND user2_id = $2) OR (user1_id = $2 AND user2_id = $1))`, userID, otherUserID).Scan(&exists)
	return exists, err
}

func (s *Server) listFriendsDB(ctx context.Context, currentUserID string) ([]friendListItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user1_id, user2_id FROM friendships WHERE user1_id = $1 OR user2_id = $1`, currentUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]friendListItem, 0)
	for rows.Next() {
		var fid, u1, u2 string
		if err := rows.Scan(&fid, &u1, &u2); err != nil {
			return nil, err
		}
		friendID := u1
		if u1 == currentUserID {
			friendID = u2
		}
		var friend publicUser
		if err := s.pool.QueryRow(ctx, `SELECT id, handle, avatar_url FROM users WHERE id = $1`, friendID).Scan(&friend.ID, &friend.Handle, &friend.AvatarURL); err != nil {
			return nil, err
		}
		var sharedCount int
		if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shared_habit_pairs shp JOIN habits h1 ON h1.id = shp.habit1_id JOIN habits h2 ON h2.id = shp.habit2_id WHERE (h1.user_id = $1 AND h2.user_id = $2) OR (h1.user_id = $2 AND h2.user_id = $1)`, currentUserID, friendID).Scan(&sharedCount); err != nil {
			return nil, err
		}
		result = append(result, friendListItem{User: friend, SharedHabitsCount: sharedCount})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SharedHabitsCount != result[j].SharedHabitsCount {
			return result[i].SharedHabitsCount > result[j].SharedHabitsCount
		}
		return strings.ToLower(result[i].User.Handle) < strings.ToLower(result[j].User.Handle)
	})
	return result, nil
}

func (s *Server) deleteFriendDB(ctx context.Context, userID string, friendUserID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM friendships WHERE (user1_id = $1 AND user2_id = $2) OR (user1_id = $2 AND user2_id = $1)`, userID, friendUserID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Server) listInvitesDB(ctx context.Context, userID string) ([]friendInviteResponse, error) {
	rows, err := s.pool.Query(ctx, `SELECT fi.id, fi.status, fi.created_at, u.id, u.handle, u.avatar_url FROM friend_invites fi JOIN users u ON u.id = fi.sender_user_id WHERE fi.receiver_user_id = $1 AND fi.status = 'pending' ORDER BY fi.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]friendInviteResponse, 0)
	for rows.Next() {
		var item friendInviteResponse
		if err := rows.Scan(&item.ID, &item.Status, &item.CreatedAt, &item.Sender.ID, &item.Sender.Handle, &item.Sender.AvatarURL); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Server) findUserByHandleDB(ctx context.Context, handle string) (string, error) {
	var targetUserID string
	if err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE handle = $1`, handle).Scan(&targetUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errNotFound
		}
		return "", err
	}
	return targetUserID, nil
}

func (s *Server) pendingInviteExistsDB(ctx context.Context, userID string, otherUserID string) (bool, error) {
	var pendingExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM friend_invites WHERE status = 'pending' AND ((sender_user_id = $1 AND receiver_user_id = $2) OR (sender_user_id = $2 AND receiver_user_id = $1)))`, userID, otherUserID).Scan(&pendingExists); err != nil {
		return false, err
	}
	return pendingExists, nil
}

func (s *Server) createInviteDB(ctx context.Context, senderID string, receiverID string) (string, error) {
	inviteID := idgen.New()
	_, err := s.pool.Exec(ctx, `INSERT INTO friend_invites (id, sender_user_id, receiver_user_id, status, created_at) VALUES ($1,$2,$3,'pending',$4)`, inviteID, senderID, receiverID, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return inviteID, nil
}

func (s *Server) acceptInviteDB(ctx context.Context, inviteID string, currentUserID string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var senderID, receiverID, status string
	err = tx.QueryRow(ctx, `SELECT sender_user_id, receiver_user_id, status FROM friend_invites WHERE id = $1`, inviteID).Scan(&senderID, &receiverID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errNotFound
		}
		return "", err
	}
	if receiverID != currentUserID {
		return "", errForbidden
	}
	if status != "pending" {
		return "", errConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE friend_invites SET status = 'accepted' WHERE id = $1`, inviteID); err != nil {
		return "", err
	}
	u1, u2 := normalizePair(senderID, receiverID)
	friendshipID := idgen.New()
	if _, err := tx.Exec(ctx, `INSERT INTO friendships (id, user1_id, user2_id, created_at) VALUES ($1,$2,$3,$4)`, friendshipID, u1, u2, time.Now().UTC()); err != nil {
		return "", errConflict
	}
	_, _ = tx.Exec(ctx, `INSERT INTO feed_items (id, recipient_user_id, actor_user_id, type, related_user_id, related_habit_id, streak_value, created_at) VALUES ($1,$2,$3,'friend_added',$4,NULL,NULL,$5)`, idgen.New(), senderID, receiverID, senderID, time.Now().UTC())
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return friendshipID, nil
}

func (s *Server) rejectInviteDB(ctx context.Context, inviteID string, currentUserID string) error {
	var receiverID, status string
	err := s.pool.QueryRow(ctx, `SELECT receiver_user_id, status FROM friend_invites WHERE id = $1`, inviteID).Scan(&receiverID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		return err
	}
	if receiverID != currentUserID {
		return errForbidden
	}
	if status != "pending" {
		return errConflict
	}
	_, err = s.pool.Exec(ctx, `UPDATE friend_invites SET status = 'rejected' WHERE id = $1`, inviteID)
	return err
}

func (s *Server) getFeedDB(ctx context.Context, userID string, limit int, offset int) ([]feedItemResponse, error) {
	rows, err := s.pool.Query(ctx, `SELECT fi.id, fi.type::text, fi.streak_value, fi.created_at, actor.id, actor.handle, actor.avatar_url, related_user.id, related_user.handle, related_user.avatar_url, related_habit.id, related_habit.title, related_habit.color FROM feed_items fi JOIN users actor ON actor.id = fi.actor_user_id LEFT JOIN users related_user ON related_user.id = fi.related_user_id LEFT JOIN habits related_habit ON related_habit.id = fi.related_habit_id WHERE fi.recipient_user_id = $1 ORDER BY CASE WHEN fi.type = 'shared_habit_reminder' THEN 0 ELSE 1 END ASC, fi.created_at DESC, fi.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]feedItemResponse, 0, limit)
	for rows.Next() {
		var item feedItemResponse
		var relatedUserID, relatedUserHandle, relatedUserAvatar *string
		var relatedHabitID, relatedHabitTitle, relatedHabitColor *string
		if err := rows.Scan(&item.ID, &item.Type, &item.StreakValue, &item.CreatedAt, &item.Actor.ID, &item.Actor.Handle, &item.Actor.AvatarURL, &relatedUserID, &relatedUserHandle, &relatedUserAvatar, &relatedHabitID, &relatedHabitTitle, &relatedHabitColor); err != nil {
			return nil, err
		}
		if relatedUserID != nil && relatedUserHandle != nil && relatedUserAvatar != nil {
			item.RelatedUser = &publicUser{ID: *relatedUserID, Handle: *relatedUserHandle, AvatarURL: *relatedUserAvatar}
		}
		if relatedHabitID != nil && relatedHabitTitle != nil && relatedHabitColor != nil {
			item.RelatedHabit = &struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Color string `json:"color"`
			}{ID: *relatedHabitID, Title: *relatedHabitTitle, Color: *relatedHabitColor}
		}
		items = append(items, item)
	}
	return items, nil
}

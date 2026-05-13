package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/httpx"
	"habical/backend/libs/idgen"
	"habical/backend/libs/logger"
	"habical/backend/libs/password"
	"habical/backend/services/auth/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	cfg  config.Config
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *Server {
	return &Server{cfg: cfg, pool: pool, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(logger.RequestID)
	r.Use(logger.HTTPLogger(s.log))

	r.Post("/auth/register", s.handleRegister)
	r.Post("/auth/login", s.handleLogin)
	r.Post("/auth/refresh", s.handleRefresh)
	r.Post("/auth/logout", s.handleLogout)
	r.Post("/auth/password-reset/request", s.handlePasswordResetRequest)
	r.Post("/auth/password-reset/confirm", s.handlePasswordResetConfirm)
	r.Handle(
		"/avatars/*",
		http.StripPrefix(
			"/avatars/",
			http.FileServer(http.Dir(s.cfg.AvatarDir)),
		),
	)

	r.Group(func(pr chi.Router) {
		pr.Use(s.authMiddleware)
		pr.Get("/me", s.handleGetMe)
		pr.Patch("/me/profile", s.handlePatchProfile)
		pr.Patch("/me/avatar", s.handlePatchAvatar)
		pr.Get("/me/settings", s.handleGetSettings)
		pr.Patch("/me/settings/privacy", s.handlePatchPrivacySettings)
		pr.Patch("/me/settings/notifications", s.handlePatchNotificationSettings)
		pr.Patch("/me/settings/calendar", s.handlePatchCalendarSettings)
	})

	// Internal endpoints for inter-service orchestration.
	r.Get("/internal/users/{userId}/public", s.handleInternalGetPublicUser)
	r.Get("/internal/users/{userId}/settings", s.handleInternalGetUserSettings)
	r.Get("/internal/users/by-handle/{handle}", s.handleInternalGetByHandle)

	return r
}

type ctxKey string

const userIDKey ctxKey = "user_id"

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := httpx.BearerToken(r)
		if !ok {
			s.writeError(w, r, http.StatusUnauthorized, "Р СњР ВµР В°Р Р†РЎвЂљР С•РЎР‚Р С‘Р В·Р С•Р Р†Р В°Р Р…")
			return
		}
		userID, err := authjwt.ParseAccessToken(token, s.cfg.JWTSecret)
		if err != nil {
			s.writeError(w, r, http.StatusUnauthorized, "Р СњР ВµР В°Р Р†РЎвЂљР С•РЎР‚Р С‘Р В·Р С•Р Р†Р В°Р Р…")
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

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, message string, attrs ...any) {
	logAttrs := []any{
		"request_id", logger.RequestIDFromContext(r.Context()),
		"user_id", userIDFromContext(r.Context()),
		"path", r.URL.Path,
		"status", status,
		"error", message,
	}
	logAttrs = append(logAttrs, attrs...)

	if status >= http.StatusInternalServerError {
		s.log.Error("handler_failed", logAttrs...)
	} else {
		s.log.Warn("handler_warning", logAttrs...)
	}

	httpx.WriteError(w, status, message)
}

type user struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Handle       string    `json:"handle"`
	AvatarURL    string    `json:"avatarUrl"`
	CreatedAt    time.Time `json:"createdAt"`
	PasswordHash string    `json:"-"`
}

type publicUser struct {
	ID        string `json:"id"`
	Handle    string `json:"handle"`
	AvatarURL string `json:"avatarUrl"`
}

type userSettings struct {
	Timezone             string `json:"timezone"`
	WeekStartsOn         int    `json:"weekStartsOn"`
	ShareHabits          bool   `json:"shareHabits"`
	ShareCalendar        bool   `json:"shareCalendar"`
	ShareNews            bool   `json:"shareNews"`
	NotifyFriendRequests bool   `json:"notifyFriendRequests"`
	NotifyHabitReminders bool   `json:"notifyHabitReminders"`
	NotifyFriendsNews    bool   `json:"notifyFriendsNews"`
}

type tokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

type authSuccessResponse struct {
	User     user         `json:"user"`
	Settings userSettings `json:"settings"`
	Tokens   tokenPair    `json:"tokens"`
}

type registerRequest struct {
	Email                string `json:"email"`
	Handle               string `json:"handle"`
	Password             string `json:"password"`
	PasswordConfirmation string `json:"passwordConfirmation"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Handle = strings.TrimSpace(req.Handle)
	if req.Email == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…Р В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°")
		return
	}
	if req.Handle == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– РЎвЂ¦Р ВµР Р…Р Т‘Р В»")
		return
	}
	if req.Password == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– Р С—Р В°РЎР‚Р С•Р В»РЎРЉ")
		return
	}
	if req.Password != req.PasswordConfirmation {
		s.writeError(w, r, http.StatusBadRequest, "Р СџР В°РЎР‚Р С•Р В»РЎРЉ Р С‘ Р С—Р С•Р Т‘РЎвЂљР Р†Р ВµРЎР‚Р В¶Р Т‘Р ВµР Р…Р С‘Р Вµ Р Р…Р Вµ РЎРѓР С•Р Р†Р С—Р В°Р Т‘Р В°РЎР‹РЎвЂљ")
		return
	}

	exists, err := s.existsByEmail(r.Context(), req.Email)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if exists {
		s.writeError(w, r, http.StatusConflict, "Р СџР С•РЎвЂЎРЎвЂљР В° РЎС“Р В¶Р Вµ Р В·Р В°Р Р…РЎРЏРЎвЂљР В°")
		return
	}
	exists, err = s.existsByHandle(r.Context(), req.Handle)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if exists {
		s.writeError(w, r, http.StatusConflict, "Р ТђР ВµР Р…Р Т‘Р В» РЎС“Р В¶Р Вµ Р В·Р В°Р Р…РЎРЏРЎвЂљ")
		return
	}

	passHash, err := password.Hash(req.Password)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	newUser := user{
		ID:        idgen.New(),
		Email:     req.Email,
		Handle:    req.Handle,
		AvatarURL: strings.TrimRight(s.cfg.AvatarBaseURL, "/") + "/default.png",
		CreatedAt: time.Now().UTC(),
	}
	defaultSettings := userSettings{
		Timezone:             "Europe/Warsaw",
		WeekStartsOn:         1,
		ShareHabits:          true,
		ShareCalendar:        true,
		ShareNews:            true,
		NotifyFriendRequests: true,
		NotifyHabitReminders: true,
		NotifyFriendsNews:    true,
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	defer tx.Rollback(r.Context())

	_, err = tx.Exec(r.Context(), `
		INSERT INTO users (id, email, handle, password_hash, avatar_url, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, newUser.ID, newUser.Email, newUser.Handle, passHash, newUser.AvatarURL, newUser.CreatedAt)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	_, err = tx.Exec(r.Context(), `
		INSERT INTO user_settings (
			user_id, timezone, week_starts_on,
			share_habits, share_calendar, share_news,
			notify_friend_requests, notify_habit_reminders, notify_friends_news
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, newUser.ID, defaultSettings.Timezone, defaultSettings.WeekStartsOn,
		defaultSettings.ShareHabits, defaultSettings.ShareCalendar, defaultSettings.ShareNews,
		defaultSettings.NotifyFriendRequests, defaultSettings.NotifyHabitReminders, defaultSettings.NotifyFriendsNews)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	tokens, err := s.issueAndStoreSessionTokens(r.Context(), tx, newUser.ID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	s.log.Info(
		"auth_register_succeeded",
		"request_id", logger.RequestIDFromContext(r.Context()),
		"user_id", newUser.ID,
		"path", r.URL.Path,
		"email", newUser.Email,
		"handle", newUser.Handle,
	)
	httpx.WriteJSON(w, http.StatusCreated, authSuccessResponse{
		User:     newUser,
		Settings: defaultSettings,
		Tokens:   tokens,
	})
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	req.Login = strings.TrimSpace(req.Login)
	if req.Login == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– login")
		return
	}
	if req.Password == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– password")
		return
	}

	var u user
	var err error
	if _, parseErr := mail.ParseAddress(req.Login); parseErr == nil {
		u, err = s.findUserByEmail(r.Context(), strings.ToLower(req.Login))
	} else {
		u, err = s.findUserByHandle(r.Context(), req.Login)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusUnauthorized, "Р СњР ВµР Р†Р ВµРЎР‚Р Р…Р В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°, РЎвЂ¦Р ВµР Р…Р Т‘Р В» Р С‘Р В»Р С‘ Р С—Р В°РЎР‚Р С•Р В»РЎРЉ")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if !password.Check(u.PasswordHash, req.Password) {
		s.writeError(w, r, http.StatusUnauthorized, "Р СњР ВµР Р†Р ВµРЎР‚Р Р…Р В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°, РЎвЂ¦Р ВµР Р…Р Т‘Р В» Р С‘Р В»Р С‘ Р С—Р В°РЎР‚Р С•Р В»РЎРЉ")
		return
	}

	settings, err := s.getUserSettings(r.Context(), u.ID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	defer tx.Rollback(r.Context())
	tokens, err := s.issueAndStoreSessionTokens(r.Context(), tx, u.ID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	u.PasswordHash = ""
	s.log.Info(
		"auth_login_succeeded",
		"request_id", logger.RequestIDFromContext(r.Context()),
		"user_id", u.ID,
		"path", r.URL.Path,
		"handle", u.Handle,
	)

	httpx.WriteJSON(w, http.StatusOK, authSuccessResponse{
		User:     u,
		Settings: settings,
		Tokens:   tokens,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		s.writeError(w, r, http.StatusUnauthorized, "Refresh token Р Р…Р ВµР Т‘Р ВµР в„–РЎРѓРЎвЂљР Р†Р С‘РЎвЂљР ВµР В»Р ВµР Р…")
		return
	}
	hash := authjwt.HashOpaqueToken(req.RefreshToken)

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	defer tx.Rollback(r.Context())

	var tokenID string
	var userID string
	var expiresAt time.Time
	var revokedAt *time.Time
	err = tx.QueryRow(r.Context(), `
		SELECT id, user_id, expires_at, revoked_at
		FROM auth_refresh_tokens
		WHERE token_hash = $1
	`, hash).Scan(&tokenID, &userID, &expiresAt, &revokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusUnauthorized, "Refresh token Р Р…Р ВµР Т‘Р ВµР в„–РЎРѓРЎвЂљР Р†Р С‘РЎвЂљР ВµР В»Р ВµР Р…")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	now := time.Now().UTC()
	if revokedAt != nil || !expiresAt.After(now) {
		s.writeError(w, r, http.StatusUnauthorized, "Refresh token Р Р…Р ВµР Т‘Р ВµР в„–РЎРѓРЎвЂљР Р†Р С‘РЎвЂљР ВµР В»Р ВµР Р…")
		return
	}

	if _, err := tx.Exec(r.Context(), `
		UPDATE auth_refresh_tokens
		SET revoked_at = $2
		WHERE id = $1
	`, tokenID, now); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	tokens, err := s.issueAndStoreSessionTokens(r.Context(), tx, userID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		s.writeError(w, r, http.StatusUnauthorized, "Refresh token Р Р…Р ВµР Т‘Р ВµР в„–РЎРѓРЎвЂљР Р†Р С‘РЎвЂљР ВµР В»Р ВµР Р…")
		return
	}
	hash := authjwt.HashOpaqueToken(req.RefreshToken)
	tag, err := s.pool.Exec(r.Context(), `
		UPDATE auth_refresh_tokens
		SET revoked_at = $2
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, hash, time.Now().UTC())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeError(w, r, http.StatusUnauthorized, "Refresh token Р Р…Р ВµР Т‘Р ВµР в„–РЎРѓРЎвЂљР Р†Р С‘РЎвЂљР ВµР В»Р ВµР Р…")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type passwordResetRequest struct {
	Email string `json:"email"`
}

func (s *Server) handlePasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	var req passwordResetRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…Р В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°")
		return
	}

	var userID string
	if err := s.pool.QueryRow(r.Context(), `SELECT id FROM users WHERE email = $1`, req.Email).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusNotFound, "Р СџР С•Р В»РЎРЉР В·Р С•Р Р†Р В°РЎвЂљР ВµР В»РЎРЉ Р Р…Р Вµ Р Р…Р В°Р в„–Р Т‘Р ВµР Р…")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	rawToken, err := authjwt.NewOpaqueToken()
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	hash := authjwt.HashOpaqueToken(rawToken)
	_, err = s.pool.Exec(r.Context(), `
		INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, used_at, created_at)
		VALUES ($1,$2,$3,$4,NULL,$5)
	`, idgen.New(), userID, hash, time.Now().UTC().Add(s.cfg.PasswordResetTTL), time.Now().UTC())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"message": "Р ВР Р…РЎРѓРЎвЂљРЎР‚РЎС“Р С”РЎвЂ Р С‘Р С‘ Р С•РЎвЂљР С—РЎР‚Р В°Р Р†Р В»Р ВµР Р…РЎвЂ№ Р Р…Р В° Р С—Р С•РЎвЂЎРЎвЂљРЎС“",
	})
}

type passwordResetConfirmRequest struct {
	Token                   string `json:"token"`
	NewPassword             string `json:"newPassword"`
	NewPasswordConfirmation string `json:"newPasswordConfirmation"`
}

func (s *Server) handlePasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	var req passwordResetConfirmRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– РЎвЂљР С•Р С”Р ВµР Р…")
		return
	}
	if req.NewPassword == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– Р С—Р В°РЎР‚Р С•Р В»РЎРЉ")
		return
	}
	if req.NewPassword != req.NewPasswordConfirmation {
		s.writeError(w, r, http.StatusBadRequest, "Р СџР В°РЎР‚Р С•Р В»Р С‘ Р Р…Р Вµ РЎРѓР С•Р Р†Р С—Р В°Р Т‘Р В°РЎР‹РЎвЂљ")
		return
	}

	hash := authjwt.HashOpaqueToken(req.Token)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	defer tx.Rollback(r.Context())

	var tokenID string
	var userID string
	var expiresAt time.Time
	var usedAt *time.Time
	err = tx.QueryRow(r.Context(), `
		SELECT id, user_id, expires_at, used_at
		FROM password_reset_tokens
		WHERE token_hash = $1
	`, hash).Scan(&tokenID, &userID, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusUnauthorized, "Р СћР С•Р С”Р ВµР Р… Р Р…Р ВµР Т‘Р ВµР в„–РЎРѓРЎвЂљР Р†Р С‘РЎвЂљР ВµР В»Р ВµР Р…")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if usedAt != nil {
		s.writeError(w, r, http.StatusConflict, "Р СћР С•Р С”Р ВµР Р… РЎС“Р В¶Р Вµ Р С‘РЎРѓР С—Р С•Р В»РЎРЉР В·Р С•Р Р†Р В°Р Р…")
		return
	}
	if !expiresAt.After(time.Now().UTC()) {
		s.writeError(w, r, http.StatusUnauthorized, "Р СћР С•Р С”Р ВµР Р… Р С‘РЎРѓРЎвЂљРЎвЂР С”")
		return
	}

	passHash, err := password.Hash(req.NewPassword)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE users SET password_hash = $2 WHERE id = $1
	`, userID, passHash); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE password_reset_tokens SET used_at = $2 WHERE id = $1
	`, tokenID, time.Now().UTC()); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"message": "Р СџР В°РЎР‚Р С•Р В»РЎРЉ РЎС“РЎРѓР С—Р ВµРЎв‚¬Р Р…Р С• Р С‘Р В·Р СР ВµР Р…РЎвЂР Р…",
	})
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.findUserByID(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		s.writeError(w, r, http.StatusUnauthorized, "Р СњР ВµР В°Р Р†РЎвЂљР С•РЎР‚Р С‘Р В·Р С•Р Р†Р В°Р Р…")
		return
	}
	u.PasswordHash = ""
	httpx.WriteJSON(w, http.StatusOK, u)
}

type patchProfileRequest struct {
	Email  *string `json:"email"`
	Handle *string `json:"handle"`
}

func (s *Server) handlePatchProfile(w http.ResponseWriter, r *http.Request) {
	var req patchProfileRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	if req.Email == nil && req.Handle == nil {
		s.writeError(w, r, http.StatusBadRequest, "Р С›РЎвЂљРЎРѓРЎС“РЎвЂљРЎРѓРЎвЂљР Р†РЎС“РЎР‹РЎвЂљ Р С—Р С•Р В»РЎРЏ Р Т‘Р В»РЎРЏ Р С•Р В±Р Р…Р С•Р Р†Р В»Р ВµР Р…Р С‘РЎРЏ")
		return
	}
	currentUserID := userIDFromContext(r.Context())

	if req.Email != nil {
		email := strings.TrimSpace(strings.ToLower(*req.Email))
		if _, err := mail.ParseAddress(email); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…Р В°РЎРЏ Р С—Р С•РЎвЂЎРЎвЂљР В°")
			return
		}
		exists, err := s.existsByEmailExcludingUser(r.Context(), email, currentUserID)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
		if exists {
			s.writeError(w, r, http.StatusConflict, "Р СџР С•РЎвЂЎРЎвЂљР В° РЎС“Р В¶Р Вµ Р В·Р В°Р Р…РЎРЏРЎвЂљР В°")
			return
		}
		req.Email = &email
	}
	if req.Handle != nil {
		handle := strings.TrimSpace(*req.Handle)
		if handle == "" {
			s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– РЎвЂ¦Р ВµР Р…Р Т‘Р В»")
			return
		}
		exists, err := s.existsByHandleExcludingUser(r.Context(), handle, currentUserID)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
		if exists {
			s.writeError(w, r, http.StatusConflict, "Р ТђР ВµР Р…Р Т‘Р В» РЎС“Р В¶Р Вµ Р В·Р В°Р Р…РЎРЏРЎвЂљ")
			return
		}
		req.Handle = &handle
	}

	setParts := make([]string, 0, 2)
	args := make([]any, 0, 3)
	idx := 1
	if req.Email != nil {
		setParts = append(setParts, fmt.Sprintf("email = $%d", idx))
		args = append(args, *req.Email)
		idx++
	}
	if req.Handle != nil {
		setParts = append(setParts, fmt.Sprintf("handle = $%d", idx))
		args = append(args, *req.Handle)
		idx++
	}
	args = append(args, currentUserID)
	query := fmt.Sprintf("UPDATE users SET %s WHERE id = $%d", strings.Join(setParts, ", "), idx)
	if _, err := s.pool.Exec(r.Context(), query, args...); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	updated, err := s.findUserByID(r.Context(), currentUserID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	updated.PasswordHash = ""
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) handlePatchAvatar(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р В¤Р В°Р в„–Р В» Р Р…Р Вµ Р С—Р ВµРЎР‚Р ВµР Т‘Р В°Р Р…")
		return
	}
	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р В¤Р В°Р в„–Р В» Р Р…Р Вµ Р С—Р ВµРЎР‚Р ВµР Т‘Р В°Р Р…")
		return
	}
	defer file.Close()

	contentType := fileHeader.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С—Р С•Р Т‘Р Т‘Р ВµРЎР‚Р В¶Р С‘Р Р†Р В°Р ВµР СРЎвЂ№Р в„– РЎвЂљР С‘Р С— РЎвЂћР В°Р в„–Р В»Р В°")
		return
	}

	userID := userIDFromContext(r.Context())
	url, err := s.saveAvatarFile(file, fileHeader, userID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р СњР Вµ РЎС“Р Т‘Р В°Р В»Р С•РЎРѓРЎРЉ РЎРѓР С•РЎвЂ¦РЎР‚Р В°Р Р…Р С‘РЎвЂљРЎРЉ Р В°Р Р†Р В°РЎвЂљР В°РЎР‚")
		return
	}

	if _, err := s.pool.Exec(r.Context(), `
		UPDATE users SET avatar_url = $2 WHERE id = $1
	`, userID, url); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}

	updated, err := s.findUserByID(r.Context(), userID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	updated.PasswordHash = ""
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) saveAvatarFile(file multipart.File, header *multipart.FileHeader, userID string) (string, error) {
	if err := os.MkdirAll(s.cfg.AvatarDir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		ext = ".bin"
	}
	filename := fmt.Sprintf("%s_%d%s", userID, time.Now().UTC().UnixNano(), ext)
	path := filepath.Join(s.cfg.AvatarDir, filename)
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		return "", err
	}
	return strings.TrimRight(s.cfg.AvatarBaseURL, "/") + "/" + filename, nil
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.getUserSettings(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settings)
}

type patchPrivacySettingsRequest struct {
	ShareHabits   *bool `json:"shareHabits"`
	ShareCalendar *bool `json:"shareCalendar"`
	ShareNews     *bool `json:"shareNews"`
}

func (s *Server) handlePatchPrivacySettings(w http.ResponseWriter, r *http.Request) {
	var req patchPrivacySettingsRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	if req.ShareHabits == nil && req.ShareCalendar == nil && req.ShareNews == nil {
		s.writeError(w, r, http.StatusBadRequest, "Р С›РЎвЂљРЎРѓРЎС“РЎвЂљРЎРѓРЎвЂљР Р†РЎС“РЎР‹РЎвЂљ Р Р†РЎРѓР Вµ Р С—Р С•Р В»РЎРЏ Р Т‘Р В»РЎРЏ Р С•Р В±Р Р…Р С•Р Р†Р В»Р ВµР Р…Р С‘РЎРЏ")
		return
	}
	userID := userIDFromContext(r.Context())
	if req.ShareHabits != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET share_habits = $2 WHERE user_id = $1`, userID, *req.ShareHabits); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}
	if req.ShareCalendar != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET share_calendar = $2 WHERE user_id = $1`, userID, *req.ShareCalendar); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}
	if req.ShareNews != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET share_news = $2 WHERE user_id = $1`, userID, *req.ShareNews); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}

	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"shareHabits":   settings.ShareHabits,
		"shareCalendar": settings.ShareCalendar,
		"shareNews":     settings.ShareNews,
	})
}

type patchNotificationSettingsRequest struct {
	NotifyFriendRequests *bool `json:"notifyFriendRequests"`
	NotifyHabitReminders *bool `json:"notifyHabitReminders"`
	NotifyFriendsNews    *bool `json:"notifyFriendsNews"`
}

func (s *Server) handlePatchNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var req patchNotificationSettingsRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	if req.NotifyFriendRequests == nil && req.NotifyHabitReminders == nil && req.NotifyFriendsNews == nil {
		s.writeError(w, r, http.StatusBadRequest, "Р С›РЎвЂљРЎРѓРЎС“РЎвЂљРЎРѓРЎвЂљР Р†РЎС“РЎР‹РЎвЂљ Р Р†РЎРѓР Вµ Р С—Р С•Р В»РЎРЏ Р Т‘Р В»РЎРЏ Р С•Р В±Р Р…Р С•Р Р†Р В»Р ВµР Р…Р С‘РЎРЏ")
		return
	}
	userID := userIDFromContext(r.Context())
	if req.NotifyFriendRequests != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET notify_friend_requests = $2 WHERE user_id = $1`, userID, *req.NotifyFriendRequests); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}
	if req.NotifyHabitReminders != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET notify_habit_reminders = $2 WHERE user_id = $1`, userID, *req.NotifyHabitReminders); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}
	if req.NotifyFriendsNews != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET notify_friends_news = $2 WHERE user_id = $1`, userID, *req.NotifyFriendsNews); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}

	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"notifyFriendRequests": settings.NotifyFriendRequests,
		"notifyHabitReminders": settings.NotifyHabitReminders,
		"notifyFriendsNews":    settings.NotifyFriendsNews,
	})
}

type patchCalendarSettingsRequest struct {
	Timezone     *string `json:"timezone"`
	WeekStartsOn *int    `json:"weekStartsOn"`
}

func (s *Server) handlePatchCalendarSettings(w http.ResponseWriter, r *http.Request) {
	var req patchCalendarSettingsRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– JSON")
		return
	}
	if req.Timezone == nil && req.WeekStartsOn == nil {
		s.writeError(w, r, http.StatusBadRequest, "Р С›РЎвЂљРЎРѓРЎС“РЎвЂљРЎРѓРЎвЂљР Р†РЎС“РЎР‹РЎвЂљ Р Р†РЎРѓР Вµ Р С—Р С•Р В»РЎРЏ Р Т‘Р В»РЎРЏ Р С•Р В±Р Р…Р С•Р Р†Р В»Р ВµР Р…Р С‘РЎРЏ")
		return
	}
	userID := userIDFromContext(r.Context())

	if req.Timezone != nil {
		tz := strings.TrimSpace(*req.Timezone)
		if tz == "" {
			s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– timezone")
			return
		}
		if _, err := time.LoadLocation(tz); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– timezone")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET timezone = $2 WHERE user_id = $1`, userID, tz); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}
	if req.WeekStartsOn != nil {
		if *req.WeekStartsOn != 1 && *req.WeekStartsOn != 7 {
			s.writeError(w, r, http.StatusBadRequest, "Р СњР ВµР С”Р С•РЎР‚РЎР‚Р ВµР С”РЎвЂљР Р…РЎвЂ№Р в„– weekStartsOn")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET week_starts_on = $2 WHERE user_id = $1`, userID, *req.WeekStartsOn); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
			return
		}
	}

	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"timezone":     settings.Timezone,
		"weekStartsOn": settings.WeekStartsOn,
	})
}

func (s *Server) handleInternalGetPublicUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	var response publicUser
	err := s.pool.QueryRow(r.Context(), `
		SELECT id, handle, avatar_url
		FROM users
		WHERE id = $1
	`, userID).Scan(&response.ID, &response.Handle, &response.AvatarURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusNotFound, "Р СџР С•Р В»РЎРЉР В·Р С•Р Р†Р В°РЎвЂљР ВµР В»РЎРЉ Р Р…Р Вµ Р Р…Р В°Р в„–Р Т‘Р ВµР Р…")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (s *Server) handleInternalGetUserSettings(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusNotFound, "Р СџР С•Р В»РЎРЉР В·Р С•Р Р†Р В°РЎвЂљР ВµР В»РЎРЉ Р Р…Р Вµ Р Р…Р В°Р в„–Р Т‘Р ВµР Р…")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settings)
}

func (s *Server) handleInternalGetByHandle(w http.ResponseWriter, r *http.Request) {
	handle := strings.TrimSpace(chi.URLParam(r, "handle"))
	if handle == "" {
		s.writeError(w, r, http.StatusBadRequest, "Р СџРЎС“РЎРѓРЎвЂљР С•Р в„– handle")
		return
	}
	var response publicUser
	err := s.pool.QueryRow(r.Context(), `
		SELECT id, handle, avatar_url
		FROM users
		WHERE handle = $1
	`, handle).Scan(&response.ID, &response.Handle, &response.AvatarURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, r, http.StatusNotFound, "Р СџР С•Р В»РЎРЉР В·Р С•Р Р†Р В°РЎвЂљР ВµР В»РЎРЉ Р Р…Р Вµ Р Р…Р В°Р в„–Р Т‘Р ВµР Р…")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, "Р вЂ™Р Р…РЎС“РЎвЂљРЎР‚Р ВµР Р…Р Р…РЎРЏРЎРЏ Р С•РЎв‚¬Р С‘Р В±Р С”Р В°")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (s *Server) issueAndStoreSessionTokens(ctx context.Context, tx pgx.Tx, userID string) (tokenPair, error) {
	access, err := authjwt.IssueAccessToken(userID, s.cfg.JWTSecret, s.cfg.AccessTTL)
	if err != nil {
		return tokenPair{}, err
	}
	refresh, err := authjwt.NewOpaqueToken()
	if err != nil {
		return tokenPair{}, err
	}
	refreshHash := authjwt.HashOpaqueToken(refresh)
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_refresh_tokens (id, user_id, token_hash, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, NULL)
	`, idgen.New(), userID, refreshHash, time.Now().UTC().Add(s.cfg.RefreshTTL))
	if err != nil {
		return tokenPair{}, err
	}
	return tokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
	}, nil
}

func (s *Server) getUserSettings(ctx context.Context, userID string) (userSettings, error) {
	var st userSettings
	err := s.pool.QueryRow(ctx, `
		SELECT timezone, week_starts_on, share_habits, share_calendar, share_news,
		       notify_friend_requests, notify_habit_reminders, notify_friends_news
		FROM user_settings
		WHERE user_id = $1
	`, userID).Scan(
		&st.Timezone, &st.WeekStartsOn, &st.ShareHabits, &st.ShareCalendar, &st.ShareNews,
		&st.NotifyFriendRequests, &st.NotifyHabitReminders, &st.NotifyFriendsNews,
	)
	return st, err
}

func (s *Server) findUserByID(ctx context.Context, userID string) (user, error) {
	var u user
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, handle, avatar_url, created_at, password_hash
		FROM users
		WHERE id = $1
	`, userID).Scan(&u.ID, &u.Email, &u.Handle, &u.AvatarURL, &u.CreatedAt, &u.PasswordHash)
	return u, err
}

func (s *Server) findUserByEmail(ctx context.Context, email string) (user, error) {
	var u user
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, handle, avatar_url, created_at, password_hash
		FROM users
		WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.Handle, &u.AvatarURL, &u.CreatedAt, &u.PasswordHash)
	return u, err
}

func (s *Server) findUserByHandle(ctx context.Context, handle string) (user, error) {
	var u user
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, handle, avatar_url, created_at, password_hash
		FROM users
		WHERE handle = $1
	`, handle).Scan(&u.ID, &u.Email, &u.Handle, &u.AvatarURL, &u.CreatedAt, &u.PasswordHash)
	return u, err
}

func (s *Server) existsByEmail(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`, email).Scan(&exists)
	return exists, err
}

func (s *Server) existsByHandle(ctx context.Context, handle string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE handle = $1)`, handle).Scan(&exists)
	return exists, err
}

func (s *Server) existsByEmailExcludingUser(ctx context.Context, email string, userID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM users WHERE email = $1 AND id <> $2)
	`, email, userID).Scan(&exists)
	return exists, err
}

func (s *Server) existsByHandleExcludingUser(ctx context.Context, handle string, userID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM users WHERE handle = $1 AND id <> $2)
	`, handle, userID).Scan(&exists)
	return exists, err
}

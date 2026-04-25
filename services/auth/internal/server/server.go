package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/httpx"
	"habical/backend/libs/idgen"
	"habical/backend/libs/password"
	"habical/backend/services/auth/internal/config"

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

	r.Post("/auth/register", s.handleRegister)
	r.Post("/auth/login", s.handleLogin)
	r.Post("/auth/refresh", s.handleRefresh)
	r.Post("/auth/logout", s.handleLogout)
	r.Post("/auth/password-reset/request", s.handlePasswordResetRequest)
	r.Post("/auth/password-reset/confirm", s.handlePasswordResetConfirm)

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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Handle = strings.TrimSpace(req.Handle)
	if req.Email == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустая почта")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректная почта")
		return
	}
	if req.Handle == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой хендл")
		return
	}
	if req.Password == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой пароль")
		return
	}
	if req.Password != req.PasswordConfirmation {
		httpx.WriteError(w, http.StatusBadRequest, "Пароль и подтверждение не совпадают")
		return
	}

	exists, err := s.existsByEmail(r.Context(), req.Email)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if exists {
		httpx.WriteError(w, http.StatusConflict, "Почта уже занята")
		return
	}
	exists, err = s.existsByHandle(r.Context(), req.Handle)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if exists {
		httpx.WriteError(w, http.StatusConflict, "Хендл уже занят")
		return
	}

	passHash, err := password.Hash(req.Password)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())

	_, err = tx.Exec(r.Context(), `
		INSERT INTO users (id, email, handle, password_hash, avatar_url, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, newUser.ID, newUser.Email, newUser.Handle, passHash, newUser.AvatarURL, newUser.CreatedAt)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	tokens, err := s.issueAndStoreSessionTokens(r.Context(), tx, newUser.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Login = strings.TrimSpace(req.Login)
	if req.Login == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой login")
		return
	}
	if req.Password == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой password")
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
			httpx.WriteError(w, http.StatusUnauthorized, "Неверная почта, хендл или пароль")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if !password.Check(u.PasswordHash, req.Password) {
		httpx.WriteError(w, http.StatusUnauthorized, "Неверная почта, хендл или пароль")
		return
	}

	settings, err := s.getUserSettings(r.Context(), u.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	defer tx.Rollback(r.Context())
	tokens, err := s.issueAndStoreSessionTokens(r.Context(), tx, u.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	u.PasswordHash = ""

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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "Refresh token недействителен")
		return
	}
	hash := authjwt.HashOpaqueToken(req.RefreshToken)

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
			httpx.WriteError(w, http.StatusUnauthorized, "Refresh token недействителен")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	now := time.Now().UTC()
	if revokedAt != nil || !expiresAt.After(now) {
		httpx.WriteError(w, http.StatusUnauthorized, "Refresh token недействителен")
		return
	}

	if _, err := tx.Exec(r.Context(), `
		UPDATE auth_refresh_tokens
		SET revoked_at = $2
		WHERE id = $1
	`, tokenID, now); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	tokens, err := s.issueAndStoreSessionTokens(r.Context(), tx, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "Refresh token недействителен")
		return
	}
	hash := authjwt.HashOpaqueToken(req.RefreshToken)
	tag, err := s.pool.Exec(r.Context(), `
		UPDATE auth_refresh_tokens
		SET revoked_at = $2
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, hash, time.Now().UTC())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, http.StatusUnauthorized, "Refresh token недействителен")
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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустая почта")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Некорректная почта")
		return
	}

	var userID string
	if err := s.pool.QueryRow(r.Context(), `SELECT id FROM users WHERE email = $1`, req.Email).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Пользователь не найден")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	rawToken, err := authjwt.NewOpaqueToken()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	hash := authjwt.HashOpaqueToken(rawToken)
	_, err = s.pool.Exec(r.Context(), `
		INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, used_at, created_at)
		VALUES ($1,$2,$3,$4,NULL,$5)
	`, idgen.New(), userID, hash, time.Now().UTC().Add(s.cfg.PasswordResetTTL), time.Now().UTC())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"message": "Инструкции отправлены на почту",
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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой токен")
		return
	}
	if req.NewPassword == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой пароль")
		return
	}
	if req.NewPassword != req.NewPasswordConfirmation {
		httpx.WriteError(w, http.StatusBadRequest, "Пароли не совпадают")
		return
	}

	hash := authjwt.HashOpaqueToken(req.Token)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
			httpx.WriteError(w, http.StatusUnauthorized, "Токен недействителен")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if usedAt != nil {
		httpx.WriteError(w, http.StatusConflict, "Токен уже использован")
		return
	}
	if !expiresAt.After(time.Now().UTC()) {
		httpx.WriteError(w, http.StatusUnauthorized, "Токен истёк")
		return
	}

	passHash, err := password.Hash(req.NewPassword)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE users SET password_hash = $2 WHERE id = $1
	`, userID, passHash); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE password_reset_tokens SET used_at = $2 WHERE id = $1
	`, tokenID, time.Now().UTC()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"message": "Пароль успешно изменён",
	})
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.findUserByID(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "Неавторизован")
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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.Email == nil && req.Handle == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют поля для обновления")
		return
	}
	currentUserID := userIDFromContext(r.Context())

	if req.Email != nil {
		email := strings.TrimSpace(strings.ToLower(*req.Email))
		if _, err := mail.ParseAddress(email); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректная почта")
			return
		}
		exists, err := s.existsByEmailExcludingUser(r.Context(), email, currentUserID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if exists {
			httpx.WriteError(w, http.StatusConflict, "Почта уже занята")
			return
		}
		req.Email = &email
	}
	if req.Handle != nil {
		handle := strings.TrimSpace(*req.Handle)
		if handle == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Пустой хендл")
			return
		}
		exists, err := s.existsByHandleExcludingUser(r.Context(), handle, currentUserID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
		if exists {
			httpx.WriteError(w, http.StatusConflict, "Хендл уже занят")
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
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	updated, err := s.findUserByID(r.Context(), currentUserID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	updated.PasswordHash = ""
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) handlePatchAvatar(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Файл не передан")
		return
	}
	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Файл не передан")
		return
	}
	defer file.Close()

	contentType := fileHeader.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		httpx.WriteError(w, http.StatusBadRequest, "Неподдерживаемый тип файла")
		return
	}

	userID := userIDFromContext(r.Context())
	url, err := s.saveAvatarFile(file, fileHeader, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Не удалось сохранить аватар")
		return
	}

	if _, err := s.pool.Exec(r.Context(), `
		UPDATE users SET avatar_url = $2 WHERE id = $1
	`, userID, url); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}

	updated, err := s.findUserByID(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.ShareHabits == nil && req.ShareCalendar == nil && req.ShareNews == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют все поля для обновления")
		return
	}
	userID := userIDFromContext(r.Context())
	if req.ShareHabits != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET share_habits = $2 WHERE user_id = $1`, userID, *req.ShareHabits); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.ShareCalendar != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET share_calendar = $2 WHERE user_id = $1`, userID, *req.ShareCalendar); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.ShareNews != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET share_news = $2 WHERE user_id = $1`, userID, *req.ShareNews); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}

	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.NotifyFriendRequests == nil && req.NotifyHabitReminders == nil && req.NotifyFriendsNews == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют все поля для обновления")
		return
	}
	userID := userIDFromContext(r.Context())
	if req.NotifyFriendRequests != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET notify_friend_requests = $2 WHERE user_id = $1`, userID, *req.NotifyFriendRequests); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.NotifyHabitReminders != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET notify_habit_reminders = $2 WHERE user_id = $1`, userID, *req.NotifyHabitReminders); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.NotifyFriendsNews != nil {
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET notify_friends_news = $2 WHERE user_id = $1`, userID, *req.NotifyFriendsNews); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}

	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
		httpx.WriteError(w, http.StatusBadRequest, "Некорректный JSON")
		return
	}
	if req.Timezone == nil && req.WeekStartsOn == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Отсутствуют все поля для обновления")
		return
	}
	userID := userIDFromContext(r.Context())

	if req.Timezone != nil {
		tz := strings.TrimSpace(*req.Timezone)
		if tz == "" {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный timezone")
			return
		}
		if _, err := time.LoadLocation(tz); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный timezone")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET timezone = $2 WHERE user_id = $1`, userID, tz); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}
	if req.WeekStartsOn != nil {
		if *req.WeekStartsOn != 1 && *req.WeekStartsOn != 7 {
			httpx.WriteError(w, http.StatusBadRequest, "Некорректный weekStartsOn")
			return
		}
		if _, err := s.pool.Exec(r.Context(), `UPDATE user_settings SET week_starts_on = $2 WHERE user_id = $1`, userID, *req.WeekStartsOn); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
			return
		}
	}

	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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
			httpx.WriteError(w, http.StatusNotFound, "Пользователь не найден")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (s *Server) handleInternalGetUserSettings(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	settings, err := s.getUserSettings(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "Пользователь не найден")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settings)
}

func (s *Server) handleInternalGetByHandle(w http.ResponseWriter, r *http.Request) {
	handle := strings.TrimSpace(chi.URLParam(r, "handle"))
	if handle == "" {
		httpx.WriteError(w, http.StatusBadRequest, "Пустой handle")
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
			httpx.WriteError(w, http.StatusNotFound, "Пользователь не найден")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
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

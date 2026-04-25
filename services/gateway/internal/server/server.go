package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/httpx"
	"habical/backend/services/gateway/internal/config"

	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg        config.Config
	httpClient *http.Client
}

func New(cfg config.Config) *Server {
	return &Server{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.HTTPClientTimeoutSeconds) * time.Second,
		},
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	// Auth public routes.
	r.MethodFunc(http.MethodPost, "/auth/register", s.forwardTo(s.cfg.AuthURL))
	r.MethodFunc(http.MethodPost, "/auth/login", s.forwardTo(s.cfg.AuthURL))
	r.MethodFunc(http.MethodPost, "/auth/refresh", s.forwardTo(s.cfg.AuthURL))
	r.MethodFunc(http.MethodPost, "/auth/logout", s.forwardTo(s.cfg.AuthURL))
	r.MethodFunc(http.MethodPost, "/auth/password-reset/request", s.forwardTo(s.cfg.AuthURL))
	r.MethodFunc(http.MethodPost, "/auth/password-reset/confirm", s.forwardTo(s.cfg.AuthURL))

	// Protected routes.
	r.Group(func(pr chi.Router) {
		pr.Use(s.authMiddleware)

		// Auth/Profile/Settings.
		pr.MethodFunc(http.MethodGet, "/me", s.forwardTo(s.cfg.AuthURL))
		pr.MethodFunc(http.MethodPatch, "/me/profile", s.forwardTo(s.cfg.AuthURL))
		pr.MethodFunc(http.MethodPatch, "/me/avatar", s.forwardTo(s.cfg.AuthURL))
		pr.MethodFunc(http.MethodGet, "/me/settings", s.forwardTo(s.cfg.AuthURL))
		pr.MethodFunc(http.MethodPatch, "/me/settings/privacy", s.forwardTo(s.cfg.AuthURL))
		pr.MethodFunc(http.MethodPatch, "/me/settings/notifications", s.forwardTo(s.cfg.AuthURL))
		pr.MethodFunc(http.MethodPatch, "/me/settings/calendar", s.forwardTo(s.cfg.AuthURL))

		// Events / categories.
		pr.MethodFunc(http.MethodGet, "/me/events", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodPost, "/me/events", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodGet, "/me/events/{eventId}", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodPatch, "/me/events/{eventId}", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodDelete, "/me/events/{eventId}", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodPost, "/me/events/{eventId}/move", s.forwardTo(s.cfg.CoreURL))

		pr.MethodFunc(http.MethodGet, "/me/event-categories", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodPost, "/me/event-categories", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodPatch, "/me/event-categories/{categoryId}", s.forwardTo(s.cfg.CoreURL))
		pr.MethodFunc(http.MethodDelete, "/me/event-categories/{categoryId}", s.forwardTo(s.cfg.CoreURL))

		// Social.
		pr.MethodFunc(http.MethodGet, "/me/friends", s.forwardTo(s.cfg.SocialURL))
		pr.MethodFunc(http.MethodDelete, "/me/friends/{friendUserId}", s.forwardTo(s.cfg.SocialURL))
		pr.MethodFunc(http.MethodGet, "/me/friend-invites", s.forwardTo(s.cfg.SocialURL))
		pr.MethodFunc(http.MethodPost, "/me/friend-invites", s.forwardTo(s.cfg.SocialURL))
		pr.MethodFunc(http.MethodPost, "/me/friend-invites/{inviteId}/accept", s.forwardTo(s.cfg.SocialURL))
		pr.MethodFunc(http.MethodPost, "/me/friend-invites/{inviteId}/reject", s.forwardTo(s.cfg.SocialURL))
		pr.MethodFunc(http.MethodGet, "/me/feed", s.forwardTo(s.cfg.SocialURL))

		// Friend page orchestration.
		pr.MethodFunc(http.MethodGet, "/users/{userId}", s.handleFriendPageUser)
		pr.MethodFunc(http.MethodGet, "/users/{userId}/events", s.handleFriendPageEvents)
		pr.MethodFunc(http.MethodGet, "/users/{userId}/tasks", s.handleFriendPageTasksNotReady)
		pr.MethodFunc(http.MethodGet, "/users/{userId}/shared-habits", s.handleFriendPageSharedHabitsNotReady)
	})

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

func (s *Server) forwardTo(targetBaseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.forward(w, r, targetBaseURL)
	}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, targetBaseURL string) {
	target, err := joinURL(targetBaseURL, r.URL.Path, r.URL.RawQuery)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Неверная конфигурация gateway")
		return
	}

	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(bodyBytes))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Внутренняя ошибка")
		return
	}
	copyHeaders(req.Header, r.Header)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "Downstream сервис недоступен")
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyHeaders(dst http.Header, src http.Header) {
	for k, values := range src {
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func joinURL(base string, path string, rawQuery string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = rawQuery
	return u.String(), nil
}

type friendshipCheckResponse struct {
	IsFriend bool `json:"isFriend"`
}

type userSettingsResponse struct {
	Timezone             string `json:"timezone"`
	WeekStartsOn         int    `json:"weekStartsOn"`
	ShareHabits          bool   `json:"shareHabits"`
	ShareCalendar        bool   `json:"shareCalendar"`
	ShareNews            bool   `json:"shareNews"`
	NotifyFriendRequests bool   `json:"notifyFriendRequests"`
	NotifyHabitReminders bool   `json:"notifyHabitReminders"`
	NotifyFriendsNews    bool   `json:"notifyFriendsNews"`
}

func (s *Server) handleFriendPageUser(w http.ResponseWriter, r *http.Request) {
	requester := userIDFromContext(r.Context())
	targetUserID := chi.URLParam(r, "userId")

	allowed, code, message := s.checkFriendAccess(r, requester, targetUserID)
	if !allowed {
		httpx.WriteError(w, code, message)
		return
	}
	target, _ := joinURL(s.cfg.AuthURL, "/internal/users/"+targetUserID+"/public", "")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "Auth сервис недоступен")
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) handleFriendPageEvents(w http.ResponseWriter, r *http.Request) {
	requester := userIDFromContext(r.Context())
	targetUserID := chi.URLParam(r, "userId")

	allowed, code, message := s.checkFriendAccess(r, requester, targetUserID)
	if !allowed {
		httpx.WriteError(w, code, message)
		return
	}

	if requester != targetUserID {
		settings, err := s.fetchUserSettings(r.Context(), targetUserID)
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "Не удалось проверить настройки приватности")
			return
		}
		if !settings.ShareCalendar {
			httpx.WriteError(w, http.StatusForbidden, "Календарь скрыт настройками приватности")
			return
		}
	}
	s.forward(w, r, s.cfg.CoreURL)
}

func (s *Server) handleFriendPageTasksNotReady(w http.ResponseWriter, r *http.Request) {
	requester := userIDFromContext(r.Context())
	targetUserID := chi.URLParam(r, "userId")
	allowed, code, message := s.checkFriendAccess(r, requester, targetUserID)
	if !allowed {
		httpx.WriteError(w, code, message)
		return
	}
	if requester != targetUserID {
		settings, err := s.fetchUserSettings(r.Context(), targetUserID)
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "Не удалось проверить настройки приватности")
			return
		}
		if !settings.ShareHabits {
			httpx.WriteError(w, http.StatusForbidden, "Привычки скрыты настройками приватности")
			return
		}
	}
	httpx.WriteError(w, http.StatusNotImplemented, "Endpoint ожидает модуль tasks (зона Участника 1)")
}

func (s *Server) handleFriendPageSharedHabitsNotReady(w http.ResponseWriter, r *http.Request) {
	requester := userIDFromContext(r.Context())
	targetUserID := chi.URLParam(r, "userId")
	allowed, code, message := s.checkFriendAccess(r, requester, targetUserID)
	if !allowed {
		httpx.WriteError(w, code, message)
		return
	}
	httpx.WriteError(w, http.StatusNotImplemented, "Endpoint ожидает модуль sharedhabits (зона Участника 1)")
}

func (s *Server) checkFriendAccess(r *http.Request, requester string, target string) (bool, int, string) {
	if requester == target {
		return true, 0, ""
	}
	targetURL, _ := joinURL(s.cfg.SocialURL, "/internal/friendships/"+requester+"/with/"+target, "")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	copyHeaders(req.Header, r.Header)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, http.StatusBadGateway, "Social сервис недоступен"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, http.StatusForbidden, "Нет дружбы"
	}
	var payload friendshipCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false, http.StatusBadGateway, "Некорректный ответ social сервиса"
	}
	if !payload.IsFriend {
		return false, http.StatusForbidden, "Нет дружбы"
	}
	return true, 0, ""
}

func (s *Server) fetchUserSettings(ctx context.Context, userID string) (userSettingsResponse, error) {
	urlStr, _ := joinURL(s.cfg.AuthURL, "/internal/users/"+userID+"/settings", "")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return userSettingsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return userSettingsResponse{}, errors.New("not found")
	}
	var settings userSettingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return userSettingsResponse{}, err
	}
	return settings, nil
}

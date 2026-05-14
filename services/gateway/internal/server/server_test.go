package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/logger"
	"habical/backend/services/gateway/internal/config"
)

type settingsResponse struct {
	ShareHabits bool `json:"shareHabits"`
}

func TestHandleFriendPageTasks(t *testing.T) {
	coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/target/tasks" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("date") != "2026-04-26" {
			http.Error(w, "missing date", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"task-1","title":"Task 1","isCompleted":false}]`))
	}))
	defer coreServer.Close()

	socialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/friendships/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"isFriend":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer socialServer.Close()

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/settings") {
			w.Header().Set("Content-Type", "application/json")
			settings := settingsResponse{ShareHabits: true}
			_ = json.NewEncoder(w).Encode(settings)
			return
		}
		http.NotFound(w, r)
	}))
	defer authServer.Close()

	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  authServer.URL,
		CoreURL:                  coreServer.URL,
		SocialURL:                socialServer.URL,
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/target/tasks?date=2026-04-26", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()

	server.Router().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "task-1") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestHandleFriendPageSharedHabits(t *testing.T) {
	coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/target/shared-habits" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"sharedHabitPairId":"pair-1","habitId":"habit-1","title":"Shared Habit","color":"green","streakDays":3,"youCompletedToday":true,"friendCompletedToday":false}]`))
	}))
	defer coreServer.Close()

	socialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/friendships/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"isFriend":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer socialServer.Close()

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/settings") {
			w.Header().Set("Content-Type", "application/json")
			settings := settingsResponse{ShareHabits: true}
			_ = json.NewEncoder(w).Encode(settings)
			return
		}
		http.NotFound(w, r)
	}))
	defer authServer.Close()

	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  authServer.URL,
		CoreURL:                  coreServer.URL,
		SocialURL:                socialServer.URL,
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/target/shared-habits", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()

	server.Router().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "pair-1") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestGatewayForwardsCoreRoutes(t *testing.T) {
	coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /me/tasks":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":"task-1"}]`))
		case "POST /me/tasks":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"task-2"}`))
		case "GET /me/tasks/task-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"task-1"}`))
		case "PATCH /me/tasks/task-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"task-1","updated":true}`))
		case "DELETE /me/tasks/task-1":
			w.WriteHeader(http.StatusNoContent)
		case "POST /me/tasks/task-1/toggle":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"task-1","toggled":true}`))
		case "POST /me/tasks/reorder":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"reordered":true}`))
		case "POST /me/tasks/task-1/event-link":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"linked":true}`))
		case "DELETE /me/tasks/task-1/event-link":
			w.WriteHeader(http.StatusNoContent)
		case "GET /me/habits":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":"habit-1"}]`))
		case "POST /me/habits":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"habit-2"}`))
		case "GET /me/habits/habit-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"habit-1"}`))
		case "PATCH /me/habits/habit-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"habit-1","updated":true}`))
		case "DELETE /me/habits/habit-1":
			w.WriteHeader(http.StatusNoContent)
		case "GET /me/habits/calendar-summary":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"summary":"ok"}`))
		case "POST /me/shared-habits":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sharedHabitPairId":"pair-123"}`))
		case "GET /me/shared-habits/pair-123":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sharedHabitPairId":"pair-123"}`))
		case "POST /me/shared-habits/pair-123/remind":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"reminded":true}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer coreServer.Close()

	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  "http://auth.invalid",
		CoreURL:                  coreServer.URL,
		SocialURL:                "http://social.invalid",
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   string
		code   int
	}{
		{"List tasks", http.MethodGet, "/me/tasks?date=2026-04-26", "", "task-1", http.StatusOK},
		{"Create task", http.MethodPost, "/me/tasks", `{"title":"New Task"}`, `"id":"task-2"`, http.StatusCreated},
		{"Get task", http.MethodGet, "/me/tasks/task-1", "", `"id":"task-1"`, http.StatusOK},
		{"Patch task", http.MethodPatch, "/me/tasks/task-1", `{"title":"Updated"}`, `"updated":true`, http.StatusOK},
		{"Delete task", http.MethodDelete, "/me/tasks/task-1", "", "", http.StatusNoContent},
		{"Toggle task", http.MethodPost, "/me/tasks/task-1/toggle", "", `"toggled":true`, http.StatusOK},
		{"Reorder tasks", http.MethodPost, "/me/tasks/reorder", `{"order":["task-1"]}`, `"reordered":true`, http.StatusOK},
		{"Link task event", http.MethodPost, "/me/tasks/task-1/event-link", `{"eventId":"event-1"}`, `"linked":true`, http.StatusCreated},
		{"Delete task event link", http.MethodDelete, "/me/tasks/task-1/event-link", "", "", http.StatusNoContent},
		{"List habits", http.MethodGet, "/me/habits", "", "habit-1", http.StatusOK},
		{"Create habit", http.MethodPost, "/me/habits", `{"title":"New Habit"}`, `"id":"habit-2"`, http.StatusCreated},
		{"Get habit", http.MethodGet, "/me/habits/habit-1", "", `"id":"habit-1"`, http.StatusOK},
		{"Patch habit", http.MethodPatch, "/me/habits/habit-1", `{"title":"Updated"}`, `"updated":true`, http.StatusOK},
		{"Delete habit", http.MethodDelete, "/me/habits/habit-1", "", "", http.StatusNoContent},
		{"Habit calendar summary", http.MethodGet, "/me/habits/calendar-summary?from=2026-04-20&to=2026-04-26", "", `"summary":"ok"`, http.StatusOK},
		{"Create shared habit", http.MethodPost, "/me/shared-habits", `{"partnerUserId":"user-2"}`, `"sharedHabitPairId":"pair-123"`, http.StatusCreated},
		{"Get shared habit", http.MethodGet, "/me/shared-habits/pair-123", "", `"sharedHabitPairId":"pair-123"`, http.StatusOK},
		{"Remind shared habit", http.MethodPost, "/me/shared-habits/pair-123/remind", `{"message":"Hi"}`, `"reminded":true`, http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
			resp := httptest.NewRecorder()

			server.Router().ServeHTTP(resp, req)

			if resp.Code != tt.code {
				t.Fatalf("%s %s: expected status %d, got %d", tt.method, tt.path, tt.code, resp.Code)
			}
			if tt.want != "" {
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), tt.want) {
					t.Fatalf("%s %s: unexpected body: %s", tt.method, tt.path, string(body))
				}
			}
		})
	}
}

func TestGatewayPropagatesRequestIDToDownstream(t *testing.T) {
	const reqID = "req-12345"
	coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") != reqID {
			http.Error(w, "missing request id", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer coreServer.Close()

	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  "http://auth.invalid",
		CoreURL:                  coreServer.URL,
		SocialURL:                "http://social.invalid",
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/me/tasks?date=2026-04-26", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-ID", reqID)
	resp := httptest.NewRecorder()

	server.Router().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d, body=%s", resp.Code, string(body))
	}
	if got := resp.Header().Get("X-Request-ID"); got != reqID {
		t.Fatalf("expected response X-Request-ID=%s, got %s", reqID, got)
	}
}

func TestGatewayPublicAuthRoutesDoNotRequireToken(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer authServer.Close()

	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  authServer.URL,
		CoreURL:                  "http://core.invalid",
		SocialURL:                "http://social.invalid",
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	paths := []string{
		"/auth/register",
		"/auth/login",
		"/auth/refresh",
		"/auth/password-reset/request",
		"/auth/password-reset/confirm",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			resp := httptest.NewRecorder()

			server.Router().ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("expected status 200 for public route %s, got %d", path, resp.Code)
			}
		})
	}
}

func TestGatewayProtectedRouteReturnsUnauthorizedWithoutOrInvalidToken(t *testing.T) {
	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  "http://auth.invalid",
		CoreURL:                  "http://core.invalid",
		SocialURL:                "http://social.invalid",
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	t.Run("without token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		resp := httptest.NewRecorder()
		server.Router().ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.Code)
		}
	})

	t.Run("with invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		resp := httptest.NewRecorder()
		server.Router().ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.Code)
		}
	})
}

func TestGatewayProtectedRoutesWithValidTokenAreRoutedToServices(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"auth"}`))
	}))
	defer authServer.Close()

	coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/tasks" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"core"}`))
	}))
	defer coreServer.Close()

	socialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/friends" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"social"}`))
	}))
	defer socialServer.Close()

	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  authServer.URL,
		CoreURL:                  coreServer.URL,
		SocialURL:                socialServer.URL,
		HTTPClientTimeoutSeconds: 5,
	}
	server := New(cfg, logger.New("gateway-test"))

	token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path      string
		wantToken string
	}{
		{path: "/me", wantToken: `"service":"auth"`},
		{path: "/me/tasks?date=2026-04-26", wantToken: `"service":"core"`},
		{path: "/me/friends", wantToken: `"service":"social"`},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp := httptest.NewRecorder()

			server.Router().ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", tt.path, resp.Code)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tt.wantToken) {
				t.Fatalf("unexpected body for %s: %s", tt.path, string(body))
			}
		})
	}
}

func TestGatewayDownstreamErrorsArePropagated(t *testing.T) {
	tests := []struct {
		name        string
		downstream  int
		wantGateway int
	}{
		{name: "400", downstream: http.StatusBadRequest, wantGateway: http.StatusBadRequest},
		{name: "404", downstream: http.StatusNotFound, wantGateway: http.StatusNotFound},
		{name: "500", downstream: http.StatusInternalServerError, wantGateway: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.downstream)
				_, _ = w.Write([]byte(`{"message":"downstream"}`))
			}))
			defer coreServer.Close()

			cfg := config.Config{
				JWTSecret:                "test-secret",
				AuthURL:                  "http://auth.invalid",
				CoreURL:                  coreServer.URL,
				SocialURL:                "http://social.invalid",
				HTTPClientTimeoutSeconds: 5,
			}
			server := New(cfg, logger.New("gateway-test"))

			token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
			if err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest(http.MethodGet, "/me/tasks?date=2026-04-26", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp := httptest.NewRecorder()

			server.Router().ServeHTTP(resp, req)

			if resp.Code != tt.wantGateway {
				t.Fatalf("expected status %d, got %d", tt.wantGateway, resp.Code)
			}
		})
	}
}

func TestGatewayReturnsBadGatewayWhenDownstreamUnavailable(t *testing.T) {
	cfg := config.Config{
		JWTSecret:                "test-secret",
		AuthURL:                  "http://auth.invalid",
		CoreURL:                  "http://127.0.0.1:1",
		SocialURL:                "http://social.invalid",
		HTTPClientTimeoutSeconds: 1,
	}
	server := New(cfg, logger.New("gateway-test"))

	token, err := authjwt.IssueAccessToken("requester", cfg.JWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/me/tasks?date=2026-04-26", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()

	server.Router().ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.Code)
	}
}

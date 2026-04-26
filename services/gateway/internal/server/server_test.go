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
	server := New(cfg)

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
	server := New(cfg)

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

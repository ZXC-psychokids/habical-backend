package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/libs/logger"
	"habical/backend/services/core/internal/config"
)

type errorEnvelope struct {
	Message string `json:"message"`
}

func newCoreServerForTests() *Server {
	cfg := config.Config{
		Port:        "4012",
		PostgresDSN: "postgres://unused",
		JWTSecret:   "test-secret",
	}
	return New(cfg, nil, logger.New("core-test"))
}

func testToken(t *testing.T) string {
	t.Helper()
	token, err := authjwt.IssueAccessToken("user-1", "test-secret", time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return token
}

func coreRequest(t *testing.T, h http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case nil:
		raw = nil
	case string:
		raw = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		raw = b
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertCoreError(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	var er errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("invalid error json: %v body=%s", err, rec.Body.String())
	}
	if er.Message == "" {
		t.Fatalf("message must not be empty")
	}
}

func TestCoreProtectedRoutesReturnUnauthorizedWithoutOrInvalidToken(t *testing.T) {
	router := newCoreServerForTests().Router()

	routes := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/me/events?from=2026-05-01T00:00:00Z&to=2026-05-02T00:00:00Z", nil},
		{http.MethodPost, "/me/events", map[string]any{}},
		{http.MethodGet, "/me/events/e1", nil},
		{http.MethodPatch, "/me/events/e1", map[string]any{}},
		{http.MethodDelete, "/me/events/e1", nil},
		{http.MethodPost, "/me/events/e1/move", map[string]any{}},
		{http.MethodGet, "/me/event-categories", nil},
		{http.MethodPost, "/me/event-categories", map[string]any{}},
		{http.MethodPatch, "/me/event-categories/c1", map[string]any{}},
		{http.MethodDelete, "/me/event-categories/c1", nil},
		{http.MethodGet, "/users/u2/events?date=2026-05-14", nil},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path+" no token", func(t *testing.T) {
			rec := coreRequest(t, router, rt.method, rt.path, rt.body, "")
			assertCoreError(t, rec, http.StatusUnauthorized)
		})
		t.Run(rt.method+" "+rt.path+" invalid token", func(t *testing.T) {
			rec := coreRequest(t, router, rt.method, rt.path, rt.body, "invalid.token")
			assertCoreError(t, rec, http.StatusUnauthorized)
		})
	}
}

func TestGetMyEventsReturnsBadRequestForMissingOrInvalidQuery(t *testing.T) {
	router := newCoreServerForTests().Router()
	token := testToken(t)

	rec := coreRequest(t, router, http.MethodGet, "/me/events?to=2026-05-02T00:00:00Z", nil, token)
	assertCoreError(t, rec, http.StatusBadRequest)

	rec = coreRequest(t, router, http.MethodGet, "/me/events?from=2026-05-01T00:00:00Z", nil, token)
	assertCoreError(t, rec, http.StatusBadRequest)

	rec = coreRequest(t, router, http.MethodGet, "/me/events?from=bad&to=2026-05-02T00:00:00Z", nil, token)
	assertCoreError(t, rec, http.StatusBadRequest)
}

func TestCreateEventValidationReturnsBadRequest(t *testing.T) {
	router := newCoreServerForTests().Router()
	token := testToken(t)

	t.Run("invalid json", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", "{", token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("empty title", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", map[string]any{
			"title":        "",
			"startsAt":     "2026-05-14T10:00:00Z",
			"endsAt":       "2026-05-14T11:00:00Z",
			"scheduleType": "daily",
			"intervalDays": 1,
			"categoryId":   "c1",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid startsAt format", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", map[string]any{
			"title":        "Event",
			"startsAt":     "bad",
			"endsAt":       "2026-05-14T11:00:00Z",
			"scheduleType": "daily",
			"intervalDays": 1,
			"categoryId":   "c1",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("endsAt before startsAt", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", map[string]any{
			"title":        "Event",
			"startsAt":     "2026-05-14T12:00:00Z",
			"endsAt":       "2026-05-14T11:00:00Z",
			"scheduleType": "daily",
			"intervalDays": 1,
			"categoryId":   "c1",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid scheduleType", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", map[string]any{
			"title":        "Event",
			"startsAt":     "2026-05-14T10:00:00Z",
			"endsAt":       "2026-05-14T11:00:00Z",
			"scheduleType": "yearly",
			"intervalDays": 1,
			"categoryId":   "c1",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("intervalDays less than 1", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", map[string]any{
			"title":        "Event",
			"startsAt":     "2026-05-14T10:00:00Z",
			"endsAt":       "2026-05-14T11:00:00Z",
			"scheduleType": "daily",
			"intervalDays": 0,
			"categoryId":   "c1",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("weekdays schedule without weekdays", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/events", map[string]any{
			"title":        "Event",
			"startsAt":     "2026-05-14T10:00:00Z",
			"endsAt":       "2026-05-14T11:00:00Z",
			"scheduleType": "weekdays",
			"intervalDays": 1,
			"weekdays":     []int{},
			"categoryId":   "c1",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
}

func TestCreateCategoryValidationReturnsBadRequest(t *testing.T) {
	router := newCoreServerForTests().Router()
	token := testToken(t)

	t.Run("invalid json", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/event-categories", "{", token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("empty title", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/event-categories", map[string]any{
			"title": "",
			"color": "#fff",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
	t.Run("empty color", func(t *testing.T) {
		rec := coreRequest(t, router, http.MethodPost, "/me/event-categories", map[string]any{
			"title": "Work",
			"color": "",
		}, token)
		assertCoreError(t, rec, http.StatusBadRequest)
	})
}

func TestValidateWeekdaysRule(t *testing.T) {
	if err := validateWeekdaysRule("weekdays", []int{1, 3, 5}); err != nil {
		t.Fatalf("unexpected error for valid weekdays rule: %v", err)
	}
	if err := validateWeekdaysRule("weekdays", nil); err == nil {
		t.Fatalf("expected error for missing weekdays when scheduleType=weekdays")
	}
	if err := validateWeekdaysRule("daily", []int{1}); err == nil {
		t.Fatalf("expected error for non-empty weekdays when scheduleType!=weekdays")
	}
	if err := validateWeekdaysRule("weekdays", []int{0}); err == nil {
		t.Fatalf("expected error for weekday out of range")
	}
}

func TestGetFriendEventsInvalidQueryCombinationsReturnBadRequest(t *testing.T) {
	router := newCoreServerForTests().Router()
	token := testToken(t)

	invalidPaths := []string{
		"/users/u2/events",
		"/users/u2/events?from=2026-05-01T00:00:00Z",
		"/users/u2/events?to=2026-05-02T00:00:00Z",
		"/users/u2/events?date=2026-05-14&from=2026-05-01T00:00:00Z&to=2026-05-02T00:00:00Z",
		"/users/u2/events?date=bad",
		"/users/u2/events?from=bad&to=2026-05-02T00:00:00Z",
	}

	for _, path := range invalidPaths {
		t.Run(path, func(t *testing.T) {
			rec := coreRequest(t, router, http.MethodGet, path, nil, token)
			assertCoreError(t, rec, http.StatusBadRequest)
		})
	}
}

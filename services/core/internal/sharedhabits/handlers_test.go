package sharedhabits

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type errMessage struct {
	Message string `json:"message"`
}

func newSharedHabitsRouter() http.Handler {
	r := chi.NewRouter()
	New(nil).RegisterRoutes(r)
	return r
}

func withSharedUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userIDContextKey, "user-1")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sharedReq(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertSharedErr(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	var er errMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("invalid error json: %v body=%s", err, rec.Body.String())
	}
	if er.Message == "" {
		t.Fatalf("message must not be empty")
	}
}

func TestSharedHabitsProtectedRoutesReturnUnauthorizedWithoutUser(t *testing.T) {
	h := newSharedHabitsRouter()
	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/me/shared-habits", map[string]any{}},
		{http.MethodGet, "/me/shared-habits/pair-1", nil},
		{http.MethodPost, "/me/shared-habits/pair-1/remind", map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := sharedReq(t, h, tt.method, tt.path, tt.body)
			assertSharedErr(t, rec, http.StatusUnauthorized)
		})
	}
}

func TestCreateSharedHabitValidationBadRequest(t *testing.T) {
	h := withSharedUser(newSharedHabitsRouter())

	t.Run("invalid json", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits", "{")
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
	t.Run("empty friend user id", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits", map[string]any{
			"friendUserId": "",
			"title":        "Habit",
			"color":        "green",
			"scheduleType": "daily",
			"intervalDays": 1,
		})
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
	t.Run("friend equals current user", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits", map[string]any{
			"friendUserId": "user-1",
			"title":        "Habit",
			"color":        "green",
			"scheduleType": "daily",
			"intervalDays": 1,
		})
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid payload fields", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits", map[string]any{
			"friendUserId": "user-2",
			"title":        "",
			"color":        "",
			"scheduleType": "monthly",
			"intervalDays": 0,
		})
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
}

func TestGetSharedHabitReturnsBadRequestForEmptyPairID(t *testing.T) {
	h := withSharedUser(newSharedHabitsRouter())
	rec := sharedReq(t, h, http.MethodGet, "/me/shared-habits/%20", nil)
	assertSharedErr(t, rec, http.StatusBadRequest)
}

func TestRemindSharedHabitValidationBadRequest(t *testing.T) {
	h := withSharedUser(newSharedHabitsRouter())

	t.Run("empty pair id", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits/%20/remind", map[string]any{
			"taskId": "task-1",
		})
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid json", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits/pair-1/remind", "{")
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
	t.Run("empty task id", func(t *testing.T) {
		rec := sharedReq(t, h, http.MethodPost, "/me/shared-habits/pair-1/remind", map[string]any{
			"taskId": "",
		})
		assertSharedErr(t, rec, http.StatusBadRequest)
	})
}

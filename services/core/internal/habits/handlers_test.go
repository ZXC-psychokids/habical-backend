package habits

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type apiErr struct {
	Message string `json:"message"`
}

func newHabitsRouter() http.Handler {
	r := chi.NewRouter()
	New(nil).RegisterRoutes(r)
	return r
}

func withHabitsUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userIDContextKey, "user-1")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func habitsReq(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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

func assertHabitErr(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	var er apiErr
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("invalid error json: %v body=%s", err, rec.Body.String())
	}
	if er.Message == "" {
		t.Fatalf("message must not be empty")
	}
}

func TestHabitsProtectedRoutesReturnUnauthorizedWithoutUser(t *testing.T) {
	h := newHabitsRouter()
	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/me/habits", nil},
		{http.MethodPost, "/me/habits", map[string]any{}},
		{http.MethodGet, "/me/habits/calendar-summary?from=2026-05-01&to=2026-05-31", nil},
		{http.MethodGet, "/me/habits/h1", nil},
		{http.MethodPatch, "/me/habits/h1", map[string]any{}},
		{http.MethodDelete, "/me/habits/h1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := habitsReq(t, h, tt.method, tt.path, tt.body)
			assertHabitErr(t, rec, http.StatusUnauthorized)
		})
	}
}

func TestCreateHabitValidationBadRequest(t *testing.T) {
	h := withHabitsUser(newHabitsRouter())

	t.Run("invalid json", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPost, "/me/habits", "{")
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("empty title", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPost, "/me/habits", map[string]any{
			"title":        "",
			"color":        "green",
			"scheduleType": "daily",
			"intervalDays": 1,
		})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("empty color", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPost, "/me/habits", map[string]any{
			"title":        "Habit",
			"color":        "",
			"scheduleType": "daily",
			"intervalDays": 1,
		})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid schedule type", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPost, "/me/habits", map[string]any{
			"title":        "Habit",
			"color":        "green",
			"scheduleType": "monthly",
			"intervalDays": 1,
		})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("weekdays schedule without weekdays", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPost, "/me/habits", map[string]any{
			"title":        "Habit",
			"color":        "green",
			"scheduleType": "weekdays",
			"intervalDays": 1,
			"weekdays":     []int{},
		})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
}

func TestGetHabitCalendarSummaryValidationBadRequest(t *testing.T) {
	h := withHabitsUser(newHabitsRouter())

	t.Run("missing from", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodGet, "/me/habits/calendar-summary?to=2026-05-31", nil)
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("missing to", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodGet, "/me/habits/calendar-summary?from=2026-05-01", nil)
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid range", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodGet, "/me/habits/calendar-summary?from=2026-06-01&to=2026-05-01", nil)
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
}

func TestGetAndDeleteHabitReturnBadRequestForEmptyHabitID(t *testing.T) {
	h := withHabitsUser(newHabitsRouter())

	rec := habitsReq(t, h, http.MethodGet, "/me/habits/%20", nil)
	assertHabitErr(t, rec, http.StatusBadRequest)

	rec = habitsReq(t, h, http.MethodDelete, "/me/habits/%20", nil)
	assertHabitErr(t, rec, http.StatusBadRequest)
}

func TestPatchHabitValidationBadRequest(t *testing.T) {
	h := withHabitsUser(newHabitsRouter())
	path := "/me/habits/habit-1"

	t.Run("invalid json", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPatch, path, "{")
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("no fields", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPatch, path, map[string]any{})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("empty title", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPatch, path, map[string]any{"title": ""})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid schedule type", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPatch, path, map[string]any{"scheduleType": "monthly"})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("negative interval", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPatch, path, map[string]any{"intervalDays": 0})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid weekday value", func(t *testing.T) {
		rec := habitsReq(t, h, http.MethodPatch, path, map[string]any{
			"scheduleType": "weekdays",
			"weekdays":     []int{0, 8},
		})
		assertHabitErr(t, rec, http.StatusBadRequest)
	})
}

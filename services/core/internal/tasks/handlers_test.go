package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type errResp struct {
	Message string `json:"message"`
}

func newTasksRouter() http.Handler {
	r := chi.NewRouter()
	New(nil).RegisterRoutes(r)
	return r
}

func withUserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userIDContextKey, "user-1")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func doReq(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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

func assertStatusAndMessage(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	var er errResp
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("invalid error json: %v body=%s", err, rec.Body.String())
	}
	if er.Message == "" {
		t.Fatalf("message must not be empty")
	}
}

func TestTasksProtectedRoutesReturnUnauthorizedWithoutUserContext(t *testing.T) {
	h := newTasksRouter()
	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/me/tasks?date=2026-05-14", nil},
		{http.MethodPost, "/me/tasks", map[string]any{}},
		{http.MethodPost, "/me/tasks/reorder", map[string]any{}},
		{http.MethodGet, "/me/tasks/task-1", nil},
		{http.MethodPatch, "/me/tasks/task-1", map[string]any{}},
		{http.MethodDelete, "/me/tasks/task-1", nil},
		{http.MethodPost, "/me/tasks/task-1/toggle", nil},
		{http.MethodPost, "/me/tasks/task-1/event-link", map[string]any{}},
		{http.MethodDelete, "/me/tasks/task-1/event-link", nil},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := doReq(t, h, tt.method, tt.path, tt.body)
			assertStatusAndMessage(t, rec, http.StatusUnauthorized)
		})
	}
}

func TestListTasksReturnsBadRequestWhenDateMissing(t *testing.T) {
	h := withUserID(newTasksRouter())
	rec := doReq(t, h, http.MethodGet, "/me/tasks", nil)
	assertStatusAndMessage(t, rec, http.StatusBadRequest)
}

func TestCreateTaskValidationBadRequest(t *testing.T) {
	h := withUserID(newTasksRouter())

	t.Run("invalid json", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks", "{")
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("empty title", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks", map[string]any{
			"title":    "",
			"taskDate": "2026-05-14",
			"position": 0,
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid date", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks", map[string]any{
			"title":    "task",
			"taskDate": "bad",
			"position": 0,
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("negative position", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks", map[string]any{
			"title":    "task",
			"taskDate": "2026-05-14",
			"position": -1,
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
}

func TestReorderTasksValidationBadRequest(t *testing.T) {
	h := withUserID(newTasksRouter())

	t.Run("invalid json", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks/reorder", "{")
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("empty items", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks/reorder", map[string]any{
			"items": []any{},
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("empty taskId", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks/reorder", map[string]any{
			"items": []map[string]any{{"taskId": "", "position": 0, "taskDate": "2026-05-14"}},
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("negative position", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks/reorder", map[string]any{
			"items": []map[string]any{{"taskId": "t1", "position": -1, "taskDate": "2026-05-14"}},
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("duplicate taskId", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, "/me/tasks/reorder", map[string]any{
			"items": []map[string]any{
				{"taskId": "t1", "position": 0, "taskDate": "2026-05-14"},
				{"taskId": "t1", "position": 1, "taskDate": "2026-05-14"},
			},
		})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
}

func TestGetTaskReturnsBadRequestForEmptyTaskID(t *testing.T) {
	h := withUserID(newTasksRouter())
	rec := doReq(t, h, http.MethodGet, "/me/tasks/%20", nil)
	assertStatusAndMessage(t, rec, http.StatusBadRequest)
}

func TestPatchTaskValidationBadRequest(t *testing.T) {
	h := withUserID(newTasksRouter())
	path := "/me/tasks/task-1"

	t.Run("invalid json", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPatch, path, "{")
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("no fields", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPatch, path, map[string]any{})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("empty title", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPatch, path, map[string]any{"title": ""})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("invalid date", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPatch, path, map[string]any{"taskDate": "bad"})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("negative position", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPatch, path, map[string]any{"position": -1})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
}

func TestLinkTaskEventValidationBadRequest(t *testing.T) {
	h := withUserID(newTasksRouter())
	path := "/me/tasks/task-1/event-link"

	t.Run("invalid json", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, path, "{")
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
	t.Run("empty eventId", func(t *testing.T) {
		rec := doReq(t, h, http.MethodPost, path, map[string]any{"eventId": ""})
		assertStatusAndMessage(t, rec, http.StatusBadRequest)
	})
}

package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONWritesStatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()

	payload := map[string]any{"ok": true, "value": 42}
	WriteJSON(rec, http.StatusCreated, payload)

	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status code: got=%d want=%d", rec.Code, http.StatusCreated)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("unexpected content-type: got=%q", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got["ok"] != true {
		t.Fatalf("unexpected JSON payload: %v", got)
	}
}

func TestWriteErrorWritesMessageEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteError(rec, http.StatusBadRequest, "bad request")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status code: got=%d want=%d", rec.Code, http.StatusBadRequest)
	}

	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got.Message != "bad request" {
		t.Fatalf("unexpected error message: got=%q", got.Message)
	}
}

func TestWriteJSONNoContentHasNoBody(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusNoContent, nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status code: got=%d want=%d", rec.Code, http.StatusNoContent)
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("expected empty body, got=%q", rec.Body.String())
	}
}

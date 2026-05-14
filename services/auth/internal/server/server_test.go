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
	"habical/backend/services/auth/internal/config"
)

type apiError struct {
	Message string `json:"message"`
}

func newTestServer() *Server {
	cfg := config.Config{
		Port:             "4011",
		PostgresDSN:      "postgres://unused",
		JWTSecret:        "test-secret",
		AvatarDir:        "test-avatars",
		AvatarBaseURL:    "http://localhost/avatars",
		AccessTTL:        0,
		RefreshTTL:       0,
		PasswordResetTTL: 0,
	}
	return New(cfg, nil, logger.New("auth-test"))
}

func doJSONRequest(t *testing.T, h http.Handler, method, path string, body any, authHeader string) *httptest.ResponseRecorder {
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
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var errResp apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("invalid error JSON: %v; body=%s", err, rec.Body.String())
	}
	if errResp.Message == "" {
		t.Fatalf("error response message is empty")
	}
}

func TestRegisterReturnsBadRequestForInvalidBodyAndFields(t *testing.T) {
	router := newTestServer().Router()

	t.Run("invalid json", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/register", "{", "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})

	t.Run("empty email", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/register", map[string]any{
			"email":                "",
			"handle":               "user",
			"password":             "pass",
			"passwordConfirmation": "pass",
		}, "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})

	t.Run("password mismatch", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/register", map[string]any{
			"email":                "user@example.com",
			"handle":               "user",
			"password":             "pass1",
			"passwordConfirmation": "pass2",
		}, "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})
}

func TestLoginReturnsBadRequestForInvalidBodyAndFields(t *testing.T) {
	router := newTestServer().Router()

	t.Run("invalid json", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/login", "{", "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})

	t.Run("empty login", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/login", map[string]any{
			"login":    "",
			"password": "pass",
		}, "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})

	t.Run("empty password", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/login", map[string]any{
			"login":    "user@example.com",
			"password": "",
		}, "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})
}

func TestRefreshAndLogoutReturnUnauthorizedForEmptyToken(t *testing.T) {
	router := newTestServer().Router()

	rec := doJSONRequest(t, router, http.MethodPost, "/auth/refresh", map[string]any{
		"refreshToken": "",
	}, "")
	assertErrorResponse(t, rec, http.StatusUnauthorized)

	rec = doJSONRequest(t, router, http.MethodPost, "/auth/logout", map[string]any{
		"refreshToken": "",
	}, "")
	assertErrorResponse(t, rec, http.StatusUnauthorized)
}

func TestProtectedRoutesReturnUnauthorizedWithoutOrInvalidToken(t *testing.T) {
	router := newTestServer().Router()

	protected := []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/me"},
		{method: http.MethodPatch, path: "/me/profile", body: map[string]any{"email": "a@b.com"}},
		{method: http.MethodGet, path: "/me/settings"},
		{method: http.MethodPatch, path: "/me/settings/privacy", body: map[string]any{"shareHabits": true}},
		{method: http.MethodPatch, path: "/me/settings/notifications", body: map[string]any{"notifyFriendRequests": true}},
		{method: http.MethodPatch, path: "/me/settings/calendar", body: map[string]any{"timezone": "Europe/Moscow"}},
	}

	for _, tc := range protected {
		t.Run(tc.method+" "+tc.path+" without token", func(t *testing.T) {
			rec := doJSONRequest(t, router, tc.method, tc.path, tc.body, "")
			assertErrorResponse(t, rec, http.StatusUnauthorized)
		})
		t.Run(tc.method+" "+tc.path+" with invalid token", func(t *testing.T) {
			rec := doJSONRequest(t, router, tc.method, tc.path, tc.body, "Bearer invalid")
			assertErrorResponse(t, rec, http.StatusUnauthorized)
		})
	}
}

func TestSettingsPatchEndpointsReturnBadRequestForInvalidJSON(t *testing.T) {
	router := newTestServer().Router()
	token, err := authjwt.IssueAccessToken("user-1", "test-secret", time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	validToken := "Bearer " + token

	paths := []string{
		"/me/profile",
		"/me/settings/privacy",
		"/me/settings/notifications",
		"/me/settings/calendar",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := doJSONRequest(t, router, http.MethodPatch, path, "{", validToken)
			assertErrorResponse(t, rec, http.StatusBadRequest)
		})
	}
}

func TestPasswordResetEndpointsValidation(t *testing.T) {
	router := newTestServer().Router()

	t.Run("request invalid json", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/password-reset/request", "{", "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})
	t.Run("request empty email", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/password-reset/request", map[string]any{
			"email": "",
		}, "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})
	t.Run("confirm invalid json", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/password-reset/confirm", "{", "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})
	t.Run("confirm empty fields", func(t *testing.T) {
		rec := doJSONRequest(t, router, http.MethodPost, "/auth/password-reset/confirm", map[string]any{
			"token":                   "",
			"newPassword":             "",
			"newPasswordConfirmation": "",
		}, "")
		assertErrorResponse(t, rec, http.StatusBadRequest)
	})
}

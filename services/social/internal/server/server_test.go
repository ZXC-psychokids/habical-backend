package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"habical/backend/libs/authjwt"
	"habical/backend/services/social/internal/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(
		config.Config{JWTSecret: "test-secret"},
		nil,
		slog.New(slog.DiscardHandler),
	)
}

func makeReq(method, path string, token string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func makeJSONReq(method, path string, body []byte, token string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func mustToken(t *testing.T, userID string) string {
	t.Helper()
	token, err := authjwt.IssueAccessToken(userID, "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return token
}

func TestProtectedRoutesReturnUnauthorizedWithoutOrInvalidToken(t *testing.T) {
	srv := newTestServer(t)
	router := srv.Router()

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/me/friends"},
		{http.MethodDelete, "/me/friends/u2"},
		{http.MethodGet, "/me/friend-invites"},
		{http.MethodPost, "/me/friend-invites"},
		{http.MethodPost, "/me/friend-invites/invite-1/accept"},
		{http.MethodPost, "/me/friend-invites/invite-1/reject"},
		{http.MethodGet, "/me/feed"},
		{http.MethodGet, "/internal/friendships/u1/with/u2"},
	}

	for _, tc := range routes {
		t.Run(tc.method+" "+tc.path+" without token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, makeReq(tc.method, tc.path, ""))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})

		t.Run(tc.method+" "+tc.path+" with invalid token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, makeReq(tc.method, tc.path, "invalid-token"))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})
	}
}

func TestCreateInviteReturnsBadRequestForInvalidBody(t *testing.T) {
	srv := newTestServer(t)
	router := srv.Router()
	token, err := authjwt.IssueAccessToken("u1", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}

	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, makeJSONReq(http.MethodPost, "/me/friend-invites", []byte(`{`), token))
	if rec1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, makeJSONReq(http.MethodPost, "/me/friend-invites", []byte(`{"handle":"   "}`), token))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty handle, got %d", rec2.Code)
	}
}

func TestGetFeedReturnsBadRequestForInvalidLimitAndCursor(t *testing.T) {
	srv := newTestServer(t)
	router := srv.Router()

	token, err := authjwt.IssueAccessToken("u1", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}

	tests := []string{
		"/me/feed?limit=0",
		"/me/feed?limit=-1",
		"/me/feed?limit=101",
		"/me/feed?limit=abc",
		"/me/feed?cursor=@@@",
		"/me/feed?cursor=" + base64.RawURLEncoding.EncodeToString([]byte("-1")),
		"/me/feed?cursor=" + base64.RawURLEncoding.EncodeToString([]byte("abc")),
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, makeReq(http.MethodGet, path, token))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}
}

func TestFriendsEndpointsSuccessAndNotFound(t *testing.T) {
	srv := newTestServer(t)
	srv.listFriendsFn = func(ctx context.Context, userID string) ([]friendListItem, error) {
		return []friendListItem{{User: publicUser{ID: "u2", Handle: "bob", AvatarURL: "x"}, SharedHabitsCount: 1}}, nil
	}
	srv.deleteFriendFn = func(ctx context.Context, userID string, friendUserID string) (bool, error) {
		return friendUserID == "u2", nil
	}
	router := srv.Router()
	token := mustToken(t, "u1")

	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, makeReq(http.MethodGet, "/me/friends", token))
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, makeReq(http.MethodDelete, "/me/friends/u2", token))
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec2.Code)
	}

	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, makeReq(http.MethodDelete, "/me/friends/u404", token))
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec3.Code)
	}
}

func TestFriendInvitesEndpoints(t *testing.T) {
	srv := newTestServer(t)
	srv.listInvitesFn = func(ctx context.Context, userID string) ([]friendInviteResponse, error) {
		return []friendInviteResponse{{ID: "i1", Status: "pending", Sender: publicUser{ID: "u2", Handle: "bob", AvatarURL: "x"}, CreatedAt: time.Now().UTC()}}, nil
	}
	srv.findUserByHandleFn = func(ctx context.Context, handle string) (string, error) {
		switch handle {
		case "missing":
			return "", errNotFound
		case "self":
			return "u1", nil
		default:
			return "u2", nil
		}
	}
	srv.friendshipExistsFn = func(ctx context.Context, userID string, otherUserID string) (bool, error) {
		return otherUserID == "u-friend", nil
	}
	srv.pendingInviteFn = func(ctx context.Context, userID string, otherUserID string) (bool, error) {
		return otherUserID == "u2-pending", nil
	}
	srv.createInviteFn = func(ctx context.Context, senderID string, receiverID string) (string, error) {
		return "invite-1", nil
	}
	srv.acceptInviteFn = func(ctx context.Context, inviteID string, currentUserID string) (string, error) {
		switch inviteID {
		case "nf":
			return "", errNotFound
		case "forbidden":
			return "", errForbidden
		case "conflict":
			return "", errConflict
		default:
			return "friendship-1", nil
		}
	}
	srv.rejectInviteFn = func(ctx context.Context, inviteID string, currentUserID string) error {
		switch inviteID {
		case "nf":
			return errNotFound
		case "forbidden":
			return errForbidden
		case "conflict":
			return errConflict
		default:
			return nil
		}
	}
	router := srv.Router()
	token := mustToken(t, "u1")

	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, makeReq(http.MethodGet, "/me/friend-invites", token))
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, makeJSONReq(http.MethodPost, "/me/friend-invites", []byte(`{"handle":"missing"}`), token))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec2.Code)
	}

	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, makeJSONReq(http.MethodPost, "/me/friend-invites", []byte(`{"handle":"self"}`), token))
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec3.Code)
	}

	rec4 := httptest.NewRecorder()
	router.ServeHTTP(rec4, makeJSONReq(http.MethodPost, "/me/friend-invites", []byte(`{"handle":"ok"}`), token))
	if rec4.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec4.Code)
	}

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/me/friend-invites/ok/accept", http.StatusOK},
		{"/me/friend-invites/nf/accept", http.StatusNotFound},
		{"/me/friend-invites/forbidden/accept", http.StatusForbidden},
		{"/me/friend-invites/conflict/accept", http.StatusConflict},
		{"/me/friend-invites/ok/reject", http.StatusNoContent},
		{"/me/friend-invites/nf/reject", http.StatusNotFound},
		{"/me/friend-invites/forbidden/reject", http.StatusForbidden},
		{"/me/friend-invites/conflict/reject", http.StatusConflict},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, makeReq(http.MethodPost, tc.path, token))
		if rec.Code != tc.code {
			t.Fatalf("%s expected %d, got %d", tc.path, tc.code, rec.Code)
		}
	}
}

func TestFeedEndpointSuccessAndInternalError(t *testing.T) {
	srv := newTestServer(t)
	srv.getFeedFn = func(ctx context.Context, userID string, limit int, offset int) ([]feedItemResponse, error) {
		if offset == 999 {
			return nil, errors.New("boom")
		}
		return []feedItemResponse{
			{ID: "f1", Type: "friend_added", Actor: publicUser{ID: "u2", Handle: "bob", AvatarURL: "x"}, CreatedAt: time.Now().UTC()},
			{ID: "f2", Type: "friend_added", Actor: publicUser{ID: "u3", Handle: "ann", AvatarURL: "x"}, CreatedAt: time.Now().UTC()},
		}, nil
	}
	router := srv.Router()
	token := mustToken(t, "u1")

	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, makeReq(http.MethodGet, "/me/feed?limit=1", token))
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	cur := encodeCursor(999)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, makeReq(http.MethodGet, "/me/feed?cursor="+cur, token))
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec2.Code)
	}
}

func TestDecodeEncodeCursor(t *testing.T) {
	n, err := decodeCursor("")
	if err != nil || n != 0 {
		t.Fatalf("expected empty cursor -> 0,nil got %d,%v", n, err)
	}

	cur := encodeCursor(42)
	got, err := decodeCursor(cur)
	if err != nil {
		t.Fatalf("decode encoded cursor: %v", err)
	}
	if got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}

	if _, err := decodeCursor("%%%"); err == nil {
		t.Fatal("expected error for malformed cursor")
	}
}

func TestNormalizePair(t *testing.T) {
	a, b := normalizePair("b", "a")
	if a != "a" || b != "b" {
		t.Fatalf("expected a,b got %s,%s", a, b)
	}
}

func TestUserIDFromContext(t *testing.T) {
	if got := userIDFromContext(context.Background()); got != "" {
		t.Fatalf("expected empty user id, got %q", got)
	}
}

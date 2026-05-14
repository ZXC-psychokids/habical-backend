package authjwt

import (
	"testing"
	"time"
)

func TestIssueAndParseAccessToken(t *testing.T) {
	secret := "test-secret"
	wantUserID := "user-123"

	token, err := IssueAccessToken(wantUserID, secret, time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken() returned error: %v", err)
	}
	if token == "" {
		t.Fatalf("IssueAccessToken() returned empty token")
	}

	gotUserID, err := ParseAccessToken(token, secret)
	if err != nil {
		t.Fatalf("ParseAccessToken() returned error: %v", err)
	}
	if gotUserID != wantUserID {
		t.Fatalf("unexpected userID: got=%q want=%q", gotUserID, wantUserID)
	}
}

func TestParseAccessTokenRejectsWrongSecret(t *testing.T) {
	token, err := IssueAccessToken("user-123", "good-secret", time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken() returned error: %v", err)
	}

	if _, err := ParseAccessToken(token, "bad-secret"); err == nil {
		t.Fatalf("expected error for token parsed with wrong secret")
	}
}

func TestParseAccessTokenRejectsInvalidAndEmptyToken(t *testing.T) {
	t.Run("invalid token string", func(t *testing.T) {
		if _, err := ParseAccessToken("not-a-jwt", "secret"); err == nil {
			t.Fatalf("expected error for invalid token string")
		}
	})

	t.Run("empty token string", func(t *testing.T) {
		if _, err := ParseAccessToken("", "secret"); err == nil {
			t.Fatalf("expected error for empty token string")
		}
	})
}

func TestParseAccessTokenRejectsExpiredToken(t *testing.T) {
	token, err := IssueAccessToken("user-123", "secret", -time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken() returned error: %v", err)
	}

	if _, err := ParseAccessToken(token, "secret"); err == nil {
		t.Fatalf("expected error for expired token")
	}
}

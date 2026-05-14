package password

import "testing"

func TestHashAndCheck(t *testing.T) {
	raw := "strong-password"

	hashed, err := Hash(raw)
	if err != nil {
		t.Fatalf("Hash() returned error: %v", err)
	}
	if hashed == "" {
		t.Fatalf("Hash() returned empty hash")
	}
	if hashed == raw {
		t.Fatalf("hash must not equal raw password")
	}
	if !Check(hashed, raw) {
		t.Fatalf("Check() must return true for valid password")
	}
	if Check(hashed, "wrong-password") {
		t.Fatalf("Check() must return false for invalid password")
	}
}

func TestHashEmptyPassword(t *testing.T) {
	hashed, err := Hash("")
	if err != nil {
		t.Fatalf("Hash(\"\") returned error: %v", err)
	}
	if hashed == "" {
		t.Fatalf("Hash(\"\") returned empty hash")
	}
	if !Check(hashed, "") {
		t.Fatalf("Check() must return true for empty password hash and empty password")
	}
}

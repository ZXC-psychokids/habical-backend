package idgen

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewReturnsNonEmptyValidUniqueUUID(t *testing.T) {
	id1 := New()
	id2 := New()

	if id1 == "" {
		t.Fatalf("first id is empty")
	}
	if id2 == "" {
		t.Fatalf("second id is empty")
	}
	if id1 == id2 {
		t.Fatalf("generated ids must be unique: id1=%q id2=%q", id1, id2)
	}

	if _, err := uuid.Parse(id1); err != nil {
		t.Fatalf("first id is not valid UUID: %v", err)
	}
	if _, err := uuid.Parse(id2); err != nil {
		t.Fatalf("second id is not valid UUID: %v", err)
	}
}

package store

import (
	"context"
	"testing"
)

func TestOpenMigratesIdempotently(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v1 < 1 {
		t.Fatalf("schema version = %d, want >= 1", v1)
	}
	s.Close()

	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	v2, _ := s.SchemaVersion(ctx)
	if v1 != v2 {
		t.Fatalf("schema version changed on reopen: %d -> %d", v1, v2)
	}
}

package keys

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestIssueThenVerify(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	plain, err := s.Issue(ctx, "https://alpha.example.com")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(plain, keyPrefix) {
		t.Errorf("issued key %q lacks the %q prefix", plain, keyPrefix)
	}
	k, err := s.Verify(ctx, plain)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if k.Label != "https://alpha.example.com" {
		t.Errorf("got label=%q", k.Label)
	}
	if k.LastUsedAt == nil {
		t.Error("verify should stamp last-used")
	}
}

func TestVerifyUnknownKey(t *testing.T) {
	s := openTemp(t)
	if _, err := s.Verify(context.Background(), "ckr_not-a-real-key"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestRevokeBlocksVerify(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	plain, _ := s.Issue(ctx, "")
	k, err := s.Verify(ctx, plain)
	if err != nil {
		t.Fatalf("verify before revoke: %v", err)
	}
	if err := s.Revoke(ctx, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Verify(ctx, plain); err != ErrNotFound {
		t.Errorf("a revoked key should verify as ErrNotFound, got %v", err)
	}
}

func TestRevokeUnknownKey(t *testing.T) {
	s := openTemp(t)
	if err := s.Revoke(context.Background(), 9999); err != ErrNotFound {
		t.Errorf("want ErrNotFound revoking a missing key, got %v", err)
	}
}

func TestListNewestFirst(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	_, _ = s.Issue(ctx, "first")
	_, _ = s.Issue(ctx, "second")

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 keys, got %d", len(list))
	}
	if list[0].Label != "second" {
		t.Errorf("want newest (second) first, got %q", list[0].Label)
	}
}

func TestDistinctKeysHashDistinctly(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a, _ := s.Issue(ctx, "")
	b, _ := s.Issue(ctx, "")
	if a == b {
		t.Fatal("two issued keys must differ")
	}
	ka, _ := s.Verify(ctx, a)
	kb, _ := s.Verify(ctx, b)
	if ka.ID == kb.ID {
		t.Error("distinct keys must map to distinct rows")
	}
}

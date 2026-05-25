package shareapi

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	payload := []byte("hello, world")
	if err := s.Put(ctx, "abcd1234", payload); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(ctx, "abcd1234")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("Get returned %q, want %q", got, payload)
	}
}

func TestGetNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Get(context.Background(), "missingxx")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on missing code: err = %v, want ErrNotFound", err)
	}
}

func TestPutDuplicateReturnsErrCodeExists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "samecode", []byte("first")); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	err := s.Put(ctx, "samecode", []byte("second"))
	if !errors.Is(err, ErrCodeExists) {
		t.Errorf("second Put: err = %v, want ErrCodeExists", err)
	}

	// First payload should still be retrievable.
	got, err := s.Get(ctx, "samecode")
	if err != nil {
		t.Fatalf("Get after failed second Put: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("payload overwritten: got %q, want %q", got, "first")
	}
}

func TestCount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, code := range []string{"a0000000", "b0000000", "c0000000"} {
		if err := s.Put(ctx, code, []byte{byte(i)}); err != nil {
			t.Fatalf("Put %s: %v", code, err)
		}
	}
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
}

// TestConcurrentWrites makes sure WAL + busy_timeout let multiple
// goroutines write without spurious "database is locked" failures.
func TestConcurrentWrites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const n = 50
	codes := make([]string, n)
	for i := range codes {
		c, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		codes[i] = c
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for _, code := range codes {
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			if err := s.Put(ctx, code, []byte(code)); err != nil {
				errCh <- err
			}
		}(code)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Put: %v", err)
	}

	count, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != int64(n) {
		t.Errorf("Count = %d, want %d", count, n)
	}
}

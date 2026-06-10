package lock_test

import (
	"context"
	"testing"
	"time"

	"cinema-booking/backend/internal/lock"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestClient(t *testing.T) (*lock.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return lock.New(rdb), mr
}

// (a) First acquire succeeds; second acquire on the same key fails.
func TestAcquire_SecondAttemptFails(t *testing.T) {
	ctx := context.Background()
	client, _ := newTestClient(t)
	key := "seat:lock:show1:seatA"
	ttl := 5 * time.Second

	token, ok, err := client.Acquire(ctx, key, ttl)
	if err != nil {
		t.Fatalf("first acquire returned error: %v", err)
	}
	if !ok {
		t.Fatal("first acquire: expected ok=true")
	}
	if token == "" {
		t.Fatal("first acquire: expected non-empty ownerToken")
	}

	_, ok2, err2 := client.Acquire(ctx, key, ttl)
	if err2 != nil {
		t.Fatalf("second acquire returned error: %v", err2)
	}
	if ok2 {
		t.Fatal("second acquire: expected ok=false (key already held)")
	}
}

// (b) Release with the WRONG token does not delete the key.
func TestRelease_WrongTokenDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	client, mr := newTestClient(t)
	key := "seat:lock:show1:seatB"

	_, _, err := client.Acquire(ctx, key, 5*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	released, err := client.Release(ctx, key, "not-the-right-token")
	if err != nil {
		t.Fatalf("release with wrong token returned error: %v", err)
	}
	if released {
		t.Fatal("release with wrong token: expected released=false")
	}

	// Key must still exist.
	if _, redisErr := mr.Get(key); redisErr != nil {
		t.Fatalf("key should still exist after wrong-token release, got: %v", redisErr)
	}
}

// (c) Release with the correct token deletes the key; key is acquirable again.
func TestRelease_CorrectTokenDeletesAndAllowsReacquire(t *testing.T) {
	ctx := context.Background()
	client, mr := newTestClient(t)
	key := "seat:lock:show1:seatC"

	token, ok, err := client.Acquire(ctx, key, 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("acquire failed: ok=%v err=%v", ok, err)
	}

	released, err := client.Release(ctx, key, token)
	if err != nil {
		t.Fatalf("release returned error: %v", err)
	}
	if !released {
		t.Fatal("release with correct token: expected released=true")
	}

	// Key must be gone.
	if val, _ := mr.Get(key); val != "" {
		t.Fatalf("key should be absent after release, got value %q", val)
	}

	// Should be acquirable again.
	_, ok2, err2 := client.Acquire(ctx, key, 5*time.Second)
	if err2 != nil {
		t.Fatalf("re-acquire returned error: %v", err2)
	}
	if !ok2 {
		t.Fatal("re-acquire after release: expected ok=true")
	}
}

package lock

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// releaseScript atomically releases a lock only if the caller still owns it.
// KEYS[1] = lock key; ARGV[1] = ownerToken expected by the caller.
// Returns 1 if deleted, 0 if the key was absent or owned by someone else.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`)

// Client wraps a Redis client with seat-lock operations.
type Client struct {
	rdb *redis.Client
}

// New creates a lock Client backed by the provided Redis client.
func New(rdb *redis.Client) *Client {
	return &Client{rdb: rdb}
}

// KeyFor returns the canonical Redis key for a seat lock.
func KeyFor(showtimeID, seatID string) string {
	return fmt.Sprintf("seat:lock:%s:%s", showtimeID, seatID)
}

// Acquire attempts to claim the lock at key for ttl.
// Returns (ownerToken, true, nil) on success.
// Returns ("", false, nil) when the key is already held by another owner.
// Returns ("", false, err) on a Redis error.
func (c *Client) Acquire(ctx context.Context, key string, ttl time.Duration) (ownerToken string, ok bool, err error) {
	token := uuid.New().String()
	set, err := c.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("lock acquire %q: %w", key, err)
	}
	if !set {
		return "", false, nil
	}
	return token, true, nil
}

// Release atomically removes the lock at key only when ownerToken matches.
// Returns (true, nil) if this call deleted the key.
// Returns (false, nil) if the key was absent or held by a different owner
// (e.g. the lock expired and was re-acquired between Acquire and Release).
// Returns (false, err) on a Redis error.
func (c *Client) Release(ctx context.Context, key, ownerToken string) (released bool, err error) {
	res, err := releaseScript.Run(ctx, c.rdb, []string{key}, ownerToken).Int()
	if err != nil {
		return false, fmt.Errorf("lock release %q: %w", key, err)
	}
	return res == 1, nil
}

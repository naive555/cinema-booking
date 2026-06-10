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

// checkScript returns: 1 if caller owns the lock, -1 if the key is absent
// (lock expired), 0 if the key exists but belongs to a different owner.
// All three branches are evaluated atomically at the Redis server.
var checkScript = redis.NewScript(`
local v = redis.call("GET", KEYS[1])
if v == false then
    return -1
elseif v == ARGV[1] then
    return 1
else
    return 0
end
`)

// OwnershipStatus is the result of CheckOwnership.
type OwnershipStatus int

const (
	Owned      OwnershipStatus = 1  // caller still holds the lock
	Expired    OwnershipStatus = -1 // key is absent; TTL elapsed
	WrongOwner OwnershipStatus = 0  // key exists but belongs to someone else
)

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

// CheckOwnership atomically reports whether key is still held by ownerToken.
// The three possible outcomes are Owned, Expired, and WrongOwner.
func (c *Client) CheckOwnership(ctx context.Context, key, ownerToken string) (OwnershipStatus, error) {
	res, err := checkScript.Run(ctx, c.rdb, []string{key}, ownerToken).Int()
	if err != nil {
		return WrongOwner, fmt.Errorf("lock check %q: %w", key, err)
	}
	return OwnershipStatus(res), nil
}

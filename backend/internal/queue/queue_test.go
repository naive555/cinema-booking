package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// withShrunkTunables overrides the package-level reclaim tunables for the
// duration of a test and restores them afterward, so tests can observe the
// reclaim loop without waiting on production timescales (30s/15s).
func withShrunkTunables(t *testing.T, minIdle, every time.Duration, maxDel int64) {
	t.Helper()
	origMinIdle, origEvery, origMax := reclaimMinIdle, reclaimEvery, maxDeliveries
	reclaimMinIdle, reclaimEvery, maxDeliveries = minIdle, every, maxDel
	t.Cleanup(func() {
		reclaimMinIdle, reclaimEvery, maxDeliveries = origMinIdle, origEvery, origMax
	})
}

func newTestClient(t *testing.T) (*Client, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return New(rdb), rdb
}

// strand publishes an event and reads it as consumer "dead-1" without
// acking it, simulating a consumer that crashed mid-processing and left the
// message in the group's PEL forever.
func strand(t *testing.T, ctx context.Context, c *Client, rdb *redis.Client, group string, ev Event) {
	t.Helper()
	if err := c.Publish(ctx, ev); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := rdb.XGroupCreateMkStream(ctx, StreamKey, group, "0").Err(); err != nil {
		t.Fatalf("create group: %v", err)
	}
	entries, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: "dead-1",
		Streams:  []string{StreamKey, ">"},
		Count:    10,
	}).Result()
	if err != nil {
		t.Fatalf("xreadgroup as dead-1: %v", err)
	}
	if len(entries) != 1 || len(entries[0].Messages) != 1 {
		t.Fatalf("expected exactly one stranded message, got %+v", entries)
	}
}

func TestStartConsumer_ReclaimsStrandedMessage(t *testing.T) {
	withShrunkTunables(t, 1*time.Millisecond, 10*time.Millisecond, 5)

	ctx := context.Background()
	c, rdb := newTestClient(t)
	group := "test-group"

	ev := Event{Action: "BOOKING_SUCCESS", BookingID: "b1", ShowtimeID: "s1", SeatID: "seat1"}
	strand(t, ctx, c, rdb, group, ev)

	received := make(chan Event, 1)
	handler := func(got Event) error {
		received <- got
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.StartConsumer(runCtx, group, "consumer-2", handler)
		close(done)
	}()

	select {
	case got := <-received:
		if got.BookingID != ev.BookingID {
			t.Fatalf("handler received wrong event: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reclaimed message to reach the handler")
	}

	cancel()
	<-done

	pending, err := rdb.XPending(ctx, StreamKey, group).Result()
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	if pending.Count != 0 {
		t.Fatalf("expected PEL empty after ack, got count=%d", pending.Count)
	}
}

func TestStartConsumer_DeadLettersAfterMaxDeliveries(t *testing.T) {
	withShrunkTunables(t, 1*time.Millisecond, 10*time.Millisecond, 2)

	ctx := context.Background()
	c, rdb := newTestClient(t)
	group := "test-group-dead"

	ev := Event{Action: "BOOKING_SUCCESS", BookingID: "b2", ShowtimeID: "s2", SeatID: "seat2"}
	strand(t, ctx, c, rdb, group, ev)

	var calls int32
	handler := func(Event) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("boom")
	}

	runCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	go c.StartConsumer(runCtx, group, "consumer-2", handler)

	deadline := time.Now().Add(2 * time.Second)
	var deadLen int64
	for time.Now().Before(deadline) {
		deadLen, _ = rdb.XLen(ctx, DeadLetterKey).Result()
		if deadLen > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	if deadLen != 1 {
		t.Fatalf("expected exactly 1 dead-lettered message, got %d", deadLen)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("expected the always-erroring handler to be called at least once")
	}

	pending, err := rdb.XPending(ctx, StreamKey, group).Result()
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	if pending.Count != 0 {
		t.Fatalf("expected PEL to drain once the message is dead-lettered, got count=%d", pending.Count)
	}
}

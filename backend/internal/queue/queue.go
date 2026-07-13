// Package queue publishes and consumes booking lifecycle events via Redis Streams.
// Producers call Publish; a background goroutine calls StartConsumer.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// StreamKey is the Redis Stream name for booking lifecycle events.
// All XADD, XREADGROUP, and XACK calls use this key.
const StreamKey = "events:booking"

// DeadLetterKey holds events that failed maxDeliveries reclaim attempts, so a
// permanently failing handler can't cause infinite redelivery of one message.
const DeadLetterKey = StreamKey + ":dead"

// Reclaim-loop tunables. Package-level vars (not consts) so tests can shrink
// them to make idle/reclaim timing observable within a test's lifetime.
var (
	reclaimMinIdle = 30 * time.Second
	reclaimEvery   = 15 * time.Second
	maxDeliveries  = int64(5)
)

// Event is a booking lifecycle event published to the stream.
// Currently only BOOKING_SUCCESS events are published; other state changes
// are either written inline (SEAT_LOCKED) or broadcast over WebSocket.
type Event struct {
	Action     string `json:"action"`     // BOOKING_SUCCESS
	BookingID  string `json:"bookingId"`
	ShowtimeID string `json:"showtimeId"`
	SeatID     string `json:"seatId"`
	UserID     string `json:"userId"`
	UserEmail  string `json:"userEmail"`  // used by consumer for mock notification
}

// Client wraps a Redis client for stream operations.
type Client struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Client { return &Client{rdb: rdb} }

// Publish appends ev to the booking.events stream. Best-effort: the caller
// should not fail the HTTP request if this returns an error.
func (c *Client) Publish(ctx context.Context, ev Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("queue publish marshal: %w", err)
	}
	_, err = c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamKey,
		Values: map[string]interface{}{"event": string(b)},
	}).Result()
	if err != nil {
		return fmt.Errorf("queue publish xadd: %w", err)
	}
	return nil
}

// Handler is the function type called for each consumed event.
// Return a non-nil error to leave the message in the PEL for retry.
type Handler func(Event) error

// StartConsumer creates the consumer group (idempotent), starts a background
// reclaim loop (see reclaim), then enters a blocking XREADGROUP loop. Calls
// handler for each message and ACKs on success. Blocks until ctx is cancelled,
// and only returns once both loops have stopped.
//
// The reclaim pass runs on its own ticker goroutine rather than interleaved
// into this loop's select: XREADGROUP blocks for up to 2s waiting for new
// messages, which would otherwise starve a same-loop reclaim tick until the
// next blocking call happens to return.
func (c *Client) StartConsumer(ctx context.Context, group, consumer string, h Handler) {
	err := c.rdb.XGroupCreateMkStream(ctx, StreamKey, group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		log.Printf("queue: create group %q: %v", group, err)
		return
	}

	log.Printf("queue: consumer %s/%s listening on %s", group, consumer, StreamKey)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.runReclaimLoop(ctx, group, consumer, h)
	}()
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		entries, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumer,
			Streams:  []string{StreamKey, ">"},
			Count:    10,
			Block:    2 * time.Second,
		}).Result()
		if err != nil {
			if err == redis.Nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("queue: xreadgroup: %v", err)
			time.Sleep(time.Second)
			continue
		}

		for _, stream := range entries {
			c.process(ctx, group, stream.Messages, h)
		}
	}
}

// runReclaimLoop runs reclaim on a fixed tick until ctx is cancelled.
func (c *Client) runReclaimLoop(ctx context.Context, group, consumer string, h Handler) {
	ticker := time.NewTicker(reclaimEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reclaim(ctx, group, consumer, h)
		}
	}
}

// process unmarshals and handles a batch of messages. A malformed payload
// (poison pill) is ACKed immediately since it can never succeed. A handler
// error leaves the message in the PEL so a later XREADGROUP redelivery (same
// consumer, still alive) or reclaim pass (different consumer, after MinIdle)
// picks it up again.
func (c *Client) process(ctx context.Context, group string, msgs []redis.XMessage, h Handler) {
	for _, msg := range msgs {
		raw, _ := msg.Values["event"].(string)
		var ev Event
		if jsonErr := json.Unmarshal([]byte(raw), &ev); jsonErr != nil {
			log.Printf("queue: bad message %s: %v", msg.ID, jsonErr)
			c.rdb.XAck(ctx, StreamKey, group, msg.ID) //nolint:errcheck
			continue
		}
		if handlerErr := h(ev); handlerErr != nil {
			log.Printf("queue: handler error msg=%s: %v (will retry)", msg.ID, handlerErr)
			continue
		}
		c.rdb.XAck(ctx, StreamKey, group, msg.ID) //nolint:errcheck
	}
}

// reclaim claims stream entries that have sat unacked for at least
// reclaimMinIdle — the mark of a consumer that crashed or errored and never
// came back to retry them itself — and reprocesses them under this consumer.
// Entries already claimed more than maxDeliveries times are routed to
// DeadLetterKey instead, so one permanently-failing message can't loop
// through reclaim forever.
func (c *Client) reclaim(ctx context.Context, group, consumer string, h Handler) {
	msgs, _, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   StreamKey,
		Group:    group,
		Consumer: consumer,
		MinIdle:  reclaimMinIdle,
		Start:    "0-0",
		Count:    10,
	}).Result()
	if err != nil && err != redis.Nil {
		log.Printf("queue: xautoclaim: %v", err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	live := msgs[:0]
	for _, msg := range msgs {
		if c.deadLetter(ctx, group, msg) {
			continue
		}
		live = append(live, msg)
	}
	c.process(ctx, group, live, h)
}

// deadLetter checks msg's delivery count and, if it exceeds maxDeliveries,
// moves it to DeadLetterKey and ACKs it off the pending list. Returns true if
// the message was dead-lettered (and so must not be processed again).
func (c *Client) deadLetter(ctx context.Context, group string, msg redis.XMessage) bool {
	pending, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: StreamKey,
		Group:  group,
		Start:  msg.ID,
		End:    msg.ID,
		Count:  1,
	}).Result()
	if err != nil || len(pending) == 0 || pending[0].RetryCount <= maxDeliveries {
		return false
	}

	if _, addErr := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: DeadLetterKey,
		Values: msg.Values,
	}).Result(); addErr != nil {
		log.Printf("queue: dead-letter xadd failed msg=%s: %v", msg.ID, addErr)
		return false // leave it in the PEL rather than lose it
	}
	c.rdb.XAck(ctx, StreamKey, group, msg.ID) //nolint:errcheck
	log.Printf("queue: message %s exceeded %d delivery attempts, moved to dead-letter", msg.ID, maxDeliveries)
	return true
}

// Package queue publishes and consumes booking lifecycle events via Redis Streams.
// Producers call Publish; a background goroutine calls StartConsumer.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// StreamKey is the Redis Stream name for booking lifecycle events.
// All XADD, XREADGROUP, and XACK calls use this key.
const StreamKey = "events:booking"

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

// StartConsumer creates the consumer group (idempotent) then enters a blocking
// XREADGROUP loop. Calls handler for each message and ACKs on success.
// Blocks until ctx is cancelled.
func (c *Client) StartConsumer(ctx context.Context, group, consumer string, h Handler) {
	err := c.rdb.XGroupCreateMkStream(ctx, StreamKey, group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		log.Printf("queue: create group %q: %v", group, err)
		return
	}

	log.Printf("queue: consumer %s/%s listening on %s", group, consumer, StreamKey)
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
			for _, msg := range stream.Messages {
				raw, _ := msg.Values["event"].(string)
				var ev Event
				if jsonErr := json.Unmarshal([]byte(raw), &ev); jsonErr != nil {
					log.Printf("queue: bad message %s: %v", msg.ID, jsonErr)
					c.rdb.XAck(ctx, StreamKey, group, msg.ID) //nolint:errcheck
					continue
				}
				if handlerErr := h(ev); handlerErr != nil {
					log.Printf("queue: handler error msg=%s: %v (will retry)", msg.ID, handlerErr)
					continue // message stays in PEL
				}
				c.rdb.XAck(ctx, StreamKey, group, msg.ID) //nolint:errcheck
			}
		}
	}
}

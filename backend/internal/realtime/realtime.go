// Package realtime manages WebSocket connections and broadcasts seat-state
// changes to all clients watching a given showtime.
package realtime

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// Event is the JSON frame pushed to every client in a showtime room.
type Event struct {
	Type   string    `json:"type"`
	SeatID string    `json:"seatId"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

// ── internal types ────────────────────────────────────────────────────────────

type client struct {
	hub        *Hub
	showtimeID string
	conn       *websocket.Conn
	send       chan []byte
}

type roomMsg struct {
	showtimeID string
	payload    []byte
}

// ── Hub ───────────────────────────────────────────────────────────────────────

// Hub owns per-showtime rooms. All state mutations run in the Run goroutine
// so the rooms map is never accessed concurrently — no mutex needed.
type Hub struct {
	rooms          map[string]map[*client]bool
	register       chan *client
	unregister     chan *client
	broadcast      chan roomMsg
	allowedOrigins map[string]bool
	upgrader       websocket.Upgrader
}

// NewHub builds a Hub whose WebSocket upgrader only accepts connections whose
// Origin header (case-insensitive, trailing slash ignored) is in
// allowedOrigins. A request with no Origin header at all — non-browser
// clients, curl, server-to-server, tests — is always allowed, since only
// browsers send Origin.
func NewHub(allowedOrigins ...string) *Hub {
	h := &Hub{
		rooms:          make(map[string]map[*client]bool),
		register:       make(chan *client, 64),
		unregister:     make(chan *client, 64),
		broadcast:      make(chan roomMsg, 256),
		allowedOrigins: make(map[string]bool, len(allowedOrigins)),
	}
	for _, o := range allowedOrigins {
		h.allowedOrigins[normalizeOrigin(o)] = true
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
		Subprotocols:    []string{"bearer"},
	}
	return h
}

func normalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
}

// checkOrigin rejects any browser-sent Origin that isn't in the allowlist.
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return h.allowedOrigins[normalizeOrigin(origin)]
}

// Run serialises all hub state changes. Must be called in its own goroutine.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			if h.rooms[c.showtimeID] == nil {
				h.rooms[c.showtimeID] = make(map[*client]bool)
			}
			h.rooms[c.showtimeID][c] = true
			log.Printf("ws: +client showtime=%s room_size=%d", c.showtimeID, len(h.rooms[c.showtimeID]))

		case c := <-h.unregister:
			if room, ok := h.rooms[c.showtimeID]; ok {
				if _, exists := room[c]; exists {
					delete(room, c)
					close(c.send)
					if len(room) == 0 {
						delete(h.rooms, c.showtimeID)
					}
				}
			}

		case msg := <-h.broadcast:
			for c := range h.rooms[msg.showtimeID] {
				select {
				case c.send <- msg.payload:
				default:
					// slow client — evict rather than block the broadcast loop
					delete(h.rooms[msg.showtimeID], c)
					close(c.send)
				}
			}
		}
	}
}

// Broadcast fans out ev to every client watching showtimeID.
func (h *Hub) Broadcast(showtimeID string, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.broadcast <- roomMsg{showtimeID: showtimeID, payload: b}
}

// OnExpiryFunc is called (in its own goroutine) for each expired seat lock.
// showtimeID and seatID are the hex strings extracted from the key name.
// Use it to write audit logs or trigger other side-effects without blocking
// the subscription loop.
type OnExpiryFunc func(showtimeID, seatID string)

// WatchLockExpiry subscribes to Redis keyspace expiry events and broadcasts
// SEAT_RELEASED whenever a seat:lock:* key expires naturally (TTL fired).
// onExpiry is called concurrently for each event; pass nil to skip the callback.
// Blocks until ctx is done.
//
// notify-keyspace-events must be set to "Ex" (or a superset) on the Redis
// server. This is done via docker-compose (redis command flag) so the setting
// survives Redis restarts. The ConfigSet call below is a belt-and-suspenders
// fallback for non-compose environments.
func (h *Hub) WatchLockExpiry(ctx context.Context, rdb *redis.Client, onExpiry OnExpiryFunc) {
	// Belt-and-suspenders: ensure the setting is active even when running
	// outside docker-compose (e.g. a developer's local Redis instance).
	if err := rdb.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		log.Printf("realtime: ConfigSet notify-keyspace-events failed: %v (continuing — flag may already be set via redis.conf)", err)
	}

	pubsub := rdb.PSubscribe(ctx, "__keyevent@0__:expired")
	defer pubsub.Close()

	log.Println("realtime: watching seat lock expiry events")
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			sid, seatID := parseLockKey(msg.Payload)
			if sid == "" {
				continue
			}
			h.Broadcast(sid, Event{
				Type:   "SEAT_RELEASED",
				SeatID: seatID,
				Status: "AVAILABLE",
				At:     time.Now(),
			})
			if onExpiry != nil {
				go onExpiry(sid, seatID)
			}
		}
	}
}

// parseLockKey extracts (showtimeID, seatID) from "seat:lock:{sid}:{seatId}".
func parseLockKey(key string) (showtimeID, seatID string) {
	parts := strings.SplitN(key, ":", 4)
	if len(parts) != 4 || parts[0] != "seat" || parts[1] != "lock" {
		return "", ""
	}
	return parts[2], parts[3]
}

// ── WebSocket upgrade ─────────────────────────────────────────────────────────
//
// The upgrader itself lives on Hub (see NewHub) so CheckOrigin can consult
// the Hub's allowedOrigins. Its Subprotocols field lists "bearer" so
// gorilla/websocket both accepts a client offering it and echoes it back in
// the handshake response — browsers abort the connection if a requested
// subprotocol isn't echoed. The auth middleware reads the JWT out of that
// same header (see auth.Middleware).

// Handler upgrades an HTTP connection to WebSocket and registers the client
// in the showtime room given by ?showtimeId=. Auth is enforced by the parent
// router group's middleware before this handler runs.
func (h *Hub) Handler(c *gin.Context) {
	showtimeID := c.Query("showtimeId")
	if showtimeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "showtimeId query param required"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	cl := &client{
		hub:        h,
		showtimeID: showtimeID,
		conn:       conn,
		send:       make(chan []byte, 64),
	}
	h.register <- cl

	go cl.writePump()
	cl.readPump() // blocks until the connection closes
}

// ── client pumps ──────────────────────────────────────────────────────────────

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 54 * time.Second // must be < pongWait
	maxMsgSize = 512
)

// readPump drains incoming frames (clients are send-only; we discard their
// messages but need to read to detect disconnects and handle pong frames).
func (c *client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMsgSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// writePump drains the send channel and sends periodic pings to detect dead
// connections before the OS TCP timeout fires.
func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(msg)
			w.Close()

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

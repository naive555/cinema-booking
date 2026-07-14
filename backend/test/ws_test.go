// Integration test for the realtime WebSocket path.
// Requires: docker compose up with DEV_AUTH=true and SEED_ON_START=true.
// Run: go test -v -count=1 -run TestWSExpiryBroadcast ./test/...   (from backend/)
package test

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// lockTTLSeconds mirrors testBaseURL's env-override pattern so this test
// tracks whatever TTL the running stack was started with instead of assuming
// docker-compose.yml's 15s default.
func lockTTLSeconds() int {
	if v := os.Getenv("LOCK_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 15 // matches docker-compose.yml's LOCK_TTL_SECONDS fallback
}

// TestWSExpiryBroadcast dials /api/ws the way a real browser does post-B1b —
// authenticating via the Sec-WebSocket-Protocol header instead of a query
// param — which end-to-end-proves the subprotocol scheme works through the
// actual HTTP upgrade, not just the auth middleware's unit tests. It then
// selects a seat, lets the lock TTL expire, and asserts a SEAT_RELEASED frame
// arrives — covering the full expiry -> keyspace notification -> broadcast
// path (§4/§B4) in one shot.
func TestWSExpiryBroadcast(t *testing.T) {
	const showtimeID = "000000000000000000000001"
	tok := mintToken(t, "ws-expiry@apitest.com")

	seatID, _, _ := findAvailableSeat(t, tok, showtimeID)

	wsURL := strings.Replace(testBaseURL(), "http://", "ws://", 1) +
		"/api/ws?showtimeId=" + showtimeID
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "bearer, "+tok)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != "bearer" {
		t.Fatalf("server did not echo the bearer subprotocol, got %q", got)
	}

	_, status := doSelect(tok, showtimeID, seatID)
	if status != http.StatusOK {
		t.Fatalf("select: want 200, got %d", status)
	}

	conn.SetReadDeadline(time.Now().Add(time.Duration(lockTTLSeconds()+2) * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for SEAT_RELEASED: %v", err)
		}
		var ev struct {
			Type   string `json:"type"`
			SeatID string `json:"seatId"`
			Status string `json:"status"`
		}
		if json.Unmarshal(msg, &ev) != nil {
			continue
		}
		if ev.Type == "SEAT_RELEASED" && ev.SeatID == seatID {
			if ev.Status != "AVAILABLE" {
				t.Errorf("SEAT_RELEASED status = %q, want AVAILABLE", ev.Status)
			}
			return
		}
	}
}

package seat

import (
	"cinema-booking/backend/internal/domain"
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Handler serves seat-map queries, merging Mongo bookings with Redis locks.
type Handler struct {
	db  *mongo.Database
	rdb *redis.Client
}

// NewHandler creates a seat Handler wired to Mongo and Redis.
func NewHandler(db *mongo.Database, rdb *redis.Client) *Handler {
	return &Handler{db: db, rdb: rdb}
}

// SeatView is the per-seat response payload, including computed status.
type SeatView struct {
	ID     string            `json:"id"`
	Row    string            `json:"row"`
	Number int               `json:"number"`
	Status domain.SeatStatus `json:"status"`
}

// GetSeatMap returns every seat for a showtime annotated with its current status.
//
// Status resolution order (highest wins):
//  1. BOOKED    — a Booking document exists in MongoDB for this seat
//  2. LOCKED    — a Redis key seat:lock:{showtimeId}:{seatId} exists
//  3. AVAILABLE — neither of the above
//
// LOCKED detection uses a single MGET over all N seat-lock keys so the Redis
// round-trips are O(1), not O(N). Redis unavailability degrades locked seats
// to AVAILABLE rather than returning 500 — lock state is ephemeral and the
// Mongo unique index remains the hard safety net.
func (h *Handler) GetSeatMap(c *gin.Context) {
	showtimeHex := c.Param("showtimeId")
	showtimeID, err := bson.ObjectIDFromHex(showtimeHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid showtime id"})
		return
	}

	ctx := c.Request.Context()

	seats, err := h.fetchSeats(ctx, showtimeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seat fetch failed"})
		return
	}
	if len(seats) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "showtime not found or has no seats"})
		return
	}

	bookedIDs, err := h.fetchBookedIDs(ctx, showtimeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "booking fetch failed"})
		return
	}

	// Single MGET round-trip — one key per seat, all fetched at once.
	lockedIDs, _ := h.fetchLockedIDs(ctx, showtimeHex, seats) // non-fatal

	views := make([]SeatView, 0, len(seats))
	for _, s := range seats {
		id := s.ID.Hex()
		status := domain.StatusAvailable
		if _, ok := bookedIDs[id]; ok {
			status = domain.StatusBooked
		} else if _, ok := lockedIDs[id]; ok {
			status = domain.StatusLocked
		}
		views = append(views, SeatView{ID: id, Row: s.Row, Number: s.Number, Status: status})
	}

	c.JSON(http.StatusOK, gin.H{"showtimeId": showtimeHex, "seats": views})
}

// ── data helpers ──────────────────────────────────────────────────────────────

func (h *Handler) fetchSeats(ctx context.Context, showtimeID bson.ObjectID) ([]domain.Seat, error) {
	cur, err := h.db.Collection("seats").Find(ctx, bson.M{"showtimeId": showtimeID})
	if err != nil {
		return nil, err
	}
	var seats []domain.Seat
	return seats, cur.All(ctx, &seats)
}

func (h *Handler) fetchBookedIDs(ctx context.Context, showtimeID bson.ObjectID) (map[string]struct{}, error) {
	cur, err := h.db.Collection("bookings").Find(ctx, bson.M{"showtimeId": showtimeID})
	if err != nil {
		return nil, err
	}
	var bookings []domain.Booking
	if err := cur.All(ctx, &bookings); err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(bookings))
	for _, b := range bookings {
		ids[b.SeatID.Hex()] = struct{}{}
	}
	return ids, nil
}

// fetchLockedIDs resolves lock state for every seat in a single MGET call.
// It builds the exact key list from the known seat IDs, so there is no
// keyspace scan — O(1) round-trips regardless of how many other keys exist.
func (h *Handler) fetchLockedIDs(ctx context.Context, showtimeHex string, seats []domain.Seat) (map[string]struct{}, error) {
	if len(seats) == 0 {
		return map[string]struct{}{}, nil
	}

	// Build all lock key names in the same order as seats.
	keys := make([]string, len(seats))
	for i, s := range seats {
		keys[i] = fmt.Sprintf("seat:lock:%s:%s", showtimeHex, s.ID.Hex())
	}

	// One MGET fetches all values — nil means key absent (AVAILABLE), non-nil means locked.
	vals, err := h.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return map[string]struct{}{}, err
	}

	ids := make(map[string]struct{})
	for i, v := range vals {
		if v != nil {
			ids[seats[i].ID.Hex()] = struct{}{}
		}
	}
	return ids, nil
}

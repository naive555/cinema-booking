package seat

import (
	"cinema-booking/backend/internal/domain"
	"context"
	"fmt"
	"net/http"
	"strings"

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
//  1. BOOKED  — a Booking document exists in MongoDB for this seat
//  2. LOCKED  — a Redis key seat:lock:{showtimeId}:{seatId} exists
//  3. AVAILABLE — neither of the above
//
// Redis unavailability degrades gracefully: locked seats are shown as AVAILABLE
// rather than returning 500, because lock state is ephemeral and its loss is
// recoverable (the lock will expire or the next booking attempt will re-acquire).
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

	lockedIDs, _ := h.fetchLockedIDs(ctx, showtimeHex) // non-fatal

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

func (h *Handler) fetchLockedIDs(ctx context.Context, showtimeHex string) (map[string]struct{}, error) {
	pattern := fmt.Sprintf("seat:lock:%s:*", showtimeHex)
	ids := make(map[string]struct{})
	var cursor uint64
	for {
		keys, nextCursor, err := h.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return ids, err
		}
		for _, key := range keys {
			// key = seat:lock:{showtimeId}:{seatId} — extract the last segment
			if idx := strings.LastIndex(key, ":"); idx >= 0 {
				ids[key[idx+1:]] = struct{}{}
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return ids, nil
}

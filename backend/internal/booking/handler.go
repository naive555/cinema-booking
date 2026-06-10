package booking

import (
	"context"
	"net/http"
	"time"

	"cinema-booking/backend/internal/auth"
	"cinema-booking/backend/internal/domain"
	"cinema-booking/backend/internal/lock"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Handler owns the seat-reservation flow: select (acquire lock) and pay (confirm booking).
type Handler struct {
	db      *mongo.Database
	locker  *lock.Client
	lockTTL time.Duration
}

func NewHandler(db *mongo.Database, lc *lock.Client, lockTTL time.Duration) *Handler {
	return &Handler{db: db, locker: lc, lockTTL: lockTTL}
}

type selectResp struct {
	OwnerToken string    `json:"ownerToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type payReq struct {
	OwnerToken string `json:"ownerToken" binding:"required"`
}

// Select acquires a Redis seat lock for the authenticated user.
// POST /api/showtimes/:showtimeId/seats/:seatId/select
func (h *Handler) Select(c *gin.Context) {
	showtimeHex, seatHex, showtimeID, seatID, ok := parseParams(c)
	if !ok {
		return
	}

	claims, _ := auth.ClaimsFromContext(c)
	ctx := c.Request.Context()

	booked, err := h.isSeatBooked(ctx, showtimeID, seatID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seat check failed"})
		return
	}
	if booked {
		c.JSON(http.StatusConflict, gin.H{"error": "seat is already booked"})
		return
	}

	key := lock.KeyFor(showtimeHex, seatHex)
	ownerToken, acquired, err := h.locker.Acquire(ctx, key, h.lockTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lock acquire failed"})
		return
	}
	if !acquired {
		h.writeAudit(ctx, "LOCK_FAIL", claims, showtimeID, seatID, nil)
		c.JSON(http.StatusConflict, gin.H{"error": "seat is being held"})
		return
	}

	h.writeAudit(ctx, "SEAT_LOCKED", claims, showtimeID, seatID, nil)

	c.JSON(http.StatusOK, selectResp{
		OwnerToken: ownerToken,
		ExpiresAt:  time.Now().Add(h.lockTTL),
	})
}

// Pay confirms the booking using the ownerToken returned by Select.
// POST /api/showtimes/:showtimeId/seats/:seatId/pay
func (h *Handler) Pay(c *gin.Context) {
	showtimeHex, seatHex, showtimeID, seatID, ok := parseParams(c)
	if !ok {
		return
	}

	var req payReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ownerToken is required"})
		return
	}

	claims, _ := auth.ClaimsFromContext(c)
	ctx := c.Request.Context()

	// Atomically verify the caller still owns the lock before writing to Mongo.
	key := lock.KeyFor(showtimeHex, seatHex)
	status, err := h.locker.CheckOwnership(ctx, key, req.OwnerToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lock check failed"})
		return
	}
	if status != lock.Owned {
		action := "LOCK_FAIL"
		if status == lock.Expired {
			action = "BOOKING_TIMEOUT"
		}
		h.writeAudit(ctx, action, claims, showtimeID, seatID, nil)
		c.JSON(http.StatusGone, gin.H{"error": "hold expired, reselect"})
		return
	}

	// Write the booking. The unique index {showtimeId, seatId} is the hard guard
	// against double-booking — it catches any race that slips past the lock.
	userID, _ := bson.ObjectIDFromHex(claims.RegisteredClaims.Subject)
	booking := domain.Booking{
		ShowtimeID: showtimeID,
		SeatID:     seatID,
		UserID:     userID,
		CreatedAt:  time.Now(),
	}
	_, insertErr := h.db.Collection("bookings").InsertOne(ctx, booking)
	if insertErr != nil {
		if mongo.IsDuplicateKeyError(insertErr) {
			h.writeAudit(ctx, "BOOKING_DUPLICATE", claims, showtimeID, seatID, nil)
			c.JSON(http.StatusConflict, gin.H{"error": "seat already booked"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "booking write failed"})
		return
	}

	// Booking committed — release the lock. Non-fatal if the TTL already fired.
	h.locker.Release(ctx, key, req.OwnerToken) //nolint:errcheck

	h.writeAudit(ctx, "BOOKING_SUCCESS", claims, showtimeID, seatID, nil)

	c.JSON(http.StatusOK, gin.H{
		"status":     "booked",
		"showtimeId": showtimeHex,
		"seatId":     seatHex,
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func parseParams(c *gin.Context) (showtimeHex, seatHex string, showtimeID, seatID bson.ObjectID, ok bool) {
	showtimeHex = c.Param("showtimeId")
	seatHex = c.Param("seatId")

	var err error
	showtimeID, err = bson.ObjectIDFromHex(showtimeHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid showtime id"})
		return
	}
	seatID, err = bson.ObjectIDFromHex(seatHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid seat id"})
		return
	}
	ok = true
	return
}

func (h *Handler) isSeatBooked(ctx context.Context, showtimeID, seatID bson.ObjectID) (bool, error) {
	n, err := h.db.Collection("bookings").CountDocuments(ctx,
		bson.M{"showtimeId": showtimeID, "seatId": seatID},
	)
	return n > 0, err
}

func (h *Handler) writeAudit(ctx context.Context, action string, claims *auth.Claims, showtimeID, seatID bson.ObjectID, meta map[string]interface{}) {
	userID, _ := bson.ObjectIDFromHex(claims.RegisteredClaims.Subject)
	entry := domain.AuditLog{
		Action:     action,
		UserID:     userID,
		ShowtimeID: showtimeID,
		SeatID:     seatID,
		Meta:       meta,
		CreatedAt:  time.Now(),
	}
	h.db.Collection("audit_logs").InsertOne(ctx, entry) //nolint:errcheck
}

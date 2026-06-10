package booking

import (
	"net/http"

	"cinema-booking/backend/internal/domain"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// AdminListBookings returns bookings filtered by optional query params.
// GET /api/admin/bookings?showtimeId=<hex>&userId=<hex>
func (h *Handler) AdminListBookings(c *gin.Context) {
	filter := bson.M{}

	if sid := c.Query("showtimeId"); sid != "" {
		if id, err := bson.ObjectIDFromHex(sid); err == nil {
			filter["showtimeId"] = id
		}
	}
	if uid := c.Query("userId"); uid != "" {
		if id, err := bson.ObjectIDFromHex(uid); err == nil {
			filter["userId"] = id
		}
	}

	ctx := c.Request.Context()
	cursor, err := h.db.Collection("bookings").Find(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}
	defer cursor.Close(ctx)

	var results []domain.Booking
	if err := cursor.All(ctx, &results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decode failed"})
		return
	}
	if results == nil {
		results = []domain.Booking{}
	}
	c.JSON(http.StatusOK, gin.H{"bookings": results, "count": len(results)})
}

package showtime

import (
	"cinema-booking/backend/internal/domain"
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Handler serves showtime queries.
type Handler struct {
	db *mongo.Database
}

// NewHandler creates a showtime Handler wired to Mongo.
func NewHandler(db *mongo.Database) *Handler {
	return &Handler{db: db}
}

// ListShowtimes returns all showtimes ordered by start time ascending.
func (h *Handler) ListShowtimes(c *gin.Context) {
	showtimes, err := h.list(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "showtime fetch failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"showtimes": showtimes})
}

func (h *Handler) list(ctx context.Context) ([]domain.Showtime, error) {
	opts := options.Find().SetSort(bson.D{{Key: "startTime", Value: 1}})
	cur, err := h.db.Collection("showtimes").Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	var results []domain.Showtime
	return results, cur.All(ctx, &results)
}

package booking

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"cinema-booking/backend/internal/domain"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

// AdminListBookings returns paginated bookings with optional filters.
//
// GET /api/admin/bookings
//
//	?showtimeId=<hex>     — exact showtime
//	&movieTitle=<str>     — case-insensitive substring match on showtime.movieTitle
//	&date=YYYY-MM-DD      — showtimes whose startTime falls on this UTC date
//	&userId=<hex>         — exact user
//	&page=1&limit=20
func (h *Handler) AdminListBookings(c *gin.Context) {
	ctx := c.Request.Context()
	page, limit := parsePagination(c)
	filter := bson.M{}

	if sid := c.Query("showtimeId"); sid != "" {
		id, err := bson.ObjectIDFromHex(sid)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid showtimeId"})
			return
		}
		filter["showtimeId"] = id
	}

	if uid := c.Query("userId"); uid != "" {
		id, err := bson.ObjectIDFromHex(uid)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid userId"})
			return
		}
		filter["userId"] = id
	}

	// movieTitle and date both resolve to a set of showtime IDs that are merged
	// into the filter. Two-query approach keeps the handler simple (no $lookup).
	if movie := c.Query("movieTitle"); movie != "" {
		ids, err := h.showtimeIDsByMovie(ctx, movie)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "showtime lookup failed"})
			return
		}
		if err := mergeShowtimeFilter(filter, ids); err != nil {
			// mergeShowtimeFilter returns a sentinel when the intersection is empty —
			// return the empty page immediately rather than running a query that can
			// never return results.
			c.JSON(http.StatusOK, emptyBookingsPage(page))
			return
		}
	}

	if dateStr := c.Query("date"); dateStr != "" {
		ids, err := h.showtimeIDsByDate(ctx, dateStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid date — use YYYY-MM-DD"})
			return
		}
		if err := mergeShowtimeFilter(filter, ids); err != nil {
			c.JSON(http.StatusOK, emptyBookingsPage(page))
			return
		}
	}

	total, err := h.db.Collection("bookings").CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "count failed"})
		return
	}

	skip := int64((page - 1) * limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip(skip).
		SetLimit(int64(limit))

	cursor, err := h.db.Collection("bookings").Find(ctx, filter, opts)
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
	c.JSON(http.StatusOK, gin.H{
		"bookings":   results,
		"count":      len(results),
		"total":      total,
		"page":       page,
		"totalPages": totalPages(total, limit),
	})
}

// AdminListAuditLogs returns paginated audit log entries with optional filters.
//
// GET /api/admin/audit-logs
//
//	?action=<str>         — exact action name (SEAT_LOCKED, SEAT_BOOKED, SEAT_RELEASED, …)
//	&showtimeId=<hex>
//	&userId=<hex>
//	&page=1&limit=20
func (h *Handler) AdminListAuditLogs(c *gin.Context) {
	ctx := c.Request.Context()
	page, limit := parsePagination(c)
	filter := bson.M{}

	if action := c.Query("action"); action != "" {
		filter["action"] = action
	}
	if sid := c.Query("showtimeId"); sid != "" {
		id, err := bson.ObjectIDFromHex(sid)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid showtimeId"})
			return
		}
		filter["showtimeId"] = id
	}
	if uid := c.Query("userId"); uid != "" {
		id, err := bson.ObjectIDFromHex(uid)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid userId"})
			return
		}
		filter["userId"] = id
	}

	total, err := h.db.Collection("audit_logs").CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "count failed"})
		return
	}

	skip := int64((page - 1) * limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip(skip).
		SetLimit(int64(limit))

	cursor, err := h.db.Collection("audit_logs").Find(ctx, filter, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}
	defer cursor.Close(ctx)

	var results []domain.AuditLog
	if err := cursor.All(ctx, &results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decode failed"})
		return
	}
	if results == nil {
		results = []domain.AuditLog{}
	}
	c.JSON(http.StatusOK, gin.H{
		"logs":       results,
		"count":      len(results),
		"total":      total,
		"page":       page,
		"totalPages": totalPages(total, limit),
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func parsePagination(c *gin.Context) (page, limit int) {
	page, limit = 1, defaultPageLimit
	if p, err := strconv.Atoi(c.DefaultQuery("page", "1")); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(defaultPageLimit))); err == nil && l > 0 {
		if l > maxPageLimit {
			l = maxPageLimit
		}
		limit = l
	}
	return
}

// showtimeIDsByMovie returns IDs of showtimes whose movieTitle contains the
// given string (case-insensitive). Returns empty slice (not error) when none match.
func (h *Handler) showtimeIDsByMovie(ctx context.Context, movieTitle string) ([]bson.ObjectID, error) {
	cursor, err := h.db.Collection("showtimes").Find(ctx,
		bson.M{"movieTitle": bson.M{"$regex": movieTitle, "$options": "i"}},
		options.Find().SetProjection(bson.M{"_id": 1}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	return collectIDs(ctx, cursor)
}

// showtimeIDsByDate returns IDs of showtimes whose startTime falls on the given
// UTC calendar date. dateStr must be "YYYY-MM-DD".
func (h *Handler) showtimeIDsByDate(ctx context.Context, dateStr string) ([]bson.ObjectID, error) {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil, err
	}
	start := t.UTC()
	end := start.Add(24 * time.Hour)
	cursor, err := h.db.Collection("showtimes").Find(ctx,
		bson.M{"startTime": bson.M{"$gte": start, "$lt": end}},
		options.Find().SetProjection(bson.M{"_id": 1}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	return collectIDs(ctx, cursor)
}

func collectIDs(ctx context.Context, cursor interface {
	Next(context.Context) bool
	Decode(interface{}) error
	Err() error
}) ([]bson.ObjectID, error) {
	var ids []bson.ObjectID
	for cursor.Next(ctx) {
		var doc struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if cursor.Decode(&doc) == nil {
			ids = append(ids, doc.ID)
		}
	}
	return ids, cursor.Err()
}

// errEmptyIntersection is a sentinel returned when a filter combination can
// never match any document (e.g. movieTitle and showtimeId disagree).
var errEmptyIntersection = fmt.Errorf("empty intersection")

// mergeShowtimeFilter adds ids to filter["showtimeId"] as an $in constraint.
// If "showtimeId" was already set to a single ObjectID it intersects: if that
// ID is not in ids, returns errEmptyIntersection so the caller can short-circuit.
func mergeShowtimeFilter(filter bson.M, ids []bson.ObjectID) error {
	if len(ids) == 0 {
		return errEmptyIntersection
	}
	existing, hasExact := filter["showtimeId"]
	if !hasExact {
		filter["showtimeId"] = bson.M{"$in": ids}
		return nil
	}
	// Already constrained to a single OID by the showtimeId= param.
	if oid, ok := existing.(bson.ObjectID); ok {
		for _, id := range ids {
			if id == oid {
				return nil // OID satisfies the new constraint — keep exact match
			}
		}
		return errEmptyIntersection
	}
	// Already an $in (two merge calls). Intersect the slices.
	if inMap, ok := existing.(bson.M); ok {
		prev, _ := inMap["$in"].([]bson.ObjectID)
		intersection := intersectIDs(prev, ids)
		if len(intersection) == 0 {
			return errEmptyIntersection
		}
		filter["showtimeId"] = bson.M{"$in": intersection}
	}
	return nil
}

func intersectIDs(a, b []bson.ObjectID) []bson.ObjectID {
	set := make(map[bson.ObjectID]bool, len(b))
	for _, id := range b {
		set[id] = true
	}
	var out []bson.ObjectID
	for _, id := range a {
		if set[id] {
			out = append(out, id)
		}
	}
	return out
}

func totalPages(total int64, limit int) int {
	if limit <= 0 {
		return 0
	}
	return int(math.Ceil(float64(total) / float64(limit)))
}

func emptyBookingsPage(page int) gin.H {
	return gin.H{
		"bookings":   []domain.Booking{},
		"count":      0,
		"total":      0,
		"page":       page,
		"totalPages": 0,
	}
}

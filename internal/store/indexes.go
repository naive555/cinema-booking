// Package store handles MongoDB index creation and other persistence setup.
package store

import (
	"context"
	"fmt"
	"log"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// indexDef pairs a collection name with an index model.
type indexDef struct {
	collection string
	model      mongo.IndexModel
}

// requiredIndexes lists every index the application needs.
// CreateOne is idempotent: same name + spec → no-op; conflicting spec → error.
var requiredIndexes = []indexDef{
	// ── bookings ──────────────────────────────────────────────────────────────
	// THE safety net: prevents double-booking even if the distributed lock fails.
	{
		"bookings",
		mongo.IndexModel{
			Keys:    bson.D{{Key: "showtimeId", Value: 1}, {Key: "seatId", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uq_showtime_seat"),
		},
	},
	{
		"bookings",
		mongo.IndexModel{
			Keys:    bson.D{{Key: "showtimeId", Value: 1}},
			Options: options.Index().SetName("idx_bookings_showtime"),
		},
	},
	{
		"bookings",
		mongo.IndexModel{
			Keys:    bson.D{{Key: "userId", Value: 1}},
			Options: options.Index().SetName("idx_bookings_user"),
		},
	},
	// ── seats ─────────────────────────────────────────────────────────────────
	// Prevents duplicate seat positions within a showtime (also used by seed upserts).
	{
		"seats",
		mongo.IndexModel{
			Keys:    bson.D{{Key: "showtimeId", Value: 1}, {Key: "row", Value: 1}, {Key: "number", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uq_seat_position"),
		},
	},
	{
		"seats",
		mongo.IndexModel{
			Keys:    bson.D{{Key: "showtimeId", Value: 1}},
			Options: options.Index().SetName("idx_seats_showtime"),
		},
	},
	// ── users ─────────────────────────────────────────────────────────────────
	{
		"users",
		mongo.IndexModel{
			Keys:    bson.D{{Key: "email", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uq_user_email"),
		},
	},
}

// EnsureIndexes creates all required indexes, logging each result.
// It is called once at startup and is safe to call repeatedly.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	for _, d := range requiredIndexes {
		name, err := db.Collection(d.collection).Indexes().CreateOne(ctx, d.model)
		if err != nil {
			return fmt.Errorf("store: ensure index %s.%s: %w", d.collection, name, err)
		}
		log.Printf("store: index ok — %s.%s", d.collection, name)
	}
	return nil
}

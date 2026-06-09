// Package seed populates the database with a minimal fixture set for demos and development.
// All operations use upsert so the seed is safe to run on every startup.
package seed

import (
	"cinema-booking/internal/domain"
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Stable ObjectIDs let us upsert by _id so re-runs are always no-ops.
var (
	showtime1ID = mustID("000000000000000000000001")
	showtime2ID = mustID("000000000000000000000002")
)

func mustID(hex string) bson.ObjectID {
	id, err := bson.ObjectIDFromHex(hex)
	if err != nil {
		panic(fmt.Sprintf("seed: bad ObjectID literal %q: %v", hex, err))
	}
	return id
}

// Run inserts seed data idempotently. Safe to call on every startup.
func Run(ctx context.Context, db *mongo.Database) error {
	if err := seedShowtimes(ctx, db); err != nil {
		return err
	}
	return seedSeats(ctx, db)
}

func seedShowtimes(ctx context.Context, db *mongo.Database) error {
	base := time.Now().Truncate(time.Hour)
	fixtures := []domain.Showtime{
		{
			ID:         showtime1ID,
			MovieTitle: "Inception",
			Hall:       "Hall A",
			StartTime:  base.Add(24 * time.Hour),
			EndTime:    base.Add(24*time.Hour + 148*time.Minute),
			CreatedAt:  base,
		},
		{
			ID:         showtime2ID,
			MovieTitle: "Interstellar",
			Hall:       "Hall B",
			StartTime:  base.Add(48 * time.Hour),
			EndTime:    base.Add(48*time.Hour + 169*time.Minute),
			CreatedAt:  base,
		},
	}

	coll := db.Collection("showtimes")
	inserted := 0
	for _, st := range fixtures {
		res, err := coll.UpdateOne(
			ctx,
			bson.M{"_id": st.ID},
			bson.M{"$setOnInsert": st},
			options.UpdateOne().SetUpsert(true),
		)
		if err != nil {
			return fmt.Errorf("seed: showtime %q: %w", st.MovieTitle, err)
		}
		if res.UpsertedCount > 0 {
			inserted++
		}
	}
	log.Printf("seed: showtimes — %d inserted, %d already existed", inserted, len(fixtures)-inserted)
	return nil
}

func seedSeats(ctx context.Context, db *mongo.Database) error {
	rows := []string{"A", "B", "C", "D", "E"}
	numbers := []int{1, 2, 3, 4, 5, 6, 7, 8}
	showtimeIDs := []bson.ObjectID{showtime1ID, showtime2ID}

	coll := db.Collection("seats")
	inserted, total := 0, 0
	for _, stID := range showtimeIDs {
		for _, row := range rows {
			for _, num := range numbers {
				total++
				res, err := coll.UpdateOne(
					ctx,
					bson.M{"showtimeId": stID, "row": row, "number": num},
					bson.M{"$setOnInsert": domain.Seat{
						ShowtimeID: stID,
						Row:        row,
						Number:     num,
					}},
					options.UpdateOne().SetUpsert(true),
				)
				if err != nil {
					return fmt.Errorf("seed: seat %s/%s%d: %w", stID.Hex(), row, num, err)
				}
				if res.UpsertedCount > 0 {
					inserted++
				}
			}
		}
	}
	log.Printf("seed: seats — %d inserted, %d already existed (total expected=%d)", inserted, total-inserted, total)
	return nil
}

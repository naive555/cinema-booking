// Package domain contains the shared data types for the cinema booking system.
// No business logic lives here — only structs, enums, and BSON/JSON tags.
package domain

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Role is the access level minted into the JWT at login time.
type Role string

const (
	RoleUser  Role = "USER"
	RoleAdmin Role = "ADMIN"
)

// SeatStatus is the computed state of a seat at read time.
// It is NOT stored in MongoDB; it is derived by merging:
//   - BOOKED  → a Booking document exists in Mongo
//   - LOCKED  → an ephemeral Redis key exists (seat:lock:{showtimeId}:{seatId})
//   - AVAILABLE → neither of the above
type SeatStatus string

const (
	StatusAvailable SeatStatus = "AVAILABLE"
	StatusLocked    SeatStatus = "LOCKED"
	StatusBooked    SeatStatus = "BOOKED"
)

// User is stored in the "users" collection.
type User struct {
	ID        bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Email     string        `bson:"email"         json:"email"`
	Name      string        `bson:"name"          json:"name"`
	Role      Role          `bson:"role"          json:"role"`
	CreatedAt time.Time     `bson:"createdAt"     json:"createdAt"`
}

// Showtime is stored in the "showtimes" collection.
type Showtime struct {
	ID         bson.ObjectID `bson:"_id,omitempty" json:"id"`
	MovieTitle string        `bson:"movieTitle"    json:"movieTitle"`
	Hall       string        `bson:"hall"          json:"hall"`
	StartTime  time.Time     `bson:"startTime"     json:"startTime"`
	EndTime    time.Time     `bson:"endTime"       json:"endTime"`
	CreatedAt  time.Time     `bson:"createdAt"     json:"createdAt"`
}

// Seat is stored in the "seats" collection.
// Status is intentionally absent — see SeatStatus above.
type Seat struct {
	ID         bson.ObjectID `bson:"_id,omitempty" json:"id"`
	ShowtimeID bson.ObjectID `bson:"showtimeId"    json:"showtimeId"`
	Row        string        `bson:"row"           json:"row"`
	Number     int           `bson:"number"        json:"number"`
}

// Booking is stored in the "bookings" collection.
// A unique compound index on {showtimeId, seatId} is the last line of defence
// against double-booking even if the distributed lock has a bug.
type Booking struct {
	ID         bson.ObjectID `bson:"_id,omitempty" json:"id"`
	ShowtimeID bson.ObjectID `bson:"showtimeId"    json:"showtimeId"`
	SeatID     bson.ObjectID `bson:"seatId"        json:"seatId"`
	UserID     bson.ObjectID `bson:"userId"        json:"userId"`
	CreatedAt  time.Time     `bson:"createdAt"     json:"createdAt"`
}

// AuditLog is stored in the "audit_logs" collection and records every
// significant action (book, cancel, admin overrides) for traceability.
type AuditLog struct {
	ID         bson.ObjectID          `bson:"_id,omitempty"  json:"id"`
	Action     string                 `bson:"action"         json:"action"`
	UserID     bson.ObjectID          `bson:"userId"         json:"userId"`
	ShowtimeID bson.ObjectID          `bson:"showtimeId"     json:"showtimeId"`
	SeatID     bson.ObjectID          `bson:"seatId"         json:"seatId"`
	Meta       map[string]interface{} `bson:"meta,omitempty" json:"meta,omitempty"`
	CreatedAt  time.Time              `bson:"createdAt"      json:"createdAt"`
}
